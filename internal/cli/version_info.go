package cli

// version is set at build time via ldflags:
//
//	go build -ldflags "-X github.com/shirvan/praxis/internal/cli.version=v1.0.0" ./cmd/praxis
var version = "dev"

// buildDate is set at build time via ldflags.
var buildDate = "unknown"
