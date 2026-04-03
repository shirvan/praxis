// Package cuevalidate provides CUE-based schema validation for Praxis resources.
//
// CUE (Configure, Unify, Execute) is used as Praxis' schema language for
// validating resource specifications before they reach AWS drivers. Every
// resource type has a corresponding CUE schema file under the schemas/ directory
// that defines required fields, types, constraints, and defaults.
//
// The validation flow:
//  1. The orchestrator renders a template into concrete resource specs.
//  2. Before dispatching to a driver, each spec is validated against its CUE
//     schema via DecodeFile.
//  3. CUE unification catches type errors, missing required fields, and
//     constraint violations (e.g. "cidrBlock must match pattern").
//  4. If an output struct is provided, CUE defaults are merged in and the
//     validated value is decoded into the Go struct.
//
// This gives Praxis a type-safe, declarative validation layer that catches
// user errors before any AWS API calls are made.
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
// decodes the resulting value, including CUE-applied defaults, into out when
// provided.
//
// Parameters:
//   - schemaDir: base directory containing CUE schema files (from config.SchemaDir).
//   - relativePath: path to the CUE file relative to schemaDir (e.g. "aws/ec2/instance.cue").
//   - definition: the CUE path to the definition to validate against (e.g. "#Spec").
//   - input: the Go value to validate (will be JSON-marshaled then compiled as CUE).
//   - out: optional pointer to receive the validated + defaulted result; pass nil to
//     validate without decoding.
//
// Processing steps:
//  1. Read the CUE schema file from disk.
//  2. Compile the schema into a CUE value.
//  3. Look up the named definition (e.g. #Spec) within the schema.
//  4. JSON-marshal the input and compile it as a CUE value.
//  5. Unify the definition with the input — this merges defaults and checks constraints.
//  6. Validate the unified value is concrete and final (no unresolved references).
//  7. If out != nil, decode the unified value into the Go struct.
//
// Errors from CUE are formatted with cueerrors.Details for human-readable output
// that includes field paths and constraint descriptions.
func DecodeFile(schemaDir, relativePath, definition string, input any, out any) error {
	if strings.TrimSpace(schemaDir) == "" {
		return fmt.Errorf("schema directory is required")
	}

	// Step 1: Read the CUE schema file.
	fullPath := filepath.Join(schemaDir, relativePath)
	content, err := os.ReadFile(fullPath) //nolint:gosec // fullPath is built from trusted schemaDir + relativePath
	if err != nil {
		return fmt.Errorf("read schema %q: %w", relativePath, err)
	}

	// Step 2: Compile the raw CUE source into a CUE value.
	ctx := cuecontext.New()
	schema := ctx.CompileBytes(content, cue.Filename(fullPath))
	if schema.Err() != nil {
		return fmt.Errorf("compile schema %q: %s", relativePath, strings.TrimSpace(cueerrors.Details(schema.Err(), nil)))
	}

	// Step 3: Look up the specific definition (e.g. #Spec, #Input) in the schema.
	definitionValue := schema.LookupPath(cue.ParsePath(definition))
	if !definitionValue.Exists() {
		return fmt.Errorf("schema %q does not define %s", relativePath, definition)
	}

	// Step 4: Marshal the Go input to JSON, then compile as CUE.
	// This round-trip ensures the input is represented in CUE's type system.
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal validation input: %w", err)
	}
	inputValue := ctx.CompileBytes(encoded)
	if inputValue.Err() != nil {
		return fmt.Errorf("compile validation input: %s", strings.TrimSpace(cueerrors.Details(inputValue.Err(), nil)))
	}

	// Step 5: Unify the schema definition with the input value.
	// Unification in CUE is the core operation: it merges constraints from the
	// schema with the actual values from the input, applying defaults and
	// detecting type/constraint violations.
	unified := definitionValue.Unify(inputValue)

	// Step 6: Validate the result is fully concrete (no open constraints remaining)
	// and final (no further unification needed).
	if err := unified.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("validate against %s in %q: %s", definition, relativePath, strings.TrimSpace(cueerrors.Details(err, nil)))
	}

	// Step 7: Decode the validated + defaulted CUE value back into Go.
	if out != nil {
		if err := unified.Decode(out); err != nil {
			return fmt.Errorf("decode validated value from %q: %w", relativePath, err)
		}
	}
	return nil
}
