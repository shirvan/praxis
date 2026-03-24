package template

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/load"
)

// PolicySource pairs a policy's CUE source with its name for diagnostics.
type PolicySource struct {
	Name   string
	Source []byte
}

type compiledPolicy struct {
	name  string
	value cue.Value
}

// Engine loads CUE schemas and evaluates templates against them.
type Engine struct {
	schemaDir string
	ctx       *cue.Context

	// lifecycleExt is a pre-compiled CUE value that permits an optional
	// lifecycle block on every resource schema. It is unified with each
	// schema definition before resource validation so that templates can
	// declare lifecycle rules without modifying individual schema files.
	lifecycleExt cue.Value
}

// lifecycleCUE defines the optional lifecycle block accepted on all resource types.
const lifecycleCUE = `{
	lifecycle?: {
		preventDestroy?: bool
		ignoreChanges?: [...string]
	}
}`

// NewEngine creates an engine that loads provider schemas from schemaDir.
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
// by resource name. All errors are collected in a single call.
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
// payloads over Restate rather than as files on local disk.
func (e *Engine) EvaluateBytes(content []byte, vars map[string]any) (map[string]json.RawMessage, error) {
	result, err := e.EvaluateBytesWithPolicies(content, nil, vars)
	if err != nil {
		return nil, err
	}
	return result.Resources, nil
}

// EvaluateBytesWithPolicies evaluates raw template bytes and applies policies
// before resource validation and JSON extraction.
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

	// Load schemas for unification.
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
	for _, item := range finalDataErrs {
		combined = append(combined, item)
	}
	combined = append(combined, policyErrs...)
	return nil, combined
}

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

func formatCUEPath(err errors.Error) string {
	path := err.Path()
	if len(path) == 0 {
		return ""
	}
	return strings.Join(path, ".")
}

func templateErrorSignature(item TemplateError) string {
	return item.Path + "|" + item.Message
}

func appendUniqueString(items []string, candidate string) []string {
	for _, item := range items {
		if item == candidate {
			return items
		}
	}
	return append(items, candidate)
}
