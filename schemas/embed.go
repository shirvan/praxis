// Package schemas embeds the CUE schema bundle so binaries can ship with the
// full validation schema set — no PRAXIS_SCHEMA_DIR required.
//
// The CLI uses this for `praxis list schemas` and `praxis get schema <Kind>`,
// giving users and AI agents offline access to every resource spec shape.
// Server-side components continue to read schemas from PRAXIS_SCHEMA_DIR so
// operators can hot-swap schema bundles without rebuilding images.
package schemas

import "embed"

// FS contains the aws/, data/, events/, and notifications/ schema trees.
//
//go:embed aws data events notifications
var FS embed.FS
