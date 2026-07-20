#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$ROOT"

VERSION=alpha
DIST="dist/$VERSION"
BUILD_DATE=$(git show -s --format=%cI HEAD 2>/dev/null || date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS="-s -w -X github.com/shirvan/praxis/internal/cli.version=$VERSION -X github.com/shirvan/praxis/internal/cli.buildDate=$BUILD_DATE"

rm -rf "$DIST"
mkdir -p "$DIST"

build_cli() {
  goos=$1
  goarch=$2
  extension=$3
  archive=$4
  stage="$DIST/praxis_${goos}_${goarch}"

  mkdir -p "$stage"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$stage/praxis$extension" ./cmd/praxis
  cp LICENSE "$stage/LICENSE"

  if [ "$extension" = ".exe" ]; then
    (cd "$stage" && zip -q "../$archive" "praxis$extension" LICENSE)
  else
    tar -czf "$DIST/$archive" -C "$stage" praxis LICENSE
  fi
  rm -rf "$stage"
}

echo "Building Praxis alpha CLI archives"
build_cli darwin arm64 "" praxis_darwin_arm64.tar.gz
build_cli darwin amd64 "" praxis_darwin_amd64.tar.gz
build_cli linux arm64 "" praxis_linux_arm64.tar.gz
build_cli linux amd64 "" praxis_linux_amd64.tar.gz
build_cli windows amd64 .exe praxis_windows_amd64.zip

echo "Building image-based quick-start bundle"
stage="$DIST/praxis-alpha-quickstart"
mkdir -p "$stage"
cp deploy/quickstart/compose.yaml "$stage/compose.yaml"
cp deploy/quickstart/praxis-up deploy/quickstart/praxis-down "$stage/"
cp deploy/quickstart/bucket.cue deploy/quickstart/README.md LICENSE "$stage/"
chmod +x "$stage/praxis-up" "$stage/praxis-down"
tar -czf "$DIST/praxis-alpha-quickstart.tar.gz" -C "$DIST" praxis-alpha-quickstart
rm -rf "$stage"

echo "Packaging Helm chart"
helm package charts/praxis \
  --version 0.0.0-alpha \
  --app-version alpha \
  --destination "$DIST" >/dev/null
mv "$DIST/praxis-0.0.0-alpha.tgz" "$DIST/praxis-alpha-chart.tgz"

echo "Generating SHA-256 checksums"
(cd "$DIST" && shasum -a 256 \
  praxis_darwin_arm64.tar.gz \
  praxis_darwin_amd64.tar.gz \
  praxis_linux_arm64.tar.gz \
  praxis_linux_amd64.tar.gz \
  praxis_windows_amd64.zip \
  praxis-alpha-quickstart.tar.gz \
  praxis-alpha-chart.tgz > checksums.txt)

echo "Praxis alpha artifacts are in $DIST"
