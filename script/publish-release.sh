#!/bin/sh
set -eu

bucket=ops-releases
profile=${OPS_R2_PROFILE:-ops-r2}
public_origin=https://ops.luigiverona.dev
immutable_cache='public, max-age=31536000, immutable'
mutable_cache='no-store'

fail() {
    printf 'publish-release: %s\n' "$*" >&2
    exit 1
}

usage() {
    printf '%s\n' 'usage: script/publish-release.sh VERSION' >&2
    exit 2
}

valid_version() {
    printf '%s\n' "$1" | awk -F. '
        NF == 3 && $1 ~ /^[0-9]+$/ && $2 ~ /^[0-9]+$/ &&
        $3 ~ /^[0-9]+$/ { ok=1 }
        END { exit !ok }
    '
}

[ "$#" -eq 1 ] || usage
version=$1
valid_version "$version" || usage
tag=v$version

endpoint=${OPS_R2_ENDPOINT:-}
[ -n "$endpoint" ] || fail 'OPS_R2_ENDPOINT is required'
case "$endpoint" in
    https://*) endpoint_host=${endpoint#https://} ;;
    *) fail 'OPS_R2_ENDPOINT must be a valid Cloudflare R2 HTTPS endpoint' ;;
esac
case "$endpoint_host" in
    */) endpoint_host=${endpoint_host%/} ;;
esac
case "$endpoint_host" in
    ''|*/*|*'?'*|*'#'*|*'@'*|*':'*)
        fail 'OPS_R2_ENDPOINT must be a valid Cloudflare R2 HTTPS endpoint'
        ;;
esac
case "$endpoint_host" in
    *.r2.cloudflarestorage.com)
        account_component=${endpoint_host%.r2.cloudflarestorage.com}
        ;;
    *)
        fail 'OPS_R2_ENDPOINT must be a valid Cloudflare R2 HTTPS endpoint'
        ;;
esac
case "$account_component" in
    ''|*[!0-9A-Fa-f]*)
        fail 'OPS_R2_ENDPOINT must be a valid Cloudflare R2 HTTPS endpoint'
        ;;
esac
[ "${#account_component}" -eq 32 ] ||
    fail 'OPS_R2_ENDPOINT must be a valid Cloudflare R2 HTTPS endpoint'
endpoint=https://$endpoint_host

for command in git aws curl cmp awk grep cat gpg sha256sum mktemp mkdir chmod rm sh cp flock; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done

# Prevent the AWS CLI from invoking a configured pager.
export AWS_PAGER=

root=$(git rev-parse --show-toplevel 2>/dev/null) ||
    fail 'run from the ops Git repository'
cd "$root"
[ ! -L dist ] || fail 'dist is not a safe release directory'
[ -z "$(git status --porcelain=v1 --untracked-files=all)" ] ||
    fail 'repository is not clean'

head=$(git rev-parse HEAD)
tag_commit=$(git rev-parse --verify "refs/tags/$tag^{commit}" 2>/dev/null) ||
    fail "exact intended tag $tag does not exist"
[ "$head" = "$tag_commit" ] || fail "$tag does not identify the current commit"
[ "$(git describe --tags --exact-match HEAD 2>/dev/null)" = "$tag" ] ||
    fail "HEAD is not exactly tagged $tag"

# flock locks are released by the kernel on every exit path, including signals
# and crashes. The inert lock file may persist without creating a stale lock.
lock_file=$(git rev-parse --git-path ops-publish.lock) ||
    fail 'could not determine the local publication lock path'
if [ -e "$lock_file" ] || [ -L "$lock_file" ]; then
    [ -f "$lock_file" ] && [ ! -L "$lock_file" ] ||
        fail 'local publication lock path is unsafe'
fi
umask 077
exec 9>>"$lock_file" || fail 'could not open the local publication lock'
flock -n 9 || fail 'another local release publication is already running'

[ -d dist ] && [ ! -L dist ] || fail 'dist is not a safe release directory'
release_dir=dist/release-$tag
[ -d "$release_dir" ] && [ ! -L "$release_dir" ] ||
    fail "$release_dir is not a safe prepared release directory"
for name in ops-linux-x86_64 checksums.txt checksums.txt.sig; do
    path=$release_dir/$name
    [ -f "$path" ] && [ ! -L "$path" ] ||
        fail "$path is not a safe regular file"
done
for path in internal/release/signing-fingerprint internal/release/signing-key.asc; do
    [ -f "$path" ] && [ ! -L "$path" ] ||
        fail "$path is not a safe regular file"
done

tmp=$(mktemp -d "${TMPDIR:-/tmp}/ops-publish.XXXXXXXX") ||
    fail 'could not create publication staging directory'
cleanup() {
    rm -rf -- "$tmp"
}
trap cleanup 0
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

fingerprint=$(cat internal/release/signing-fingerprint)
case "$fingerprint" in
    ''|*[!0-9A-F]*) fail 'invalid signing fingerprint' ;;
esac
[ "${#fingerprint}" -eq 40 ] || fail 'invalid signing fingerprint'

gpg_home=$tmp/gnupg
mkdir "$gpg_home" || fail 'could not create isolated verification keyring'
chmod 0700 "$gpg_home"
shown=$(gpg --homedir "$gpg_home" --no-options --batch --no-tty \
    --with-colons --show-keys internal/release/signing-key.asc 2>/dev/null) ||
    fail 'embedded release signing key is invalid'
printf '%s\n' "$shown" | awk -F: -v fingerprint="$fingerprint" '
    $1 == "sub" { active=($2 != "r" && $2 != "e" && tolower($12) ~ /s/); next }
    active && $1 == "fpr" && toupper($10) == fingerprint { found=1 }
    $1 != "fpr" { active=0 }
    END { exit !found }
' || fail 'pinned signing subkey is absent, expired, revoked, or not signing-capable'
gpg --homedir "$gpg_home" --no-options --batch --no-tty \
    --status-fd 1 --import-options import-minimal \
    --import internal/release/signing-key.asc >/dev/null 2>&1 ||
    fail 'could not create isolated release keyring'

verify_artifacts() {
    verify_dir=$1
    verify_label=$2
    signature_status=''
    if signature_status=$(gpg \
        --homedir "$gpg_home" --no-options --batch --no-tty \
        --status-fd 1 --trust-model always --no-auto-key-retrieve \
        --verify "$verify_dir/checksums.txt.sig" "$verify_dir/checksums.txt" \
        2>/dev/null)
    then
        signature_exit=0
    else
        signature_exit=$?
    fi
    [ "$signature_exit" -eq 0 ] || fail "$verify_label signature verification failed"
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
    ' || fail "$verify_label signature status is invalid"

    expected=$(awk '
        $2 == "ops-linux-x86_64" || $2 == "*ops-linux-x86_64" {
            count++
            hash=tolower($1)
        }
        END {
            if (count == 1 && hash ~ /^[0-9a-f]{64}$/) print hash
            else exit 1
        }
    ' "$verify_dir/checksums.txt") || fail "$verify_label checksum manifest is invalid"
    actual=$(sha256sum "$verify_dir/ops-linux-x86_64" | awk '{ print tolower($1) }')
    [ "$actual" = "$expected" ] || fail "$verify_label binary checksum verification failed"
    [ "$("$verify_dir/ops-linux-x86_64" --version 2>/dev/null)" = "ops $version" ] ||
        fail "$verify_label binary reports an unexpected version"
}

verify_artifacts "$release_dir" 'prepared release'

# Snapshot the verified ignored dist artifacts into the private temporary
# directory, verify the snapshot again, and never read dist for publication.
staged_release=$tmp/release
mkdir "$staged_release" || fail 'could not create private release snapshot'
for name in ops-linux-x86_64 checksums.txt checksums.txt.sig; do
    cp -- "$release_dir/$name" "$staged_release/$name" ||
        fail "could not snapshot verified artifact $name"
done
chmod 0700 "$staged_release"
chmod 0700 "$staged_release/ops-linux-x86_64"
chmod 0600 "$staged_release/checksums.txt" "$staged_release/checksums.txt.sig"
verify_artifacts "$staged_release" 'staged release'

installer=$tmp/install
latest=$tmp/latest
sh script/render-install.sh "$installer" || fail 'could not render production installer'
printf '%s\n' "$version" > "$latest"
[ -z "$(git status --porcelain=v1 --untracked-files=all)" ] ||
    fail 'repository changed during publication preparation'

aws_cmd() {
    aws --profile "$profile" --endpoint-url "$endpoint" "$@"
}

not_found_error() {
    grep -Eq '\(404\)|Not Found|NotFound|NoSuchKey' "$1"
}

object_exists() {
    key=$1
    error=$tmp/aws-head-error
    : > "$error"
    if aws_cmd s3api head-object --bucket "$bucket" --key "$key" \
        >/dev/null 2>"$error"
    then
        return 0
    fi
    if not_found_error "$error"; then
        return 1
    fi
    cat "$error" >&2
    fail "could not determine whether s3://$bucket/$key exists"
}

verify_s3() {
    key=$1
    path=$2
    content_type=$3
    cache_control=$4
    copy=$tmp/s3-copy
    rm -f -- "$copy"
    aws_cmd s3api get-object --bucket "$bucket" --key "$key" "$copy" \
        >/dev/null || fail "could not read back $key from R2"
    cmp -s "$path" "$copy" || fail "R2 content verification failed for $key"

    actual_type=$(aws_cmd s3api head-object --bucket "$bucket" --key "$key" \
        --query ContentType --output text) ||
        fail "could not verify content type for $key"
    [ "$actual_type" = "$content_type" ] || fail "unexpected content type for $key"
    actual_cache=$(aws_cmd s3api head-object --bucket "$bucket" --key "$key" \
        --query CacheControl --output text) ||
        fail "could not verify cache control for $key"
    [ "$actual_cache" = "$cache_control" ] || fail "unexpected cache control for $key"
}

publish_immutable() {
    key=$1
    path=$2
    content_type=$3
    if object_exists "$key"; then
        verify_s3 "$key" "$path" "$content_type" "$immutable_cache"
        printf 'Verified existing immutable object: %s\n' "$key"
        return
    fi

    put_error=$tmp/aws-put-error
    : > "$put_error"
    if aws_cmd s3api put-object \
        --bucket "$bucket" --key "$key" --body "$path" \
        --content-type "$content_type" --cache-control "$immutable_cache" \
        --if-none-match '*' >/dev/null 2>"$put_error"
    then
        verify_s3 "$key" "$path" "$content_type" "$immutable_cache"
        printf 'Published immutable object: %s\n' "$key"
        return
    fi

    # A retry or concurrent writer may have created the key between HEAD and
    # PUT. Accept that result only after exact bytes and metadata verification.
    if object_exists "$key"; then
        verify_s3 "$key" "$path" "$content_type" "$immutable_cache"
        printf 'Verified raced immutable object: %s\n' "$key"
        return
    fi
    cat "$put_error" >&2
    fail "could not publish immutable object $key"
}

put_mutable() {
    key=$1
    path=$2
    content_type=$3
    aws_cmd s3api put-object \
        --bucket "$bucket" --key "$key" --body "$path" \
        --content-type "$content_type" --cache-control "$mutable_cache" \
        >/dev/null || fail "could not publish mutable object $key"
    verify_s3 "$key" "$path" "$content_type" "$mutable_cache"
}

read_version_file() {
    awk '
        NR == 1 && $0 ~ /^[0-9]+\.[0-9]+\.[0-9]+$/ { version=$0; next }
        { invalid=1 }
        END {
            if (NR == 1 && !invalid) print version
            else exit 1
        }
    ' "$1"
}

# Read the authenticated R2 object as the rollback authority and retain the
# exact ETag for the final compare-and-swap update.
r2_latest_state=absent
r2_latest_version=
r2_latest_etag=
r2_latest_body=$tmp/r2-latest
r2_latest_head=$tmp/r2-latest-head
r2_latest_error=$tmp/r2-latest-error
: > "$r2_latest_error"
if aws_cmd s3api head-object --bucket "$bucket" --key releases/latest \
    --query '[ETag,ContentType,CacheControl]' --output text \
    >"$r2_latest_head" 2>"$r2_latest_error"
then
    head_valid=$(awk -F '\t' '
        NR == 1 && NF == 3 && $1 != "" && $1 != "None" {
            print "yes"
            next
        }
        { invalid=1 }
        END { if (NR != 1 || invalid) exit 1 }
    ' "$r2_latest_head") || fail 'authoritative R2 latest metadata is malformed'
    [ "$head_valid" = yes ] || fail 'authoritative R2 latest metadata is malformed'
    r2_latest_etag=$(awk -F '\t' '{ print $1 }' "$r2_latest_head")
    r2_latest_type=$(awk -F '\t' '{ print $2 }' "$r2_latest_head")
    r2_latest_cache=$(awk -F '\t' '{ print $3 }' "$r2_latest_head")
    [ "$r2_latest_type" = 'text/plain; charset=utf-8' ] ||
        fail 'authoritative R2 latest has unexpected content type'
    [ "$r2_latest_cache" = "$mutable_cache" ] ||
        fail 'authoritative R2 latest has unexpected cache control'
    aws_cmd s3api get-object --bucket "$bucket" --key releases/latest \
        --if-match "$r2_latest_etag" "$r2_latest_body" >/dev/null ||
        fail 'authoritative R2 latest changed while it was being inspected'
    r2_latest_version=$(read_version_file "$r2_latest_body") ||
        fail 'authoritative R2 latest version is invalid'
    r2_latest_state=present
elif not_found_error "$r2_latest_error"; then
    : > "$r2_latest_body"
else
    cat "$r2_latest_error" >&2
    fail 'could not inspect authoritative R2 latest release'
fi

# The public URL is not authoritative, but it must agree exactly with R2.
public_latest=$tmp/public-latest
if public_status=$(curl --silent --show-error --location \
    --proto '=https' --proto-redir '=https' --tlsv1.2 \
    "$public_origin/releases/latest" --output "$public_latest" \
    --write-out '%{http_code}')
then
    :
else
    fail 'could not inspect public latest release'
fi
case "$r2_latest_state:$public_status" in
    absent:404) ;;
    present:200)
        cmp -s "$r2_latest_body" "$public_latest" ||
            fail 'public latest is inconsistent with authoritative R2 latest'
        ;;
    *) fail 'public latest is inconsistent with authoritative R2 latest' ;;
esac

if [ "$r2_latest_state" = present ]; then
    comparison=$(awk -v current="$r2_latest_version" -v proposed="$version" '
        function normalized(value) {
            sub(/^0+/, "", value)
            return value == "" ? "0" : value
        }
        BEGIN {
            split(current, old, ".")
            split(proposed, new, ".")
            for (i=1; i<=3; i++) {
                old[i]=normalized(old[i])
                new[i]=normalized(new[i])
                if (length(new[i]) < length(old[i])) { print -1; exit }
                if (length(new[i]) > length(old[i])) { print 1; exit }
                if (("x" new[i]) < ("x" old[i])) { print -1; exit }
                if (("x" new[i]) > ("x" old[i])) { print 1; exit }
            }
            print 0
        }
    ')
    [ "$comparison" -ge 0 ] ||
        fail "refusing to move latest backward from $r2_latest_version to $version"
fi

binary_key=releases/$version/ops-linux-x86_64
checksums_key=releases/$version/checksums.txt
signature_key=releases/$version/checksums.txt.sig

publish_immutable "$binary_key" "$staged_release/ops-linux-x86_64" \
    'application/octet-stream'
publish_immutable "$checksums_key" "$staged_release/checksums.txt" \
    'text/plain; charset=utf-8'
publish_immutable "$signature_key" "$staged_release/checksums.txt.sig" \
    'application/pgp-signature'

verify_public() {
    key=$1
    path=$2
    copy=$tmp/public-copy
    rm -f -- "$copy"
    curl --fail --silent --show-error --location \
        --proto '=https' --proto-redir '=https' --tlsv1.2 \
        "$public_origin/$key" --output "$copy" ||
        fail "could not fetch public object $key"
    cmp -s "$path" "$copy" || fail "public content verification failed for $key"
}

verify_public "$binary_key" "$staged_release/ops-linux-x86_64"
verify_public "$checksums_key" "$staged_release/checksums.txt"
verify_public "$signature_key" "$staged_release/checksums.txt.sig"

put_mutable install "$installer" 'text/plain; charset=utf-8'
verify_public install "$installer"

# Compare-and-swap the authoritative latest object against the exact state
# observed before any mutation. A concurrent change fails closed.
latest_put_error=$tmp/latest-put-error
: > "$latest_put_error"
if [ "$r2_latest_state" = absent ]; then
    latest_condition=--if-none-match
    latest_condition_value='*'
else
    latest_condition=--if-match
    latest_condition_value=$r2_latest_etag
fi
if ! aws_cmd s3api put-object \
    --bucket "$bucket" --key releases/latest --body "$latest" \
    --content-type 'text/plain; charset=utf-8' --cache-control "$mutable_cache" \
    "$latest_condition" "$latest_condition_value" \
    >/dev/null 2>"$latest_put_error"
then
    fail 'conditional latest update failed; authoritative R2 state may have changed'
fi
verify_s3 releases/latest "$latest" 'text/plain; charset=utf-8' "$mutable_cache"
verify_public releases/latest "$latest"

cleanup
trap - 0 HUP INT TERM

printf 'Published\n'
printf '  version          %s\n' "$version"
printf '  release          %s/releases/%s/\n' "$public_origin" "$version"
printf '  installer        %s/install\n' "$public_origin"
