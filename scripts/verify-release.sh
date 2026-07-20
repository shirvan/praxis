#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$ROOT"

DIST=dist/alpha
test -f "$DIST/checksums.txt"
(cd "$DIST" && shasum -a 256 -c checksums.txt)

case "$(go env GOOS)/$(go env GOARCH)" in
  darwin/arm64|darwin/amd64|linux/arm64|linux/amd64)
    archive="praxis_$(go env GOOS)_$(go env GOARCH).tar.gz"
    stage=$(mktemp -d)
    trap 'rm -rf "$stage"' EXIT HUP INT TERM
    tar -xzf "$DIST/$archive" -C "$stage"
    version=$($stage/praxis version --output json)
    echo "$version" | grep -q '"version": "alpha"'
    test -f "$stage/LICENSE"
    ;;
esac

compose=$(docker compose -f deploy/quickstart/compose.yaml config)
if echo "$compose" | grep -q 'build:'; then
  echo "quick-start compose file must not build Praxis from source" >&2
  exit 1
fi
count=$(echo "$compose" | grep -c 'ghcr.io/shirvan/praxis-.*:alpha')
test "$count" -eq 6

helm lint charts/praxis >/dev/null
rendered=$(helm template praxis charts/praxis)
count=$(echo "$rendered" | grep -c 'image: ghcr.io/shirvan/praxis-.*:alpha')
test "$count" -eq 6
echo "$rendered" | grep -Fq '\"force\": true'
if echo "$rendered" | grep -q 'registration returned an error'; then
  echo "Helm registration hook must fail when Restate rejects a service" >&2
  exit 1
fi

echo "Praxis alpha release artifacts verified"
