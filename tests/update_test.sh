#!/bin/bash
# Isolated updater tests. Real system services/configuration are never touched.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
[ "$EUID" -eq 0 ] || { echo 'Run these isolated tests as root on Linux.'; exit 77; }
temporary=$(mktemp -d)
trap 'rm -rf -- "$temporary"' EXIT
mkdir -p "$temporary/mock" "$temporary/private"
export PATH="$temporary/mock:$PATH"
export LWH_DNS_BIN="$temporary/current"
export LWH_DNS_VERSION_FILE="$temporary/private/VERSION"
export LWH_DNS_UPDATE_LOCK="$temporary/update.lock"
export LWH_DNS_AUTOUPDATE_LOCK="$temporary/auto.lock"
export LWH_DNS_INSTALLER="$ROOT/install.sh"
export TEST_LOG="$temporary/commands"
export TEST_STAGED="$temporary/new"
printf '%s\n' 'DNS_NAMESERVERS=ns.example.invalid' 'CUSTOM_SETTING=preserve-me' > "$temporary/private/lwh-dns.env"
cp "$temporary/private/lwh-dns.env" "$temporary/env.before"
make_binary() { printf '#!/bin/bash\n[ "${1:-}" = --version ] || exit 93\necho %s\n' "$2" > "$1"; chmod 755 "$1"; }
make_binary "$LWH_DNS_BIN" 2.2.2
make_binary "$TEST_STAGED" 2.2.3
echo 2.2.2 > "$LWH_DNS_VERSION_FILE"
cat > "$temporary/mock/systemctl" <<'MOCK'
#!/bin/bash
echo "systemctl $*" >> "$TEST_LOG"
if [ "${FAIL_NEW:-0}" = 1 ] && [ "$("$LWH_DNS_BIN" --version)" = 2.2.3 ]; then exit 1; fi
MOCK
printf '#!/bin/bash\nexit 0\n' > "$temporary/mock/sleep"
for command in apt apt-get ufw mariadb hostnamectl; do
    printf '#!/bin/bash\necho forbidden >> "$TEST_LOG"\nexit 99\n' > "$temporary/mock/$command"
done
cat > "$temporary/mock/curl" <<'MOCK'
#!/bin/bash
url=''; output=''
while [ "$#" -gt 0 ]; do
 case "$1" in -o) output=$2;shift 2;; --connect-timeout|--max-time) shift 2;; http*) url=$1;shift;; *) shift;; esac
done
case "$url" in
 */VERSION) body=${REMOTE_VERSION:-2.2.3};;
 *.sha256) if [ "${DOWNLOAD_AVAILABLE:-0}" = 2 ]; then body=$(sha256sum "$TEST_STAGED" | cut -d' ' -f1); else body=$(printf '%064d' 0); fi;;
 *) [ "${DOWNLOAD_AVAILABLE:-0}" != 0 ] || exit 22; cp "$TEST_STAGED" "$output"; exit 0;;
esac
if [ -n "$output" ]; then printf '%s\n' "$body" > "$output"; else printf '%s\n' "$body"; fi
MOCK
chmod 755 "$temporary/mock/"*

# Missing a release must never fall through into the full installer.
if bash "$ROOT/install.sh" /update --target-version 2.2.3; then echo 'Missing binary incorrectly accepted'; exit 1; fi
[ "$("$LWH_DNS_BIN" --version)" = 2.2.2 ]
[ "$(cat "$LWH_DNS_VERSION_FILE")" = 2.2.2 ]

# A failing replacement rolls back the executable and leaves the version marker.
export FAIL_NEW=1
if bash "$ROOT/install.sh" /update --binary "$TEST_STAGED" --target-version 2.2.3; then echo 'Failed service incorrectly accepted'; exit 1; fi
[ "$("$LWH_DNS_BIN" --version)" = 2.2.2 ]
[ "$(cat "$LWH_DNS_VERSION_FILE")" = 2.2.2 ]
unset FAIL_NEW
bash "$ROOT/install.sh" /update --binary "$TEST_STAGED" --target-version 2.2.3
[ "$("$LWH_DNS_BIN" --version)" = 2.2.3 ]
[ "$(cat "$LWH_DNS_VERSION_FILE")" = 2.2.3 ]
cp "$TEST_LOG" "$temporary/log.before"
bash "$ROOT/install.sh" /update --target-version 2.2.3
cmp "$TEST_LOG" "$temporary/log.before"
cmp "$temporary/private/lwh-dns.env" "$temporary/env.before"

# An unchanged repository performs no writes or restarts on repeated checks.
export REMOTE_VERSION=2.2.3
bash "$ROOT/autoupdate.sh"
bash "$ROOT/autoupdate.sh"
cmp "$TEST_LOG" "$temporary/log.before"
export REMOTE_VERSION=2.2.4
bash "$ROOT/autoupdate.sh" --check
if bash "$ROOT/autoupdate.sh"; then echo 'Missing release incorrectly accepted'; exit 1; fi
export DOWNLOAD_AVAILABLE=1
if bash "$ROOT/autoupdate.sh"; then echo 'Bad checksum incorrectly accepted'; exit 1; fi
export REMOTE_VERSION='<html>Error</html>'
if bash "$ROOT/autoupdate.sh"; then echo 'Invalid remote version incorrectly accepted'; exit 1; fi
cmp "$TEST_LOG" "$temporary/log.before"
cmp "$temporary/private/lwh-dns.env" "$temporary/env.before"
! grep -q forbidden "$TEST_LOG"
export REMOTE_VERSION=2.2.4 DOWNLOAD_AVAILABLE=2
make_binary "$TEST_STAGED" 2.2.4
bash "$ROOT/autoupdate.sh"
[ "$("$LWH_DNS_BIN" --version)" = 2.2.4 ]
[ "$(cat "$LWH_DNS_VERSION_FILE")" = 2.2.4 ]
cmp "$temporary/private/lwh-dns.env" "$temporary/env.before"
! grep -q forbidden "$TEST_LOG"
echo 'DNS updater tests passed: no provisioning, version convergence, rollback and preserved environment.'
