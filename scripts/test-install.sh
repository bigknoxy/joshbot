#!/usr/bin/env bash
#
# Exercises install.sh against the real GitHub release.
#
# Not run in CI: it downloads published artifacts, so it needs the network and
# it would fail on a release that has not been published yet. Run it by hand
# before tagging, and after any change to install.sh.
#
# Usage: ./scripts/test-install.sh [version]

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALLER="${SCRIPT_DIR}/install.sh"
VERSION="${1:-}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0

check() {
    local name="$1" got="$2" want="$3"
    if [ "$got" = "$want" ]; then
        printf '  PASS  %s\n' "$name"
        pass=$((pass + 1))
    else
        printf '  FAIL  %s (got %q, want %q)\n' "$name" "$got" "$want"
        fail=$((fail + 1))
    fi
}

run() {
    if [ -n "$VERSION" ]; then
        bash "$INSTALLER" --version "$VERSION" "$@" >/dev/null 2>&1
    else
        bash "$INSTALLER" "$@" >/dev/null 2>&1
    fi
}

echo "Testing $INSTALLER"

# The bug this suite exists for: --bin-dir naming a directory that does not
# exist reported a successful download and then died on a bare mv error.
run --bin-dir "$WORK/new/nested/bin"
check "creates a nested --bin-dir" "$?" "0"
check "  binary is installed and executable" \
    "$(test -x "$WORK/new/nested/bin/joshbot" && echo yes)" "yes"
check "  binary runs" \
    "$("$WORK/new/nested/bin/joshbot" --version >/dev/null 2>&1 && echo yes)" "yes"

# Argument handling must never fail mutely.
run --bin-dir
check "--bin-dir with no value fails" "$?" "1"
run --version
check "--version with no value fails" "$?" "1"
run --not-a-flag
check "unknown flag fails" "$?" "1"
bash "$INSTALLER" --help >/dev/null 2>&1
check "--help exits clean" "$?" "0"

# Overwrite protection.
run --bin-dir "$WORK/new/nested/bin"
check "reinstalling without --force refuses" "$?" "1"
run --bin-dir "$WORK/new/nested/bin" --force
check "reinstalling with --force succeeds" "$?" "0"

# Permission and availability failures.
mkdir -p "$WORK/readonly" && chmod 500 "$WORK/readonly"
run --bin-dir "$WORK/readonly"
check "unwritable install dir fails" "$?" "1"
chmod 700 "$WORK/readonly"

bash "$INSTALLER" --bin-dir "$WORK/absent" --version v99.99.99 >/dev/null 2>&1
check "a release with no build fails" "$?" "1"

# Verification must fail closed: unreachable checksums install nothing.
sed "s#https://github.com/\${REPO}/releases/download/\${version}/checksums.txt#file:///nonexistent/checksums.txt#" \
    "$INSTALLER" > "$WORK/nochecksum.sh"
bash "$WORK/nochecksum.sh" --bin-dir "$WORK/unverified" >/dev/null 2>&1
check "unfetchable checksums refuse to install" "$?" "1"
check "  and install nothing" \
    "$(ls "$WORK/unverified" 2>/dev/null | wc -l | tr -d ' ')" "0"
JOSHBOT_SKIP_CHECKSUM=1 bash "$WORK/nochecksum.sh" --bin-dir "$WORK/override" >/dev/null 2>&1
check "JOSHBOT_SKIP_CHECKSUM=1 overrides deliberately" "$?" "0"

# A wrong checksum must abort rather than install.
printf '%064d  joshbot_v0.0.0_any\n' 0 > "$WORK/wrong.txt"
sed "s#https://github.com/\${REPO}/releases/download/\${version}/checksums.txt#file://$WORK/wrong.txt#" \
    "$INSTALLER" > "$WORK/mismatch.sh"
bash "$WORK/mismatch.sh" --bin-dir "$WORK/tampered" >/dev/null 2>&1
check "a checksum mismatch refuses to install" "$?" "1"
check "  and installs nothing" \
    "$(ls "$WORK/tampered" 2>/dev/null | wc -l | tr -d ' ')" "0"

echo
echo "  $pass passed, $fail failed"
[ "$fail" -eq 0 ]
