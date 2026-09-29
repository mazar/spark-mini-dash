#!/usr/bin/env bash
# Build a self-contained .deb installer (Ubuntu) for one component.
#
#   scripts/build-deb.sh <arch> <pkg>
#     arch: arm64 | amd64
#     pkg:  spark-agent | spark-dash
#
# Output: dist/deb/<pkg>_<version>_<arch>.deb
# Installing it ("sudo apt install ./file.deb") drops the binary under
# /usr/bin, registers the systemd unit, and enables + starts the service.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null | sed -e 's/^v//' -e 's/-/+/g' || true)}
VERSION=${VERSION:-0.0.0}
# Debian versions should start with a digit (a bare git hash may not).
case "$VERSION" in [0-9]*) ;; *) VERSION="0.0.0+$VERSION" ;; esac

ARCH=${1:?usage: build-deb.sh <arm64|amd64> <spark-agent|spark-dash>}
PKG=${2:?usage: build-deb.sh <arm64|amd64> <spark-agent|spark-dash>}

case "$ARCH" in arm64|amd64) ;; *) echo "unsupported arch: $ARCH" >&2; exit 1 ;; esac
case "$PKG" in spark-agent|spark-dash) ;; *) echo "unknown package: $PKG" >&2; exit 1 ;; esac

WORK="dist/deb-staging/$PKG-$ARCH"
rm -rf "$WORK"
mkdir -p "$WORK/DEBIAN" "$WORK/usr/bin" "$WORK/lib/systemd/system"

LDFLAGS="-s -w -X spark-mini-dash/internal/version.Version=$VERSION"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath -ldflags "$LDFLAGS" \
    -o "$WORK/usr/bin/$PKG" "./cmd/$PKG"

sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$ARCH/" \
    "packaging/deb/control.$PKG.in" > "$WORK/DEBIAN/control"

for s in postinst prerm postrm; do
    sed "s/@UNIT@/$PKG.service/g" "packaging/deb/$s.in" > "$WORK/DEBIAN/$s"
    chmod 0755 "$WORK/DEBIAN/$s"
done

install -m 0644 "packaging/deb/$PKG.service" \
    "$WORK/lib/systemd/system/$PKG.service"

# spark-dash ships a default config (local agent) as a conffile, so dpkg
# preserves operator edits across upgrades.
if [ "$PKG" = spark-dash ]; then
    mkdir -p "$WORK/etc/spark-mini-dash"
    install -m 0644 packaging/deb/spark-dash.config.json \
        "$WORK/etc/spark-mini-dash/config.json"
    printf '/etc/spark-mini-dash/config.json\n' > "$WORK/DEBIAN/conffiles"
fi

mkdir -p dist/deb
dpkg-deb --build --root-owner-group "$WORK" \
    "dist/deb/${PKG}_${VERSION}_${ARCH}.deb"
echo "built dist/deb/${PKG}_${VERSION}_${ARCH}.deb"
