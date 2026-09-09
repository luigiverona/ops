#!/bin/sh
set -eu

release_base=${OPS_RELEASE_BASE:-https://ops.luigiverona.dev/releases}
target=/usr/local/bin/ops
fingerprint='@OPS_SIGNING_FINGERPRINT@'

fail() {
    printf 'ops installer: %s\n' "$*" >&2
    exit 2
}

cleanup() {
    if [ -n "${tmp:-}" ]; then
        case "$tmp" in
            "${tmp_parent:-/tmp}"/ops-install.*) rm -rf -- "$tmp" ;;
        esac
    fi
    if [ -n "${staged:-}" ]; then
        sudo -n rm -f -- "$staged" >/dev/null 2>&1 || true
    fi
    if [ -n "${backup:-}" ] && [ "${keep_backup:-no}" != yes ]; then
        sudo -n rm -f -- "$backup" >/dev/null 2>&1 || true
    fi
}

trap cleanup EXIT HUP INT TERM

[ "$(id -u)" -ne 0 ] || fail 'run as a normal user; root would create incorrectly owned user configuration and cannot safely build AUR packages'
[ "$(uname -s)" = Linux ] || fail 'only official Arch Linux is supported'
[ "$(uname -m)" = x86_64 ] || fail 'only x86_64 is supported'
[ -r /etc/os-release ] || fail 'cannot identify the operating system'
os_id=$(awk -F= '$1 == "ID" { value=$2; gsub(/^"|"$/, "", value); print value; exit }' /etc/os-release)
[ "$os_id" = arch ] || fail 'only official Arch Linux is supported; derivatives are not supported'

for command in curl sha256sum gpg awk mktemp sudo install mv cp rm chmod mkdir ln rmdir; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required for verified installation"
done
[ -r /dev/tty ] && [ -w /dev/tty ] || fail 'interactive installation requires a usable terminal'

[ "$fingerprint" != '@OPS_SIGNING_FINGERPRINT@' ] || fail 'release signing trust is not configured; refusing an unverified installation'
case "$fingerprint" in
    *[!0-9A-F]*|'') fail 'the pinned release signing fingerprint is invalid' ;;
esac
[ "${#fingerprint}" -eq 40 ] || fail 'the pinned release signing fingerprint is invalid'

version=$(curl -fsSL "$release_base/latest") || fail 'could not resolve the latest release'
version=$(printf '%s' "$version" | awk 'NF { gsub(/[[:space:]]/, ""); print; exit }')
printf '%s\n' "$version" | awk -F. 'NF == 3 && $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ && $3 ~ /^[0-9]+$/ { ok=1 } END { exit !ok }' || fail 'release service returned an invalid version'

tmp_parent=$(CDPATH= cd -P "${TMPDIR:-/tmp}" && pwd -P) || fail 'could not resolve temporary directory'
tmp=$(mktemp -d "$tmp_parent/ops-install.XXXXXXXX") || fail 'could not create a temporary directory'
chmod 700 "$tmp"
gpg_home=$tmp/gnupg
mkdir "$gpg_home" || fail 'could not create an isolated GPG home'
chmod 700 "$gpg_home"
base=$release_base/$version
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail 'could not download checksum manifest'
curl -fsSL "$base/checksums.txt.sig" -o "$tmp/checksums.txt.sig" || fail 'could not download manifest signature'
curl -fsSL "$base/ops-linux-x86_64" -o "$tmp/ops-linux-x86_64" || fail 'could not download release binary'

cat > "$tmp/signing-key.asc" <<'OPS_SIGNING_KEY'
@OPS_SIGNING_PUBLIC_KEY@
OPS_SIGNING_KEY

shown=$(gpg --homedir "$gpg_home" --no-options --batch --no-tty --with-colons --show-keys "$tmp/signing-key.asc" 2>/dev/null) || fail 'embedded release signing key is invalid'
printf '%s\n' "$shown" | awk -F: -v fingerprint="$fingerprint" '
    $1 == "sub" { active=($2 != "r" && $2 != "e" && tolower($12) ~ /s/); next }
    active && $1 == "fpr" && toupper($10) == fingerprint { found=1 }
    $1 != "fpr" { active=0 }
    END { exit !found }
' || fail 'pinned release-signing subkey is absent, expired, revoked, or not signing-capable'
gpg --homedir "$gpg_home" --no-options --batch --no-tty --status-fd 1 --import-options import-minimal --import "$tmp/signing-key.asc" >/dev/null 2>&1 || fail 'could not create isolated release keyring'
signature_status=''
if signature_status=$(gpg --homedir "$gpg_home" --no-options --batch --no-tty --status-fd 1 --trust-model always --no-auto-key-retrieve --verify "$tmp/checksums.txt.sig" "$tmp/checksums.txt" 2>/dev/null); then
    signature_exit=0
else
    signature_exit=$?
fi
[ "$signature_exit" -eq 0 ] || fail 'release signature verification failed'
printf '%s\n' "$signature_status" | awk -v fingerprint="$fingerprint" '
    $1 != "[GNUPG:]" { next }
    $2 == "VALIDSIG" {
        total++
        if (toupper($3) == fingerprint) valid++
        else invalid=1
    }
    $2 == "REVKEYSIG" || $2 == "EXPKEYSIG" || $2 == "EXPSIG" ||
    $2 == "BADSIG" || $2 == "ERRSIG" || $2 == "NO_PUBKEY" ||
    $2 == "NODATA" || $2 == "BADARMOR" || $2 == "KEYEXPIRED" ||
    $2 == "SIGEXPIRED" || $2 == "KEYREVOKED" || $2 == "FAILURE" ||
    $2 == "ERROR" || $2 == "UNEXPECTED" { invalid=1 }
    $2 != "VALIDSIG" && $2 != "NEWSIG" && $2 != "KEY_CONSIDERED" &&
    $2 != "SIG_ID" && $2 != "GOODSIG" && $2 !~ /^TRUST_/ { invalid=1 }
    END { exit !(valid == 1 && total == 1 && !invalid) }
' || fail 'release signature status is invalid or does not match the pinned release-signing subkey'

expected=$(awk '$2 == "ops-linux-x86_64" || $2 == "*ops-linux-x86_64" { count++; hash=tolower($1) } END { if (count == 1 && hash ~ /^[0-9a-f]{64}$/) print hash; else exit 1 }' "$tmp/checksums.txt") || fail 'signed checksum manifest is invalid'
actual=$(sha256sum "$tmp/ops-linux-x86_64" | awk '{ print tolower($1) }')
[ "$actual" = "$expected" ] || fail 'release binary checksum verification failed'
chmod 755 "$tmp/ops-linux-x86_64"
[ "$("$tmp/ops-linux-x86_64" --version 2>/dev/null)" = "ops $version" ] || fail 'verified binary reports an unexpected version'

printf 'ops %s verified.\n' "$version"
printf 'Install to %s? [Y/n] ' "$target" > /dev/tty
IFS= read -r answer < /dev/tty || fail 'could not read confirmation'
case "$answer" in
    ''|y|Y|yes|YES|Yes) ;;
    n|N|no|NO|No) printf '%s\n' 'No changes made.'; exit 0 ;;
    *) fail 'invalid response; enter yes or no' ;;
esac

config_dir=$HOME/.config/ops
config=$config_dir/apps.toml
config_parent=$HOME/.config
binary_installed=no

config_fail() {
    if [ "$binary_installed" = yes ]; then
        printf '\nInstalled ops %s, but configuration setup failed for %s.\nThe binary remains installed.\n' "$version" "$config" >&2
    fi
    fail "$*"
}

# Read-only preflight; repeat after directory creation and failed exclusive publication.
# The user's .config parent may be a symlink, but managed targets must not be.
check_config_path() {
    if [ -e "$config_parent" ] && [ ! -d "$config_parent" ]; then
        config_fail "configuration parent $config_parent is not a directory"
    fi
    if [ -L "$config_dir" ]; then
        config_fail 'configuration directory ~/.config/ops is a symlink; refusing unsafe configuration creation'
    fi
    if [ -e "$config_dir" ] && [ ! -d "$config_dir" ]; then
        config_fail 'configuration path ~/.config/ops is not a directory'
    fi
    if [ "${config_pinned:-no}" = yes ] && [ ! "$config_dir" -ef . ]; then
        config_fail 'configuration directory changed during installation; inspect the path and rerun the installer'
    fi
    if [ -L "$config" ]; then
        config_fail 'configuration file ~/.config/ops/apps.toml is a symlink; refusing unsafe configuration creation'
    fi
    if [ -e "$config" ] && [ ! -f "$config" ]; then
        config_fail 'configuration path ~/.config/ops/apps.toml is not a regular file'
    fi
}
check_config_path

sudo -v || fail 'sudo authorization failed'
suffix=$$
staged=$target.ops-new-$suffix
backup=$target.ops-backup-$suffix
keep_backup=no
sudo -n rm -f -- "$staged" "$backup"
sudo -n install -m 0755 -o root -g root -- "$tmp/ops-linux-x86_64" "$staged" || fail 'could not stage binary'
[ "$("$staged" --version 2>/dev/null)" = "ops $version" ] || fail 'staged binary verification failed'
had_target=no
if [ -L "$target" ]; then
    fail 'existing install target is a symlink; refusing unsafe replacement'
fi
if [ -e "$target" ] && [ ! -f "$target" ]; then
    fail 'existing install target is not a regular file'
fi
if [ -e "$target" ] || [ -L "$target" ]; then
    had_target=yes
    sudo -n cp --preserve=mode,ownership,timestamps -- "$target" "$backup" || fail 'could not preserve existing binary'
fi
sudo -n mv -- "$staged" "$target" || fail 'could not atomically install binary'
if [ "$("$target" --version 2>/dev/null || true)" != "ops $version" ]; then
    if [ "$had_target" = yes ]; then
        if ! sudo -n mv -- "$backup" "$target"; then
            keep_backup=yes
            fail "installation failed and the previous binary could not be restored; backup retained at $backup"
        fi
    else
        sudo -n rm -f -- "$target"
    fi
    fail 'installed binary verification failed; previous binary was restored when available'
fi
sudo -n rm -f -- "$backup"

binary_installed=yes
check_config_path
(umask 077; mkdir -p "$config_parent") || config_fail "could not create configuration parent $config_parent; fix the path and rerun the installer"
if [ ! -d "$config_dir" ]; then
    (umask 077; mkdir "$config_dir") || {
        check_config_path
        [ -d "$config_dir" ] || config_fail "could not create configuration directory $config_dir; fix the path and rerun the installer"
    }
fi
check_config_path
# Pin the physical directory before writing. Relative operations keep using it
# even if another process replaces ~/.config/ops after the checks.
config_physical=$(CDPATH= cd -P "$config_parent" && pwd -P) || config_fail 'could not resolve configuration parent'
CDPATH= cd -P "$config_dir" || config_fail 'could not enter configuration directory'
[ "$(pwd -P)" = "$config_physical/ops" ] || config_fail 'configuration directory changed during installation'
config_pinned=yes
check_config_path
created=no
if [ ! -e "$config" ]; then
    # Stage privately on the same filesystem, then link without replacement.
    # Never expose a partial apps.toml or open a raced-in device/FIFO for writing.
    config_stage=$(umask 077; mktemp -d ./.ops-config.XXXXXXXX) || config_fail "could not create configuration staging directory; fix permissions and rerun the installer"
    result=0
    (
        CDPATH= cd -P "$config_stage" || config_fail 'could not enter configuration staging directory'
        [ "$(pwd -P)" = "$config_physical/ops/${config_stage#./}" ] || config_fail 'configuration staging directory changed during installation'
        # Cleanup is confined to the private staging directory, never apps.toml
        # in the managed directory, which another process may have replaced.
        trap 'rm -f -- ./apps.toml' EXIT
        trap 'exit 2' HUP INT TERM
        umask 077
        if ! cat > ./apps.toml <<'OPS_CONFIG'
# Applications managed by ops.
# Use exact, case-sensitive identifiers from the selected source.
#
# Official Arch packages (pacman):
#   https://archlinux.org/packages/
#   pacman -Ss SEARCH_TERM
#
# AUR packages (aur):
#   https://aur.archlinux.org/
#
# Flatpak application IDs (flatpak):
#   https://flathub.org/
#   flatpak search SEARCH_TERM

# apps.toml format version. Independent of the ops program version.
version = 2

pacman = []
aur = []
flatpak = []
OPS_CONFIG
        then
            config_fail "could not write configuration; installer did not create apps.toml; fix storage or permissions and rerun the installer"
        fi
        # The parent shell stays in the pinned destination while this subshell
        # writes in staging. Linux procfs lets ln use that directory directly.
        ln -T -- ./apps.toml /proc/$$/cwd/apps.toml || exit 4
    ) || result=$?
    rmdir -- "$config_stage" || config_fail 'could not remove configuration staging directory; inspect the path before retrying'
    case "$result" in
        0) created=yes ;;
        4)
            check_config_path
            # A concurrent regular file belongs to its creator and is preserved.
            [ -f "$config" ] || config_fail "could not create $config; fix the path or permissions and rerun the installer"
            ;;
        *) exit "$result" ;;
    esac
fi

check_config_path

printf '\nInstalled ops %s.\n' "$version"
if [ "$created" = yes ]; then
    printf '%s\n\n' 'Created ~/.config/ops/apps.toml.'
    printf '%s\n' 'Optionally add applications; the file explains names and sources.'
else
    printf '%s\n\n' 'Preserved existing ~/.config/ops/apps.toml.'
fi
printf '%s\n' 'Run ops to review workstation setup, including Git, SSH, and GitHub.'
