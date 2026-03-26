package cuevalidate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
)

// DecodeFile validates input against a named definition in a CUE file and
// decodes the resulting value, including defaults, into out when provided.
func DecodeFile(schemaDir, relativePath, definition string, input any, out any) error {
	if strings.TrimSpace(schemaDir) == "" {
		return fmt.Errorf("schema directory is required")
	}
	fullPath := filepath.Join(schemaDir, relativePath)
	content, err := os.ReadFile(fullPath) //nolint:gosec // fullPath is built from trusted schemaDir + relativePath
	if err != nil {
		return fmt.Errorf("read schema %q: %w", relativePath, err)
	}

	ctx := cuecontext.New()
	schema := ctx.CompileBytes(content, cue.Filename(fullPath))
	if schema.Err() != nil {
		return fmt.Errorf("compile schema %q: %s", relativePath, strings.TrimSpace(cueerrors.Details(schema.Err(), nil)))
	}

	definitionValue := schema.LookupPath(cue.ParsePath(definition))
	if !definitionValue.Exists() {
		return fmt.Errorf("schema %q does not define %s", relativePath, definition)
	}

	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal validation input: %w", err)
	}
	inputValue := ctx.CompileBytes(encoded)
	if inputValue.Err() != nil {
		return fmt.Errorf("compile validation input: %s", strings.TrimSpace(cueerrors.Details(inputValue.Err(), nil)))
	}

	unified := definitionValue.Unify(inputValue)
	if err := unified.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("validate against %s in %q: %s", definition, relativePath, strings.TrimSpace(cueerrors.Details(err, nil)))
	}
	if out != nil {
		if err := unified.Decode(out); err != nil {
			return fmt.Errorf("decode validated value from %q: %w", relativePath, err)
		}
	}
	return nil
}
