#!/bin/bash
# Update only the Go binary. Application updates never reinstall the node.
set -euo pipefail

REPO_RAW=${LWH_DNS_REPO_RAW:-https://raw.githubusercontent.com/m4rg4rit4/Lightweight-Hosting-DNS/main}
RELEASE_BASE=${LWH_DNS_RELEASE_BASE:-https://github.com/m4rg4rit4/Lightweight-Hosting-DNS/releases/download}
BIN=${LWH_DNS_BIN:-/usr/local/bin/lwh-dns-v2}
VERSION_FILE=${LWH_DNS_VERSION_FILE:-/etc/lwh-dns/VERSION}
CHECK=false
case "${1:-}" in --check) CHECK=true;; "") ;; *) echo 'Usage: autoupdate.sh [--check]' >&2; exit 2;; esac

exec 9>"${LWH_DNS_AUTOUPDATE_LOCK:-/run/lwh-dns-autoupdate.lock}"
flock -n 9 || exit 0
local_version=$(timeout 5 "$BIN" --version)
remote_version=$(curl -fsSL --connect-timeout 5 --max-time 30 "$REPO_RAW/VERSION" | tr -d '[:space:]')
for version in "$local_version" "$remote_version"; do
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid binary or repository version; nothing changed.' >&2; exit 1; }
done
echo "DNS binary: $local_version; repository: $remote_version"
if [[ "$local_version" == "$remote_version" ]] || [[ "$(printf '%s\n' "$local_version" "$remote_version" | sort -V | tail -n 1)" == "$local_version" ]]; then
    if [ ! -f "$VERSION_FILE" ] || [ "$(cat "$VERSION_FILE")" != "$local_version" ]; then
        mkdir -p "$(dirname "$VERSION_FILE")"
        printf '%s\n' "$local_version" > "$VERSION_FILE.new"
        chmod 600 "$VERSION_FILE.new"; mv "$VERSION_FILE.new" "$VERSION_FILE"
    fi
    echo 'DNS is current. No install, configuration write or restart required.'
    exit 0
fi
$CHECK && { echo 'A newer binary is available.'; exit 0; }

case "$(uname -m)" in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; *) echo 'Unsupported architecture.' >&2; exit 1;; esac
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
asset="lwh-dns-linux-$arch"
url="$RELEASE_BASE/v$remote_version/$asset"
# A version file alone is not a deployable release. Missing assets leave the
# running service and its recorded version untouched; never call full install.
curl -fsSL --connect-timeout 5 --max-time 120 "$url" -o "$temporary/$asset" || { echo 'Release binary unavailable; service unchanged.' >&2; exit 1; }
curl -fsSL --connect-timeout 5 --max-time 30 "$url.sha256" -o "$temporary/checksum"
expected=$(awk 'NR==1 {print $1}' "$temporary/checksum")
[[ "$expected" =~ ^[a-fA-F0-9]{64}$ ]] || { echo 'Invalid checksum.' >&2; exit 1; }
[[ "$(sha256sum "$temporary/$asset" | cut -d' ' -f1)" == "${expected,,}" ]] || { echo 'Checksum mismatch; service unchanged.' >&2; exit 1; }
chmod 755 "$temporary/$asset"
[[ "$(timeout 5 "$temporary/$asset" --version)" == "$remote_version" ]] || { echo 'Release version mismatch.' >&2; exit 1; }
installer=${LWH_DNS_INSTALLER:-/usr/local/lib/lwh-dns/install.sh}
[ -f "$installer" ] || { echo 'Guarded local installer missing; service unchanged.' >&2; exit 1; }
bash -n "$installer"
# Never download and execute main/install.sh from cron: an old cached installer
# could ignore the update-only contract and provision the whole machine again.
LWH_DNS_VERSION_FILE="$VERSION_FILE" bash "$installer" /update --binary "$temporary/$asset" --target-version "$remote_version"
