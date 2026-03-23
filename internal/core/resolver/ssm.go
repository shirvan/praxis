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

const ssmBatchSize = 10

// SSMClient abstracts the AWS SSM SDK operations needed for parameter resolution.
type SSMClient interface {
	GetParameters(ctx context.Context, params *ssm.GetParametersInput, optFns ...func(*ssm.Options)) (*ssm.GetParametersOutput, error)
}

// SSMResolver resolves ssm:/// URI references in JSON specs by fetching
// parameter values from AWS Systems Manager Parameter Store.
type SSMResolver struct {
	client SSMClient
}

// SensitiveParams tracks which resolved values should be masked when rendered in
// user-facing output such as plans, deployment detail responses, or CLI views.
//
// The values themselves are still resolved normally because durable execution
// needs the real data. This structure exists only to help downstream display
// logic know which JSON paths should be replaced with "***".
type SensitiveParams struct {
	// Paths is a set keyed by "resourceName.json.path".
	Paths map[string]bool `json:"paths"`
}

// NewSensitiveParams creates an empty sensitivity set.
func NewSensitiveParams() *SensitiveParams {
	return &SensitiveParams{Paths: make(map[string]bool)}
}

// Add marks a resource-local JSON path as sensitive.
func (s *SensitiveParams) Add(resourceName, jsonPath string) {
	if s == nil {
		return
	}
	if s.Paths == nil {
		s.Paths = make(map[string]bool)
	}
	s.Paths[resourceName+"."+jsonPath] = true
}

// Contains reports whether a resource-local JSON path should be masked.
func (s *SensitiveParams) Contains(resourceName, jsonPath string) bool {
	if s == nil || s.Paths == nil {
		return false
	}
	return s.Paths[resourceName+"."+jsonPath]
}

type ssmReference struct {
	Raw       string
	Path      string
	Sensitive bool
}

type ssmReferenceOccurrence struct {
	ResourceName string
	JSONPath     string
	Ref          ssmReference
}

// NewSSMResolver creates a resolver backed by the given SSM client.
func NewSSMResolver(client SSMClient) *SSMResolver {
	return &SSMResolver{client: client}
}

// Resolve scans rawSpecs for ssm:/// strings, batch-fetches all unique paths,
// and returns a new map with all ssm:/// strings replaced by their values.
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

// batchFetch retrieves parameters in batches of ssmBatchSize.
func (r *SSMResolver) batchFetch(ctx context.Context, paths []string) (map[string]string, template.TemplateErrors) {
	resolved := make(map[string]string, len(paths))
	var errs template.TemplateErrors

	for i := 0; i < len(paths); i += ssmBatchSize {
		end := i + ssmBatchSize
		if end > len(paths) {
			end = len(paths)
		}
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
