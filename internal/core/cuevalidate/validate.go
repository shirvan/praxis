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
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cueerrors "cuelang.org/go/cue/errors"
)

// compiledSchema holds a schema definition compiled once and reused across
// validations. The definition value is bound to ctx, and CUE contexts are not
// safe for concurrent use, so callers must hold mu while compiling the input in
// ctx and unifying. Different schema files use different contexts, so unrelated
// resource kinds still validate concurrently.
type compiledSchema struct {
	mu  sync.Mutex
	ctx *cue.Context
	def cue.Value
}

// schemaCache memoizes compiled schema definitions by "fullPath|definition".
// Schema files are immutable at runtime (mounted read-only), so entries never
// need invalidation. This replaces a per-call cuecontext.New() + CompileBytes of
// the whole schema file, which dominated event-validation cost on the hot path.
var schemaCache sync.Map // string -> *compiledSchema

// loadSchema returns the cached compiled definition for a schema file, compiling
// and caching it on first use.
func loadSchema(schemaDir, relativePath, definition string) (*compiledSchema, error) {
	fullPath := filepath.Join(schemaDir, relativePath)
	cacheKey := fullPath + "|" + definition
	if cached, ok := schemaCache.Load(cacheKey); ok {
		return cached.(*compiledSchema), nil
	}

	content, err := os.ReadFile(fullPath) //nolint:gosec // fullPath is built from trusted schemaDir + relativePath
	if err != nil {
		return nil, fmt.Errorf("read schema %q: %w", relativePath, err)
	}

	ctx := cuecontext.New()
	schema := ctx.CompileBytes(content, cue.Filename(fullPath))
	if schema.Err() != nil {
		return nil, fmt.Errorf("compile schema %q: %s", relativePath, strings.TrimSpace(cueerrors.Details(schema.Err(), nil)))
	}

	definitionValue := schema.LookupPath(cue.ParsePath(definition))
	if !definitionValue.Exists() {
		return nil, fmt.Errorf("schema %q does not define %s", relativePath, definition)
	}

	entry := &compiledSchema{ctx: ctx, def: definitionValue}
	actual, _ := schemaCache.LoadOrStore(cacheKey, entry)
	return actual.(*compiledSchema), nil
}

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

	// Steps 1–3: Read + compile the schema and look up the definition — cached
	// after the first call for this (file, definition) pair.
	schema, err := loadSchema(schemaDir, relativePath, definition)
	if err != nil {
		return err
	}

	// Step 4: Marshal the Go input to JSON. Done outside the lock since it
	// doesn't touch the CUE context.
	encoded, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal validation input: %w", err)
	}

	// Steps 5–7 touch the schema's CUE context (compile input, unify, validate,
	// decode). CUE contexts are not concurrency-safe, so serialize per schema.
	schema.mu.Lock()
	defer schema.mu.Unlock()

	inputValue := schema.ctx.CompileBytes(encoded)
	if inputValue.Err() != nil {
		return fmt.Errorf("compile validation input: %s", strings.TrimSpace(cueerrors.Details(inputValue.Err(), nil)))
	}

	// Unification merges schema constraints with the input, applying defaults and
	// detecting type/constraint violations.
	unified := schema.def.Unify(inputValue)

	// Validate the result is fully concrete (no open constraints) and final.
	if err := unified.Validate(cue.Final(), cue.Concrete(true)); err != nil {
		return fmt.Errorf("validate against %s in %q: %s", definition, relativePath, strings.TrimSpace(cueerrors.Details(err, nil)))
	}

	// Decode the validated + defaulted CUE value back into Go.
	if out != nil {
		if err := unified.Decode(out); err != nil {
			return fmt.Errorf("decode validated value from %q: %w", relativePath, err)
		}
	}
	return nil
}
