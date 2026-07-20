// version_info.go holds build-time version metadata.
//
// buildDate is injected at compile time. Praxis has one supported release and
// API contract during alpha, so the public version remains "alpha" until that
// contract changes deliberately.
package cli

// version is the one supported public version of the `praxis` binary.
var version = "alpha"

// buildDate is the ISO-8601 date the binary was compiled. Set at build time.
var buildDate = "unknown"
