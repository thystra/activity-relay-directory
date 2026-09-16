#!/bin/sh
set -eu

if [ "$#" -ne 1 ]; then
    echo "usage: $0 ARTIFACT_ROOT" >&2
    exit 2
fi

artifact_root="$1"
public="$artifact_root/public"

set -- "$public"/activity-relay-directory_*_*.deb

if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "expected exactly one activity-relay-directory .deb" >&2
    exit 1
fi

deb="$1"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT HUP INT TERM

control="$work/control"
root="$work/root"

mkdir -p "$control" "$root"

dpkg-deb --control "$deb" "$control"
dpkg-deb --extract "$deb" "$root"

for script in postinst prerm postrm; do
    if [ ! -f "$control/$script" ]; then
        echo "missing generated maintainer script: $script" >&2
        exit 1
    fi

    sh -n "$control/$script"
done

python3 - "$control/postrm" <<'PY'
from pathlib import Path
import sys

path = Path(sys.argv[1])
text = path.read_text()

marker = 'if [ "${1:-}" = "purge" ]; then'
if text.count(marker) != 1:
    raise SystemExit(
        "custom purge guard must occur exactly once"
    )

guard = text.index(marker)

root_guard = (
    'if [ "${1:-}" = "purge" ] && '
    '[ -n "${DPKG_ROOT:-}" ]; then'
)

if text.count(root_guard) != 1:
    raise SystemExit(
        "DPKG_ROOT purge guard must occur exactly once"
    )

root_guard_pos = text.index(root_guard)

if root_guard_pos >= guard:
    raise SystemExit(
        "DPKG_ROOT guard must precede destructive purge body"
    )

required = [
    'rm -rf -- "$state"',
    'deluser --system --quiet "$account"',
    'delgroup --system --quiet "$account"',
    'rmdir "$config_dir" 2>/dev/null || true',
]

for token in required:
    if text.count(token) != 1:
        raise SystemExit(
            f"expected exactly one postrm token: {token}"
        )

    if text.index(token) <= guard:
        raise SystemExit(
            f"destructive token occurs outside custom purge block: {token}"
        )

prefix = text[:guard]

for token in (
    'rm -rf -- "$state"',
    'deluser --system --quiet "$account"',
    'delgroup --system --quiet "$account"',
):
    if token in prefix:
        raise SystemExit(
            f"destructive token appears before purge guard: {token}"
        )

if '#DEBHELPER#' in text:
    raise SystemExit(
        "unexpanded #DEBHELPER# remained in built postrm"
    )

print("generated_postrm_purge_boundary=PASS")
PY

if [ ! -f "$control/conffiles" ]; then
    echo "missing conffiles metadata" >&2
    exit 1
fi

conffiles="$(
    sed '/^[[:space:]]*$/d' "$control/conffiles"
)"

if [ "$conffiles" != "/etc/default/activity-relay-directory" ]; then
    echo "unexpected package conffile set:" >&2
    printf '%s\n' "$conffiles" >&2
    exit 1
fi

if [ -e "$root/etc/activity-relay-directory/config.yml" ]; then
    echo "operator-owned config.yml must not be packaged" >&2
    exit 1
fi

echo "package_conffile_boundary=PASS"
echo "operator_config_not_packaged=PASS"
echo "debian_lifecycle_static_validation=PASS"
