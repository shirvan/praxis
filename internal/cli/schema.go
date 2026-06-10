// schema.go implements schema discovery commands backed by the embedded CUE
// schema bundle. These commands work fully offline — no running stack needed:
//
//	praxis list schemas          List every resource kind with its schema file
//	praxis get schema <Kind>     Print the CUE schema for one kind
//
// The schemas are the source of truth for what a template's spec may contain,
// so these commands are the fastest way for a user (or an AI agent authoring
// a template) to discover valid fields without reading the repository.
package cli

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shirvan/praxis/schemas"
)

// schemaInfo describes one resource kind and where its schema lives.
type schemaInfo struct {
	Kind string `json:"kind"`
	File string `json:"file"`
}

// kindLineRe matches the `kind: "<Kind>"` field inside a schema definition.
// Only definitions with a literal kind field are resource schemas; helper
// definitions (e.g. #Rule, #Target) have no kind and are excluded.
var kindLineRe = regexp.MustCompile(`(?m)^\s*kind:\s*"([A-Za-z0-9]+)"`)

// listResourceSchemas walks the embedded aws/ schema tree and returns every
// resource kind, sorted alphabetically.
func listResourceSchemas() ([]schemaInfo, error) {
	var out []schemaInfo
	err := fs.WalkDir(schemas.FS, "aws", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".cue") {
			return nil
		}
		content, readErr := fs.ReadFile(schemas.FS, path)
		if readErr != nil {
			return readErr
		}
		for _, match := range kindLineRe.FindAllStringSubmatch(string(content), -1) {
			out = append(out, schemaInfo{Kind: match[1], File: "schemas/" + path})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out, nil
}

// findResourceSchema returns the schema source for a kind (case-insensitive).
// The full file is returned, not just the definition block, because helper
// definitions in the same file are needed to understand the spec shape.
func findResourceSchema(kind string) (schemaInfo, string, error) {
	infos, err := listResourceSchemas()
	if err != nil {
		return schemaInfo{}, "", err
	}
	for _, info := range infos {
		if strings.EqualFold(info.Kind, kind) {
			content, readErr := fs.ReadFile(schemas.FS, strings.TrimPrefix(info.File, "schemas/"))
			if readErr != nil {
				return schemaInfo{}, "", readErr
			}
			return info, string(content), nil
		}
	}
	known := make([]string, 0, len(infos))
	for _, info := range infos {
		known = append(known, info.Kind)
	}
	return schemaInfo{}, "", fmt.Errorf("unknown kind %q — known kinds: %s", kind, strings.Join(known, ", "))
}

// newGetSchemaCmd builds `praxis get schema <Kind>`.
func newGetSchemaCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "schema <Kind>",
		Short: "Print the CUE schema for a resource kind (offline)",
		Long: `Schema prints the embedded CUE schema that templates are validated against.

Works fully offline — no running stack required. Use 'praxis list schemas'
to discover available kinds.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return getSchema(flags, normalizeKind(args[0]))
		},
	}
}

// listSchemas renders all resource kinds for `praxis list schemas`.
func listSchemas(flags *rootFlags) error {
	infos, err := listResourceSchemas()
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(infos)
	}
	rows := make([][]string, 0, len(infos))
	for _, info := range infos {
		rows = append(rows, []string{info.Kind, info.File})
	}
	printTable(flags.renderer(), []string{"KIND", "SCHEMA"}, rows)
	return nil
}

// getSchema renders the CUE schema for one kind for `praxis get schema <Kind>`.
func getSchema(flags *rootFlags, kind string) error {
	info, source, err := findResourceSchema(kind)
	if err != nil {
		return err
	}
	if flags.outputFormat() == OutputJSON {
		return printJSON(map[string]string{
			"kind":   info.Kind,
			"file":   info.File,
			"source": source,
		})
	}
	_, _ = fmt.Fprintln(flags.renderer().out, source)
	return nil
}
