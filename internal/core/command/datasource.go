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

type lookupCapableAdapter interface {
	Lookup(ctx restate.Context, account string, filter provider.LookupFilter) (map[string]any, error)
}

var dataExprRe = regexp.MustCompile(`^\$\{data\.([A-Za-z_][A-Za-z0-9_-]*)\.outputs\.([A-Za-z_][A-Za-z0-9_-]*)\}$`)

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

func (s *PraxisCommandService) resolveDataSources(
	ctx restate.Context,
	dataSources map[string]template.DataSourceSpec,
	defaultAccount string,
) (map[string]types.DataSourceResult, error) {
	resolved := make(map[string]types.DataSourceResult, len(dataSources))

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
		lookupAdapter, ok := adapter.(lookupCapableAdapter)
		if !ok {
			return nil, providerErrLookupUnsupported(spec.Kind)
		}

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
		return current, nil
	}
}

func outputKeys(outputs map[string]any) []string {
	keys := make([]string, 0, len(outputs))
	for key := range outputs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneStringMapAny(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

func providerErrLookupUnsupported(kind string) error {
	return restate.TerminalError(fmt.Errorf("data source lookup is not supported for %q", kind), 501)
}
