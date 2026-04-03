// version_info.go holds build-time version metadata.
//
// These variables are injected at compile time via Go ldflags:
//
//	go build -ldflags "-X github.com/shirvan/praxis/internal/cli.version=v1.0.0 \
//	    -X github.com/shirvan/praxis/internal/cli.buildDate=2025-01-01" ./cmd/praxis
package cli

// version is the semantic version of the `praxis` binary. Set at build time.
var version = "dev"

// buildDate is the ISO-8601 date the binary was compiled. Set at build time.
var buildDate = "unknown"
