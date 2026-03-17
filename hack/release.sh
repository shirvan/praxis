#!/usr/bin/env bash
set -euo pipefail

VERSION="${1:?Usage: hack/release.sh VERSION}"
DATE=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
DIST="dist/${VERSION}"
LDFLAGS="-X github.com/praxiscloud/praxis/internal/cli.version=${VERSION} \
         -X github.com/praxiscloud/praxis/internal/cli.buildDate=${DATE}"

mkdir -p "${DIST}"

echo "Building CLI binaries..."

# CLI — macOS arm64, macOS amd64, Linux amd64
for GOOS_GOARCH in darwin/arm64 darwin/amd64 linux/amd64; do
  IFS='/' read -r GOOS GOARCH <<< "${GOOS_GOARCH}"
  OUT="${DIST}/praxis_${GOOS}_${GOARCH}"
  mkdir -p "${OUT}"
  echo "  ${GOOS}/${GOARCH}"
  GOOS=${GOOS} GOARCH=${GOARCH} go build -ldflags "${LDFLAGS}" -o "${OUT}/praxis" ./cmd/praxis
  tar -czf "${DIST}/praxis_${GOOS}_${GOARCH}.tar.gz" -C "${OUT}" praxis
done

echo "Building service binaries (linux/amd64)..."

# Service binaries — Linux amd64 only (for containers)
mkdir -p "${DIST}/linux_amd64"
for SVC in praxis-core praxis-s3 praxis-sg; do
  echo "  ${SVC}"
  GOOS=linux GOARCH=amd64 go build -ldflags "${LDFLAGS}" -o "${DIST}/linux_amd64/${SVC}" "./cmd/${SVC}"
done

echo "Building Docker images..."

# Docker images
docker build -t "praxiscloud/praxis-core:${VERSION}" -f cmd/praxis-core/Dockerfile .
docker build -t "praxiscloud/praxis-s3:${VERSION}" -f cmd/praxis-s3/Dockerfile .
docker build -t "praxiscloud/praxis-sg:${VERSION}" -f cmd/praxis-sg/Dockerfile .

echo "Generating checksums..."

# Checksums
cd "${DIST}" && shasum -a 256 *.tar.gz > checksums.txt

echo ""
echo "Release ${VERSION} built successfully:"
echo "  CLI tarballs:  ${DIST}/praxis_*.tar.gz"
echo "  Services:      ${DIST}/linux_amd64/"
echo "  Docker images: praxiscloud/praxis-{core,s3,sg}:${VERSION}"
echo "  Checksums:     ${DIST}/checksums.txt"
