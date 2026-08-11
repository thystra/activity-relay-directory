#!/usr/bin/env bash
set -Eeuo pipefail
umask 022

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="${1:?usage: build-debian-artifacts.sh OUTPUT_DIR}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"

for c in go dpkg-buildpackage dpkg-deb dpkg-parsechangelog lintian python3 sha256sum tar; do
    command -v "$c" >/dev/null || {
        echo "missing release-build command: $c" >&2
        exit 1
    }
done

DEB_VERSION="$(dpkg-parsechangelog -l"$ROOT/debian/changelog" -SVersion)"
PACKAGE="$(dpkg-parsechangelog -l"$ROOT/debian/changelog" -SSource)"
ARCH="$(dpkg --print-architecture)"
DEB_UPSTREAM="${DEB_VERSION%-*}"
APP_VERSION="${DEB_UPSTREAM//\~/-}"
PUBLIC_DEB_VERSION="${DEB_VERSION//\~/-}"
SOURCE_DATE_EPOCH="$(
    date -u -d "$(dpkg-parsechangelog -l"$ROOT/debian/changelog" -SDate)" +%s
)"
export SOURCE_DATE_EPOCH
export GOTOOLCHAIN=local

case "$(go env GOVERSION)" in
    go1.26.0|go1.26.5) ;;
    *)
        echo "release artifacts require exact Go 1.26.0 or Go 1.26.5; got $(go env GOVERSION)" >&2
        exit 1
        ;;
esac

SOURCE_IDENTITY="${RELEASE_COMMIT:-uncommitted-validation}:${RELEASE_TREE:-unknown-tree}"
DPKG_DEB_VERSION="$(dpkg-deb --version)"
DPKG_DEB_VERSION="${DPKG_DEB_VERSION%%$'\n'*}"

TMP="$(mktemp -d)"
cleanup() { rm -rf "$TMP"; }
trap cleanup EXIT

mkdir -p "$TMP/src" "$OUT/public" "$OUT/evidence"
tar -C "$ROOT" \
    --exclude=.git \
    --exclude=.project-local \
    --exclude='./*.deb' \
    --exclude='./*.changes' \
    --exclude='./*.buildinfo' \
    -cf - . | tar -C "$TMP/src" -xf -

(
    cd "$TMP/src"
    dpkg-buildpackage -us -uc -b -d
)

DEB="$TMP/${PACKAGE}_${DEB_VERSION}_${ARCH}.deb"
BUILDINFO="$(find "$TMP" -maxdepth 1 -type f -name "${PACKAGE}_*.buildinfo" -print -quit)"
CHANGES="$(find "$TMP" -maxdepth 1 -type f -name "${PACKAGE}_*.changes" -print -quit)"
[[ -f "$DEB" && -f "$BUILDINFO" && -f "$CHANGES" ]]

PUBLIC_DEB="$OUT/public/${PACKAGE}_${PUBLIC_DEB_VERSION}_${ARCH}.deb"
cp -a "$DEB" "$PUBLIC_DEB"
cp -a "$BUILDINFO" "$OUT/evidence/"
cp -a "$CHANGES" "$OUT/evidence/"

EXTRACT="$TMP/extract"
CONTROL="$OUT/evidence/control"
mkdir -p "$EXTRACT" "$CONTROL"
dpkg-deb -x "$PUBLIC_DEB" "$EXTRACT"
dpkg-deb --control "$PUBLIC_DEB" "$CONTROL"

BIN="$EXTRACT/usr/bin/activity-relay-directory"
[[ -x "$BIN" ]]
[[ "$("$BIN" --version)" == "$APP_VERSION" ]]

PUBLIC_BIN="$OUT/public/${PACKAGE}_${APP_VERSION}_linux_${ARCH}"
cp -a "$BIN" "$PUBLIC_BIN"

(
    cd "$TMP/src"
    python3 scripts/release/generate-cyclonedx.py \
        --version "$APP_VERSION" \
        --debian-version "$DEB_VERSION" \
        --binary "$BIN" \
        --source-date-epoch "$SOURCE_DATE_EPOCH" \
        --source-identity "$SOURCE_IDENTITY" \
        --output "$OUT/public/${PACKAGE}_${APP_VERSION}_${ARCH}.cdx.json"
)

dpkg-deb --info "$PUBLIC_DEB" >"$OUT/evidence/dpkg-deb-info.txt"
dpkg-deb --contents "$PUBLIC_DEB" >"$OUT/evidence/dpkg-deb-contents.txt"

set +e
lintian --show-overrides "$PUBLIC_DEB" >"$OUT/evidence/lintian.txt" 2>&1
LINTIAN_RC=$?
set -e
if grep -Eq '^[EW]:' "$OUT/evidence/lintian.txt"; then
    cat "$OUT/evidence/lintian.txt" >&2
    echo "lintian emitted an unoverridden error/warning finding" >&2
    exit 1
fi
if (( LINTIAN_RC != 0 )); then
    cat "$OUT/evidence/lintian.txt" >&2
    echo "lintian runtime failure or unexpected nonzero status: rc=$LINTIAN_RC" >&2
    exit 1
fi
echo "lintian_exit_code=$LINTIAN_RC" >>"$OUT/evidence/lintian.txt"

{
    echo "package=$PACKAGE"
    echo "application_version=$APP_VERSION"
    echo "debian_version=$DEB_VERSION"
    echo "architecture=$ARCH"
    echo "source_date_epoch=$SOURCE_DATE_EPOCH"
    echo "source_identity=$SOURCE_IDENTITY"
    echo "go_version=$(go version)"
    echo "go_env_GOVERSION=$(go env GOVERSION)"
    echo "dpkg_deb_version=$DPKG_DEB_VERSION"
    echo "deb_sha256=$(sha256sum "$PUBLIC_DEB" | awk '{print $1}')"
    echo "binary_sha256=$(sha256sum "$PUBLIC_BIN" | awk '{print $1}')"
} >"$OUT/public/BUILD-METADATA.txt"

(
    cd "$OUT/public"
    rm -f SHA256SUMS
    find . -maxdepth 1 -type f ! -name SHA256SUMS -printf '%P\0' |
        sort -z |
        xargs -0 sha256sum >SHA256SUMS
    sha256sum -c SHA256SUMS
)

echo "public_artifacts=$OUT/public"
echo "build_evidence=$OUT/evidence"
