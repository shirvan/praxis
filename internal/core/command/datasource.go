// datasource.go implements data source validation, resolution, and expression
// substitution for the template evaluation pipeline.
//
// # Data sources in Praxis templates
//
// Templates can declare `data` blocks that reference existing cloud resources.
// For example, a template might look up a VPC by tag and inject its ID into
// a subnet resource spec:
//
//	data: myVpc: {
//	    kind: "AWS::EC2::VPC"
//	    filter: { tag: { Name: "production" } }
//	}
//	resources: mySubnet: {
//	    kind: "AWS::EC2::Subnet"
//	    spec: { vpcId: "${data.myVpc.outputs.vpcId}" }
//	}
//
// The data source pipeline has three phases:
//
//  1. Validation (validateDataSources): Ensure data source names don't
//     collide with resource names, the kind has a registered adapter, and
//     at least one filter criterion is specified.
//
//  2. Resolution (resolveDataSources): Call the provider adapter's Lookup
//     method for each data source to fetch the live cloud resource outputs.
//     The Lookup call runs inside the adapter's Restate Run block.
//
//  3. Substitution (substituteDataExprs): Walk all resource specs and replace
//     ${data.<name>.outputs.<field>} string expressions with actual values
//     from the resolved data sources.
package command

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/internal/core/provider"
	"github.com/shirvan/praxis/internal/core/template"
	"github.com/shirvan/praxis/pkg/types"
)

// lookupCapableAdapter is an optional interface that provider adapters may
// implement to support data source lookups. Not all resource kinds support
// lookup — attempting to use an unsupported kind as a data source returns a
// 501 TerminalError.
type lookupCapableAdapter interface {
	Lookup(ctx restate.Context, account string, filter provider.LookupFilter) (map[string]any, error)
}

// dataExprRe matches data source expression strings of the form:
//
//	${data.<sourceName>.outputs.<fieldName>}
//
// Capture groups: [1] = source name, [2] = output field name.
// The expression must be the entire string value (no partial substitution).
var dataExprRe = regexp.MustCompile(`^\$\{data\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs\.([A-Za-z_][A-Za-z0-9_-]*)\}$`)

// validateDataSources performs static validation of data source declarations
// before any cloud API calls are made. It checks:
//   - No name collision between data sources and resources (they share the
//     expression namespace).
//   - The data source's kind has a registered provider adapter.
//   - At least one filter criterion (id, name, or tag) is specified.
//
// Returns a user-facing error with the data source name for context.
func (s *PraxisCommandService) validateDataSources(dataSources map[string]template.DataSourceSpec, resourceNames map[string]bool) error {
	for name, spec := range dataSources {
		if resourceNames[name] {
			return fmt.Errorf("data source %q conflicts with resource of the same name; data source and resource names must be unique", name)
		}
		if _, err := s.providers.Get(spec.Kind); err != nil {
			return fmt.Errorf("data source %q: %w", name, err)
		}
		if strings.TrimSpace(spec.Filter.ID) == "" && strings.TrimSpace(spec.Filter.Name) == "" && len(spec.Filter.Tag) == 0 {
			return fmt.Errorf("data source %q: filter must specify at least one of: id, name, tag", name)
		}
	}
	return nil
}

// resolveDataSources executes the cloud API Lookup call for each data source.
// Data sources are processed in sorted name order for deterministic behavior
// during Restate replay.
//
// Each adapter's Lookup method may internally use restate.Run to journal
// the API call, ensuring the result is replayed from the journal on retries
// rather than re-executing the cloud API call.
//
// The defaultAccount is used when the data source doesn't specify an explicit
// account override.
func (s *PraxisCommandService) resolveDataSources(
	ctx restate.Context,
	dataSources map[string]template.DataSourceSpec,
	defaultAccount string,
) (map[string]types.DataSourceResult, error) {
	resolved := make(map[string]types.DataSourceResult, len(dataSources))

	// Sort names for deterministic processing order, which is important
	// for Restate journal replay consistency.
	names := make([]string, 0, len(dataSources))
	for name := range dataSources {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		spec := dataSources[name]
		adapter, err := s.providers.Get(spec.Kind)
		if err != nil {
			return nil, fmt.Errorf("data source %q: %w", name, err)
		}
		// Assert that the adapter supports lookup. Not all resource kinds
		// can serve as data sources (e.g., resource kinds that don't have
		// a meaningful "find by filter" API).
		lookupAdapter, ok := adapter.(lookupCapableAdapter)
		if !ok {
			return nil, providerErrLookupUnsupported(spec.Kind)
		}

		// Use the data source's explicit account if set, otherwise fall
		// back to the deployment's default account. This allows cross-account
		// data source lookups.
		account := strings.TrimSpace(spec.Account)
		if account == "" {
			account = defaultAccount
		}

		outputs, err := lookupAdapter.Lookup(ctx, account, provider.LookupFilter{
			Region: strings.TrimSpace(spec.Region),
			ID:     strings.TrimSpace(spec.Filter.ID),
			Name:   strings.TrimSpace(spec.Filter.Name),
			Tag:    cloneStringMapAny(spec.Filter.Tag),
		})
		if err != nil {
			return nil, fmt.Errorf("data source %q (%s): lookup failed: %w", name, spec.Kind, err)
		}

		resolved[name] = types.DataSourceResult{Kind: spec.Kind, Outputs: outputs}
	}

	return resolved, nil
}

// substituteDataExprs replaces all ${data.<name>.outputs.<field>} expressions
// in resource specs with the corresponding resolved data source values.
// Returns the updated specs; the original map is not modified.
func substituteDataExprs(specs map[string]json.RawMessage, dataSources map[string]types.DataSourceResult) (map[string]json.RawMessage, error) {
	if len(dataSources) == 0 {
		return specs, nil
	}

	resolved := make(map[string]json.RawMessage, len(specs))
	for name, raw := range specs {
		updated, err := substituteDataExprsInDoc(raw, dataSources)
		if err != nil {
			return nil, fmt.Errorf("resource %q: %w", name, err)
		}
		resolved[name] = updated
	}

	return resolved, nil
}

// substituteDataExprsInDoc processes a single resource spec JSON document,
// recursively walking the JSON tree and replacing data expression strings.
func substituteDataExprsInDoc(doc json.RawMessage, dataSources map[string]types.DataSourceResult) (json.RawMessage, error) {
	var root any
	if err := json.Unmarshal(doc, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	updated, err := walkAndSubstitute(root, dataSources)
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(updated)
	if err != nil {
		return nil, fmt.Errorf("marshal substituted JSON: %w", err)
	}
	return encoded, nil
}

// walkAndSubstitute recursively walks a decoded JSON value and replaces any
// string matching the data expression pattern with the resolved value.
//
// The substitution is whole-value: if a string is "${data.myVpc.outputs.vpcId}"
// and the resolved value is "vpc-123abc", the string is replaced with
// "vpc-123abc". If the resolved value is a non-string type (number, bool),
// the type is preserved — this enables type-correct injection of integers
// and booleans from data source outputs.
func walkAndSubstitute(current any, dataSources map[string]types.DataSourceResult) (any, error) {
	switch typed := current.(type) {
	case map[string]any:
		for key, value := range typed {
			replaced, err := walkAndSubstitute(value, dataSources)
			if err != nil {
				return nil, fmt.Errorf("at key %q: %w", key, err)
			}
			typed[key] = replaced
		}
		return typed, nil
	case []any:
		for index, item := range typed {
			replaced, err := walkAndSubstitute(item, dataSources)
			if err != nil {
				return nil, fmt.Errorf("at index %d: %w", index, err)
			}
			typed[index] = replaced
		}
		return typed, nil
	case string:
		trimmed := strings.TrimSpace(typed)
		match := dataExprRe.FindStringSubmatch(trimmed)
		if match == nil {
			return typed, nil
		}

		// match[1] = data source name, match[2] = output field name.
		name := match[1]
		field := match[2]
		result, ok := dataSources[name]
		if !ok {
			return nil, fmt.Errorf("unresolved data source %q in expression %q", name, typed)
		}
		value, ok := result.Outputs[field]
		if !ok {
			return nil, fmt.Errorf("data source %q has no output %q (available: %v)", name, field, outputKeys(result.Outputs))
		}
		return value, nil
	default:
		// Non-string, non-container values (numbers, booleans, null)
		// pass through unchanged.
		return current, nil
	}
}

// outputKeys returns sorted keys from a data source outputs map.
// Used in error messages to help users identify available fields.
func outputKeys(outputs map[string]any) []string {
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// cloneStringMapAny creates a shallow copy of a string-to-string map.
// Used to avoid mutating the original filter tags during resolution.
func cloneStringMapAny(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

// providerErrLookupUnsupported returns a 501 TerminalError when a resource
// kind is used as a data source but its adapter doesn't implement the
// lookupCapableAdapter interface.
func providerErrLookupUnsupported(kind string) error {
	return restate.TerminalError(fmt.Errorf("data source lookup is not supported for %q", kind), 501)
}
