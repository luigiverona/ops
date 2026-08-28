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
            "${TMPDIR:-/tmp}"/ops-install.*) rm -rf -- "$tmp" ;;
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

printf '%s\n' 'ops prepares an official Arch Linux x86_64 workstation.'
printf '%s\n' 'The installer verifies a signed checksum manifest and binary before requesting sudo.'

[ "$(id -u)" -ne 0 ] || fail 'run as a normal user; root would create incorrectly owned user configuration and cannot safely build AUR packages'
[ "$(uname -s)" = Linux ] || fail 'only official Arch Linux is supported'
[ "$(uname -m)" = x86_64 ] || fail 'only x86_64 is supported'
[ -r /etc/os-release ] || fail 'cannot identify the operating system'
os_id=$(awk -F= '$1 == "ID" { value=$2; gsub(/^"|"$/, "", value); print value; exit }' /etc/os-release)
[ "$os_id" = arch ] || fail 'only official Arch Linux is supported; derivatives are not supported'

for command in curl sha256sum gpg gpgv awk mktemp sudo install mv cp rm chmod; do
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

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ops-install.XXXXXXXX") || fail 'could not create a temporary directory'
chmod 700 "$tmp"
base=$release_base/$version
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || fail 'could not download checksum manifest'
curl -fsSL "$base/checksums.txt.sig" -o "$tmp/checksums.txt.sig" || fail 'could not download manifest signature'
curl -fsSL "$base/ops-linux-x86_64" -o "$tmp/ops-linux-x86_64" || fail 'could not download release binary'

cat > "$tmp/signing-key.asc" <<'OPS_SIGNING_KEY'
@OPS_SIGNING_PUBLIC_KEY@
OPS_SIGNING_KEY

shown=$(gpg --batch --with-colons --show-keys "$tmp/signing-key.asc" 2>/dev/null) || fail 'embedded release signing key is invalid'
printf '%s\n' "$shown" | awk -F: -v fingerprint="$fingerprint" '$1 == "fpr" && toupper($10) == fingerprint { found=1 } END { exit !found }' || fail 'release signing key fingerprint mismatch'
GNUPGHOME=$tmp gpg --batch --no-default-keyring --keyring "$tmp/trustedkeys.gpg" --import "$tmp/signing-key.asc" >/dev/null 2>&1 || fail 'could not create isolated release keyring'
signature_status=$(GNUPGHOME=$tmp gpgv --status-fd 1 --keyring "$tmp/trustedkeys.gpg" "$tmp/checksums.txt.sig" "$tmp/checksums.txt" 2>/dev/null) || fail 'release signature verification failed'
printf '%s\n' "$signature_status" | awk -v fingerprint="$fingerprint" '$1 == "[GNUPG:]" && $2 == "VALIDSIG" && toupper($3) == fingerprint { found=1 } END { exit !found }' || fail 'release was not signed by the pinned release-signing key'

expected=$(awk '$2 == "ops-linux-x86_64" || $2 == "*ops-linux-x86_64" { count++; hash=tolower($1) } END { if (count == 1 && hash ~ /^[0-9a-f]{64}$/) print hash; else exit 1 }' "$tmp/checksums.txt") || fail 'signed checksum manifest is invalid'
actual=$(sha256sum "$tmp/ops-linux-x86_64" | awk '{ print tolower($1) }')
[ "$actual" = "$expected" ] || fail 'release binary checksum verification failed'
chmod 755 "$tmp/ops-linux-x86_64"
[ "$("$tmp/ops-linux-x86_64" --version 2>/dev/null)" = "ops $version" ] || fail 'verified binary reports an unexpected version'

printf '\nVerified\n  release         %s\n  signature       valid\n  sha256          valid\n  install path    %s\n' "$version" "$target"
printf 'Install this verified release? [Y/n] ' > /dev/tty
IFS= read -r answer < /dev/tty || fail 'could not read confirmation'
case "$answer" in
    ''|y|Y|yes|YES|Yes) ;;
    n|N|no|NO|No) printf '%s\n' 'Installation skipped.'; exit 0 ;;
    *) fail 'invalid response; enter yes or no' ;;
esac

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

config_dir=$HOME/.config/ops
config=$config_dir/apps.toml
mkdir -p "$config_dir"
created=no
if [ ! -e "$config" ] && [ ! -L "$config" ]; then
    if (umask 077; set -C; cat > "$config" <<'OPS_CONFIG'
# ops configuration
#
# Define each application using the "source:package" format.
# Supported sources are "pacman", "aur", and "flatpak".
# For pacman and AUR, use the exact package name; for Flatpak, use the exact application ID.
# Add applications under the appropriate category below, and leave unused categories empty.
# ops automatically installs and configures any required dependencies or system prerequisites.
# ops installs declared applications but never removes applications that are no longer listed.
# Applications that cannot be installed are skipped and reported as unresolved when the run finishes.
#
# Example:
#
# [apps]
# browser = ["aur:librewolf-bin", "aur:mullvad-browser-bin"]
# vpn = ["pacman:mullvad-vpn"]
# vault = ["pacman:bitwarden"]
# mail = ["flatpak:com.tutanota.Tutanota"]
# social = ["pacman:discord"]
# music = ["pacman:spotify-launcher"]
# game = ["pacman:steam"]

version = 1

[apps]
browser = []
vpn = []
vault = []
mail = []
social = []
music = []
game = []
OPS_CONFIG
    ); then
        created=yes
    fi
fi

printf '\nInstalled\n  binary          %s\n  configuration   %s\n' "$target" "$config"
if [ "$created" = yes ]; then
    printf '%s\n' 'Edit apps.toml if desired, then run ops.'
else
    printf '%s\n' 'Existing apps.toml was preserved. Run ops when ready.'
fi
