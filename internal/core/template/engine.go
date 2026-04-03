package template

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/load"
)

// PolicySource pairs a policy's CUE source with its name for diagnostics.
// During evaluation, each policy is compiled and unified with the template
// value; errors introduced by the unification are attributed to the policy
// by name so the user knows which constraint failed.
type PolicySource struct {
	Name   string
	Source []byte
}

// compiledPolicy is a CUE value that has been compiled from a PolicySource.
// Kept internal — the Engine pre-compiles all policies once, then unifies
// them with the template value for constraint checking.
type compiledPolicy struct {
	name  string
	value cue.Value
}

// Engine is the CUE evaluation pipeline for Praxis templates.
//
// It is responsible for:
//  1. Loading CUE template source (from file path or raw bytes).
//  2. Injecting template variables into the CUE value tree.
//  3. Loading provider schemas from schemaDir and unifying them with resources
//     to enforce field types and constraints.
//  4. Optionally applying policy CUE constraints and attributing violations.
//  5. Extracting data source specifications and per-resource JSON specs.
//
// Thread safety: Engine is safe for concurrent use once constructed because
// it only reads its schemaDir and pre-compiled lifecycleExt.
type Engine struct {
	// schemaDir is the filesystem path to the directory containing CUE schema
	// definitions for all supported resource kinds (e.g. #S3Bucket, #EC2Instance).
	schemaDir string
	ctx       *cue.Context

	// lifecycleExt is a pre-compiled CUE value that permits an optional
	// lifecycle block on every resource schema. It is unified with each
	// schema definition before resource validation so that templates can
	// declare lifecycle rules without modifying individual schema files.
	lifecycleExt cue.Value
}

// lifecycleCUE defines the optional lifecycle block accepted on all resource types.
// This CUE fragment is compiled once at Engine construction and unified with
// each resource during validation so that lifecycle directives (preventDestroy,
// ignoreChanges) are always permitted regardless of the provider schema.
const lifecycleCUE = `{
	lifecycle?: {
		preventDestroy?: bool
		ignoreChanges?: [...string]
	}
}`

// NewEngine creates an engine that loads provider schemas from schemaDir.
// The CUE context and lifecycle extension are compiled once and reused for
// all subsequent evaluations.
func NewEngine(schemaDir string) *Engine {
	ctx := cuecontext.New()
	return &Engine{
		schemaDir:    schemaDir,
		ctx:          ctx,
		lifecycleExt: ctx.CompileString(lifecycleCUE),
	}
}

// Evaluate loads the CUE template at templatePath, unifies it against provider
// schemas, validates constraints, and returns per-resource raw JSON specs keyed
// by resource name. This is the file-based entry point used by the CLI.
func (e *Engine) Evaluate(templatePath string, vars map[string]any) (map[string]json.RawMessage, error) {
	templateDir := filepath.Dir(templatePath)
	cfg := &load.Config{
		Dir:     templateDir,
		Overlay: map[string]load.Source{},
	}
	result, err := e.evaluateWithLoadConfig([]string{filepath.Base(templatePath)}, cfg, templatePath, vars, nil)
	if err != nil {
		return nil, err
	}
	return result.Resources, nil
}

// EvaluateBytes evaluates a CUE template from raw bytes instead of a file path.
//
// The command service uses this entry point because templates arrive as request
// payloads over Restate rather than as files on local disk. A virtual overlay
// file is created so that the CUE loader can process the source without
// touching the filesystem.
func (e *Engine) EvaluateBytes(content []byte, vars map[string]any) (map[string]json.RawMessage, error) {
	result, err := e.EvaluateBytesWithPolicies(content, nil, vars)
	if err != nil {
		return nil, err
	}
	return result.Resources, nil
}

// EvaluateBytesWithPolicies evaluates raw template bytes and applies policies
// before resource validation and JSON extraction.
//
// Policy evaluation strategy:
//  1. Evaluate the template without policies to get a "baseline" error set.
//  2. Unify each policy individually with the template to identify which new
//     errors each policy introduces.
//  3. Unify all policies together with the template for the final evaluation.
//  4. Any error that was not in the baseline is classified as a PolicyViolation
//     and annotated with the responsible policy name(s).
func (e *Engine) EvaluateBytesWithPolicies(content []byte, policies []PolicySource, vars map[string]any) (*EvaluationResult, error) {
	virtualDir := os.TempDir()
	if trimmed := strings.TrimSpace(e.schemaDir); trimmed != "" {
		virtualDir = filepath.Join(filepath.Dir(trimmed), ".praxis-inline")
	}

	virtualPath := filepath.Join(virtualDir, "template.cue")
	cfg := &load.Config{
		Dir: virtualDir,
		Overlay: map[string]load.Source{
			virtualPath: load.FromBytes(content),
		},
	}
	return e.evaluateWithLoadConfig([]string{filepath.Base(virtualPath)}, cfg, virtualPath, vars, policies)
}

// evaluateWithLoadConfig is the shared evaluation core used by all public
// entry points. It handles the full pipeline: CUE loading → variable injection
// → data source extraction → schema unification → policy enforcement → JSON export.
func (e *Engine) evaluateWithLoadConfig(patterns []string, cfg *load.Config, templatePath string, vars map[string]any, policies []PolicySource) (*EvaluationResult, error) {
	instances := load.Instances(patterns, cfg)
	if len(instances) == 0 {
		return nil, TemplateErrors{{
			Kind:    ErrCUELoad,
			Path:    templatePath,
			Message: "no CUE instances found",
			Detail:  "Ensure the file path is correct and contains valid CUE syntax.",
		}}
	}

	inst := instances[0]
	if inst.Err != nil {
		return nil, e.convertLoadErrors(inst.Err, templatePath)
	}

	val := e.ctx.BuildInstance(inst)
	if val.Err() != nil {
		return nil, e.convertLoadErrors(val.Err(), templatePath)
	}

	// Load schemas for unification. Provider schemas define closed CUE
	// definitions (#S3Bucket, #EC2Instance, etc.) that constrain valid field
	// names and types for each resource kind.
	schemaVal, schemaErr := e.loadSchemas()
	if schemaErr != nil {
		return nil, schemaErr
	}

	// Inject template-level variables if provided.
	if len(vars) > 0 {
		varsJSON, jerr := json.Marshal(vars)
		if jerr != nil {
			return nil, TemplateErrors{{
				Kind:    ErrCUELoad,
				Path:    "variables",
				Message: fmt.Sprintf("failed to marshal variables: %v", jerr),
			}}
		}
		varsVal := e.ctx.CompileBytes(varsJSON)
		if varsVal.Err() != nil {
			return nil, TemplateErrors{{
				Kind:    ErrCUELoad,
				Path:    "variables",
				Message: fmt.Sprintf("invalid variables: %v", varsVal.Err()),
			}}
		}
		val = val.FillPath(cue.ParsePath("variables"), varsVal)
	}

	baselineDataSources, baselineDataErrs := e.evaluateDataSources(val)
	baselineResults, baselineErrs := e.evaluateResources(val, schemaVal)
	if len(policies) == 0 {
		combined := append(make(TemplateErrors, 0, len(baselineErrs)+len(baselineDataErrs)), baselineErrs...)
		combined = append(combined, baselineDataErrs...)
		if len(combined) > 0 {
			return nil, combined
		}
		return &EvaluationResult{Resources: baselineResults, DataSources: baselineDataSources}, nil
	}

	baselineSet := make(map[string]struct{}, len(baselineErrs)+len(baselineDataErrs))
	for _, item := range baselineErrs {
		baselineSet[templateErrorSignature(item)] = struct{}{}
	}
	for _, item := range baselineDataErrs {
		baselineSet[templateErrorSignature(item)] = struct{}{}
	}

	var compiledPolicies []compiledPolicy
	var policyErrs TemplateErrors
	policyMatches := make(map[string][]string)
	for _, policy := range policies {
		policyVal := e.ctx.CompileBytes(policy.Source)
		if policyVal.Err() != nil {
			policyErrs = append(policyErrs, TemplateError{
				Kind:       ErrPolicyViolation,
				Path:       "policy:" + policy.Name,
				Message:    fmt.Sprintf("policy %q contains invalid CUE: %v", policy.Name, policyVal.Err()),
				PolicyName: policy.Name,
				Cause:      policyVal.Err(),
			})
			continue
		}

		compiledPolicies = append(compiledPolicies, compiledPolicy{name: policy.Name, value: policyVal})
		_, perPolicyErrs := e.evaluateResources(val.Unify(policyVal), schemaVal)
		for _, item := range perPolicyErrs {
			signature := templateErrorSignature(item)
			if _, exists := baselineSet[signature]; exists {
				continue
			}
			policyMatches[signature] = appendUniqueString(policyMatches[signature], policy.Name)
		}
	}

	if len(compiledPolicies) == 0 {
		combined := append(make(TemplateErrors, 0, len(baselineErrs)+len(baselineDataErrs)+len(policyErrs)), baselineErrs...)
		combined = append(combined, baselineDataErrs...)
		combined = append(combined, policyErrs...)
		if len(combined) > 0 {
			return nil, combined
		}
		return &EvaluationResult{Resources: baselineResults, DataSources: baselineDataSources}, nil
	}

	policyUnified := val
	for _, policy := range compiledPolicies {
		policyUnified = policyUnified.Unify(policy.value)
	}

	dataSources, finalDataErrs := e.evaluateDataSources(policyUnified)
	results, finalErrs := e.evaluateResources(policyUnified, schemaVal)
	if len(finalErrs) == 0 && len(finalDataErrs) == 0 && len(policyErrs) == 0 {
		return &EvaluationResult{Resources: results, DataSources: dataSources}, nil
	}

	combined := make(TemplateErrors, 0, len(finalErrs)+len(finalDataErrs)+len(policyErrs))
	for _, item := range finalErrs {
		signature := templateErrorSignature(item)
		if _, exists := baselineSet[signature]; exists {
			combined = append(combined, item)
			continue
		}
		item.Kind = ErrPolicyViolation
		if names := policyMatches[signature]; len(names) > 0 {
			item.PolicyName = strings.Join(names, ",")
		}
		combined = append(combined, item)
	}
	combined = append(combined, finalDataErrs...)
	combined = append(combined, policyErrs...)
	return nil, combined
}

// evaluateDataSources extracts data source declarations from the template's
// top-level "data" block. Each entry is validated to have a kind and at least
// one filter field. Returns nil if the template has no data block.
func (e *Engine) evaluateDataSources(val cue.Value) (map[string]DataSourceSpec, TemplateErrors) {
	dataVal := val.LookupPath(cue.ParsePath("data"))
	if !dataVal.Exists() {
		return nil, nil
	}

	iter, err := dataVal.Fields(cue.Concrete(false))
	if err != nil {
		return nil, TemplateErrors{{
			Kind:    ErrCUELoad,
			Path:    "data",
			Message: fmt.Sprintf("cannot iterate data sources: %v", err),
		}}
	}

	results := make(map[string]DataSourceSpec)
	var errs TemplateErrors
	for iter.Next() {
		name := iter.Selector().String()
		if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
			name = name[1 : len(name)-1]
		}

		entry := iter.Value()
		jsonBytes, err := entry.MarshalJSON()
		if err != nil {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    "data." + name,
				Message: fmt.Sprintf("failed to marshal data source: %v", err),
			})
			continue
		}

		var spec DataSourceSpec
		if err := json.Unmarshal(jsonBytes, &spec); err != nil {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    "data." + name,
				Message: fmt.Sprintf("invalid data source spec: %v", err),
			})
			continue
		}

		if err := validateDataSourceSpec(spec); err != nil {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    "data." + name,
				Message: err.Error(),
			})
			continue
		}

		results[name] = spec
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return results, nil
}

func validateDataSourceSpec(spec DataSourceSpec) error {
	if strings.TrimSpace(spec.Kind) == "" {
		return fmt.Errorf("data source is missing 'kind' field")
	}
	if strings.TrimSpace(spec.Filter.ID) == "" && strings.TrimSpace(spec.Filter.Name) == "" && len(spec.Filter.Tag) == 0 {
		return fmt.Errorf("filter must specify at least one of: id, name, tag")
	}
	return nil
}

// evaluateResources iterates the template's "resources" block, unifies each
// resource with its provider schema, validates concreteness, and exports JSON.
// Lifecycle blocks are stripped before schema validation (since they are Praxis-
// level, not schema-level) and spliced back into the final JSON output.
func (e *Engine) evaluateResources(val cue.Value, schemaVal *cue.Value) (map[string]json.RawMessage, TemplateErrors) {
	resourcesVal := val.LookupPath(cue.ParsePath("resources"))
	if !resourcesVal.Exists() {
		return nil, TemplateErrors{{
			Kind:    ErrCUEValidation,
			Path:    "resources",
			Message: "template must contain a top-level 'resources' field",
			Detail:  "Add a 'resources: { ... }' block to your template.",
		}}
	}

	var errs TemplateErrors
	results := make(map[string]json.RawMessage)

	iter, iterErr := resourcesVal.Fields()
	if iterErr != nil {
		return nil, TemplateErrors{{
			Kind:    ErrCUELoad,
			Path:    "resources",
			Message: fmt.Sprintf("cannot iterate resources: %v", iterErr),
		}}
	}

	for iter.Next() {
		name := iter.Selector().String()
		// Comprehension-generated fields use CUE string labels whose
		// selector representation includes surrounding quotes.  Strip them
		// so the resource map uses bare names (e.g. "bucket-orders", not
		// "\"bucket-orders\"").
		if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
			name = name[1 : len(name)-1]
		}
		resVal := iter.Value()

		kindVal := resVal.LookupPath(cue.ParsePath("kind"))
		if !kindVal.Exists() {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    fmt.Sprintf("resources.%s", name),
				Message: "resource is missing 'kind' field",
				Detail:  "Every resource must declare a 'kind' (e.g., kind: \"S3Bucket\").",
			})
			continue
		}

		kind, kerr := kindVal.String()
		if kerr != nil {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    fmt.Sprintf("resources.%s.kind", name),
				Message: fmt.Sprintf("'kind' must be a string: %v", kerr),
			})
			continue
		}

		// Lifecycle is a Praxis-level field, not part of any driver schema.
		// We validate it independently and strip it before schema
		// unification so CUE closed definitions don't reject it.
		var lifecycleJSON json.RawMessage
		lifecycleVal := resVal.LookupPath(cue.ParsePath("lifecycle"))
		if lifecycleVal.Exists() {
			if err := e.lifecycleExt.LookupPath(cue.ParsePath("lifecycle")).Unify(lifecycleVal).Validate(); err != nil {
				errs = append(errs, TemplateError{
					Kind:    ErrCUEValidation,
					Path:    fmt.Sprintf("resources.%s.lifecycle", name),
					Message: fmt.Sprintf("invalid lifecycle block: %v", err),
				})
				continue
			}
			lcBytes, _ := lifecycleVal.MarshalJSON()
			lifecycleJSON = lcBytes

			// Strip lifecycle from the CUE value for schema validation.
			rawJSON, merr := resVal.MarshalJSON()
			if merr == nil {
				var m map[string]json.RawMessage
				if json.Unmarshal(rawJSON, &m) == nil {
					delete(m, "lifecycle")
					if stripped, jerr := json.Marshal(m); jerr == nil {
						resVal = e.ctx.CompileBytes(stripped)
					}
				}
			}
		}

		if schemaVal != nil {
			schemaDef := schemaVal.LookupPath(cue.ParsePath("#" + kind))
			if schemaDef.Exists() {
				resVal = resVal.Unify(schemaDef)
			}
		}

		if verr := resVal.Validate(cue.Concrete(true), cue.Final()); verr != nil {
			for _, cerr := range errors.Errors(verr) {
				path := fmt.Sprintf("resources.%s", name)
				pathSuffix := formatCUEPath(cerr)
				if pathSuffix != "" {
					path = path + "." + pathSuffix
				}

				source := ""
				positions := cerr.InputPositions()
				if len(positions) > 0 {
					source = positions[0].String()
				}

				errs = append(errs, TemplateError{
					Kind:    ErrCUEValidation,
					Path:    path,
					Source:  source,
					Message: cerr.Error(),
					Cause:   cerr,
				})
			}
			continue
		}

		jsonBytes, jerr := resVal.MarshalJSON()
		if jerr != nil {
			errs = append(errs, TemplateError{
				Kind:    ErrCUEValidation,
				Path:    fmt.Sprintf("resources.%s", name),
				Message: fmt.Sprintf("failed to export JSON: %v", jerr),
			})
			continue
		}

		// Splice lifecycle back into output JSON if it was present.
		if lifecycleJSON != nil {
			var m map[string]json.RawMessage
			if json.Unmarshal(jsonBytes, &m) == nil {
				m["lifecycle"] = lifecycleJSON
				if merged, merr := json.Marshal(m); merr == nil {
					jsonBytes = merged
				}
			}
		}

		results[name] = json.RawMessage(jsonBytes)
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return results, nil
}

// loadSchemas loads all CUE packages under schemaDir and unifies them into
// a single CUE value. This combined value contains named definitions (e.g.
// #S3Bucket) that evaluateResources looks up per resource kind.
func (e *Engine) loadSchemas() (*cue.Value, error) {
	cfg := &load.Config{
		Dir: e.schemaDir,
	}

	instances := load.Instances([]string{"./..."}, cfg)
	if len(instances) == 0 {
		return nil, nil
	}

	var combined *cue.Value
	for _, inst := range instances {
		if inst.Err != nil {
			continue
		}
		val := e.ctx.BuildInstance(inst)
		if val.Err() != nil {
			continue
		}
		if combined == nil {
			combined = &val
		} else {
			unified := combined.Unify(val)
			combined = &unified
		}
	}

	return combined, nil
}

// convertLoadErrors translates CUE loader errors into TemplateErrors with
// source position information extracted from CUE's error positions.
func (e *Engine) convertLoadErrors(err error, templatePath string) TemplateErrors {
	var errs TemplateErrors
	for _, cerr := range errors.Errors(err) {
		source := ""
		positions := cerr.InputPositions()
		if len(positions) > 0 {
			source = positions[0].String()
		}
		errs = append(errs, TemplateError{
			Kind:    ErrCUELoad,
			Path:    templatePath,
			Source:  source,
			Message: cerr.Error(),
			Cause:   cerr,
		})
	}
	if len(errs) == 0 {
		errs = append(errs, TemplateError{
			Kind:    ErrCUELoad,
			Path:    templatePath,
			Message: err.Error(),
			Cause:   err,
		})
	}
	return errs
}

// formatCUEPath extracts the dot-separated path from a CUE error for use in
// TemplateError.Path (e.g. "spec.tags.Environment").
func formatCUEPath(err errors.Error) string {
	path := err.Path()
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, ".")
}

// templateErrorSignature generates a stable key from Path+Message used to
// de-duplicate errors between baseline and policy evaluations.
func templateErrorSignature(item TemplateError) string {
	return item.Path + "|" + item.Message
}

// appendUniqueString appends candidate to items only if it is not already
// present. Used to track which policy names contributed to a given error.
func appendUniqueString(items []string, candidate string) []string {
	if slices.Contains(items, candidate) {
		return items
	}
	return append(items, candidate)
}
