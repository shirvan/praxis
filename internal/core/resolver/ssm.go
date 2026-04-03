package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/shirvan/praxis/internal/core/jsonpath"
	"github.com/shirvan/praxis/internal/core/template"
)

// ssmBatchSize is the maximum number of SSM parameter names per GetParameters
// API call. AWS limits this to 10.
const ssmBatchSize = 10

// SSMClient abstracts the AWS SSM SDK operations needed for parameter resolution.
// This interface exists to allow unit testing with a mock SSM backend.
type SSMClient interface {
	GetParameters(ctx context.Context, params *ssm.GetParametersInput, optFns ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

// SSMResolver resolves ssm:/// URI references in JSON specs by fetching
// parameter values from AWS Systems Manager Parameter Store.
//
// Templates can reference SSM parameters using the URI format:
//
//	ssm:///path/to/parameter              → resolved normally
//	ssm:///path/to/secret?sensitive=true   → resolved + marked for masking in CLI output
//
// The resolver scans all resource specs for ssm:/// strings, deduplicates
// the parameter paths, batch-fetches them from AWS, and replaces the URIs
// with the resolved values in the JSON documents.
type SSMResolver struct {
	client SSMClient
}

// SensitiveParams tracks which resolved values should be masked when rendered in
// user-facing output such as plans, deployment detail responses, or CLI views.
//
// The values themselves are still resolved normally because durable execution
// needs the real data. This structure exists only to help downstream display
// logic know which JSON paths should be replaced with "***".
//
// A parameter is marked sensitive when the template author appends
// ?sensitive=true to the ssm:/// URI.
type SensitiveParams struct {
	// Paths is a set keyed by "resourceName.json.path".
	Paths map[string]bool `json:"paths"`
}

// NewSensitiveParams creates an empty sensitivity tracking set.
func NewSensitiveParams() *SensitiveParams {
	return &SensitiveParams{Paths: make(map[string]bool)}
}

// Add marks a resource-local JSON path as sensitive. The key format is
// "resourceName.json.path" (e.g. "my-db.spec.password").
func (s *SensitiveParams) Add(resourceName, jsonPath string) {
	if s == nil {
		return
	}
	if s.Paths == nil {
		s.Paths = make(map[string]bool)
	}
	s.Paths[resourceName+"."+jsonPath] = true
}

// Contains reports whether a resource-local JSON path should be masked
// in user-facing output.
func (s *SensitiveParams) Contains(resourceName, jsonPath string) bool {
	if s == nil || s.Paths == nil {
		return false
	}
	return s.Paths[resourceName+"."+jsonPath]
}

// ssmReference is a parsed ssm:/// URI with its path and sensitivity flag.
type ssmReference struct {
	Raw       string // Original URI string from the template
	Path      string // SSM parameter path (e.g. "/prod/db/password")
	Sensitive bool   // Whether to mask the resolved value in output
}

// ssmReferenceOccurrence records where an SSM URI was found in the resource
// specs, allowing the resolver to replace the value at the correct JSON path
// after batch fetching.
type ssmReferenceOccurrence struct {
	ResourceName string       // Name of the resource containing the reference
	JSONPath     string       // Dot-separated path within the resource's JSON
	Ref          ssmReference // The parsed SSM URI
}

// NewSSMResolver creates a resolver backed by the given SSM client.
func NewSSMResolver(client SSMClient) *SSMResolver {
	return &SSMResolver{client: client}
}

// Resolve scans rawSpecs for ssm:/// strings, batch-fetches all unique paths,
// and returns a new map with all ssm:/// strings replaced by their values.
// This is the non-Restate entry point used for local CLI evaluation.
// All resolution errors are collected before returning.
func (r *SSMResolver) Resolve(ctx context.Context, rawSpecs map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	resolved, _, err := resolveSSMReferences(rawSpecs, func(paths []string) (map[string]string, error) {
		return r.batchFetchMap(ctx, paths)
	})
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

// resolveSSMReferences is the shared resolution core. It accepts a pluggable
// fetch function so that the Restate-journaled resolver can inject
// restate.Run-wrapped fetching while reusing all the scanning and
// replacement logic.
//
// Steps:
//  1. Unmarshal each resource spec and walk the JSON tree for ssm:/// strings.
//  2. Parse each URI into path + sensitivity flag.
//  3. Deduplicate paths and call the fetch function once.
//  4. Replace each occurrence with the resolved value using jsonpath.Set.
//  5. Track sensitive paths for downstream masking.
func resolveSSMReferences(
	rawSpecs map[string]json.RawMessage,
	fetch func(paths []string) (map[string]string, error),
) (map[string]json.RawMessage, *SensitiveParams, error) {
	decoded := make(map[string]any, len(rawSpecs))
	occurrences := make([]ssmReferenceOccurrence, 0)
	pathSet := make(map[string]struct{})
	var errs template.TemplateErrors

	for name, raw := range rawSpecs {
		var doc any
		if err := json.Unmarshal(raw, &doc); err != nil {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrResolve,
				Path:    fmt.Sprintf("resources.%s", name),
				Message: fmt.Sprintf("invalid JSON resource document: %v", err),
				Cause:   err,
			})
			continue
		}
		decoded[name] = doc

		walkJSON(doc, "", func(path string, value any) {
			str, ok := value.(string)
			if !ok || !strings.HasPrefix(str, "ssm:///") {
				return
			}
			ref, err := parseSSMReference(str)
			if err != nil {
				errs = append(errs, template.TemplateError{
					Kind:    template.ErrResolve,
					Path:    fmt.Sprintf("resources.%s.%s", name, path),
					Message: err.Error(),
					Detail:  "Use the form ssm:///path/to/parameter or add ?sensitive=true to mark the value for masking.",
				})
				return
			}
			occurrences = append(occurrences, ssmReferenceOccurrence{
				ResourceName: name,
				JSONPath:     path,
				Ref:          ref,
			})
			pathSet[ref.Path] = struct{}{}
		})
	}

	if len(errs) > 0 {
		return nil, nil, errs
	}
	if len(pathSet) == 0 {
		return rawSpecs, NewSensitiveParams(), nil
	}

	paths := make([]string, 0, len(pathSet))
	for path := range pathSet {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	resolved, err := fetch(paths)
	if err != nil {
		return nil, nil, err
	}

	sensitive := NewSensitiveParams()
	for _, occurrence := range occurrences {
		resolvedValue, ok := resolved[occurrence.Ref.Path]
		if !ok {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrResolve,
				Path:    fmt.Sprintf("resources.%s.%s", occurrence.ResourceName, occurrence.JSONPath),
				Message: fmt.Sprintf("SSM parameter %q not found in resolved cache", occurrence.Ref.Path),
			})
			continue
		}
		updated, setErr := jsonpath.Set(decoded[occurrence.ResourceName], occurrence.JSONPath, resolvedValue)
		if setErr != nil {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrResolve,
				Path:    fmt.Sprintf("resources.%s.%s", occurrence.ResourceName, occurrence.JSONPath),
				Message: setErr.Error(),
				Detail:  "Ensure the stored JSON path still points at a string SSM placeholder in the rendered resource document.",
				Cause:   setErr,
			})
			continue
		}
		decoded[occurrence.ResourceName] = updated
		if occurrence.Ref.Sensitive {
			sensitive.Add(occurrence.ResourceName, occurrence.JSONPath)
		}
	}

	if len(errs) > 0 {
		return nil, nil, errs
	}

	result := make(map[string]json.RawMessage, len(decoded))
	for name, doc := range decoded {
		marshaled, err := json.Marshal(doc)
		if err != nil {
			return nil, nil, template.TemplateErrors{template.TemplateError{
				Kind:    template.ErrResolve,
				Path:    fmt.Sprintf("resources.%s", name),
				Message: fmt.Sprintf("failed to marshal resolved JSON: %v", err),
				Cause:   err,
			}}
		}
		result[name] = marshaled
	}

	return result, sensitive, nil
}

// parseSSMReference parses an ssm:/// URI string into its path and sensitivity
// flag. The URI format is: ssm:///path/to/param[?sensitive=true]
func parseSSMReference(raw string) (ssmReference, error) {
	uri, err := url.Parse(raw)
	if err != nil {
		return ssmReference{}, fmt.Errorf("invalid SSM URI %q: %w", raw, err)
	}
	if uri.Scheme != "ssm" {
		return ssmReference{}, fmt.Errorf("unsupported URI scheme %q for %q", uri.Scheme, raw)
	}
	if uri.Path == "" || uri.Path == "/" {
		return ssmReference{}, fmt.Errorf("SSM URI %q must include a parameter path", raw)
	}
	return ssmReference{
		Raw:       raw,
		Path:      uri.Path,
		Sensitive: strings.EqualFold(uri.Query().Get("sensitive"), "true"),
	}, nil
}

// walkJSON recursively visits every node in a generic JSON value tree.
// The visitor callback receives the dot-separated path and the value at
// each node (including intermediate maps and slices). Map keys are sorted
// for deterministic traversal order.
func walkJSON(value any, path string, visit func(path string, value any)) {
	visit(path, value)

	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			walkJSON(typed[key], appendJSONPath(path, key), visit)
		}
	case []any:
		for index, item := range typed {
			walkJSON(item, appendJSONPath(path, fmt.Sprintf("%d", index)), visit)
		}
	}
}

// appendJSONPath concatenates two path segments with a dot separator.
func appendJSONPath(path, part string) string {
	if path == "" {
		return part
	}
	return path + "." + part
}

func (r *SSMResolver) batchFetchMap(ctx context.Context, paths []string) (map[string]string, error) {
	resolved, errs := r.batchFetch(ctx, paths)
	if len(errs) > 0 {
		return nil, errs
	}
	return resolved, nil
}

// batchFetch retrieves parameters in batches of ssmBatchSize (10) to respect
// the AWS GetParameters API limit. Invalid (not found) parameters are reported
// as template errors with actionable suggestions.
func (r *SSMResolver) batchFetch(ctx context.Context, paths []string) (map[string]string, template.TemplateErrors) {
	resolved := make(map[string]string, len(paths))
	var errs template.TemplateErrors

	for i := 0; i < len(paths); i += ssmBatchSize {
		end := min(i+ssmBatchSize, len(paths))
		batch := paths[i:end]

		out, err := r.client.GetParameters(ctx, &ssm.GetParametersInput{
			Names:          batch,
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			// Classify the error.
			errMsg := err.Error()
			detail := "Check that the SSM parameter exists and your IAM role has ssm:GetParameters permission."
			if strings.Contains(errMsg, "AccessDenied") || strings.Contains(errMsg, "not authorized") {
				detail = "Ensure the execution role has ssm:GetParameters permission on the parameter path."
			}
			for _, p := range batch {
				errs = append(errs, template.TemplateError{
					Kind:    template.ErrResolve,
					Path:    p,
					Message: fmt.Sprintf("SSM GetParameters failed: %v", err),
					Detail:  detail,
					Cause:   err,
				})
			}
			continue
		}

		for _, param := range out.Parameters {
			if param.Name != nil && param.Value != nil {
				resolved[*param.Name] = *param.Value
			}
		}

		// Report invalid (not found) parameters.
		for _, name := range out.InvalidParameters {
			errs = append(errs, template.TemplateError{
				Kind:    template.ErrResolve,
				Path:    name,
				Message: fmt.Sprintf("SSM parameter %q not found", name),
				Detail:  "Create the parameter in SSM or check the path for typos.",
			})
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}
	return resolved, nil
}
