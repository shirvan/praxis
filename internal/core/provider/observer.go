package provider

import (
	"encoding/json"
	"fmt"
	"reflect"

	restate "github.com/restatedev/sdk-go"

	"github.com/shirvan/praxis/pkg/types"
)

// ObserveResult describes the current state of a managed resource as seen by a
// fast observe path.
type ObserveResult struct {
	Exists   bool           `json:"exists"`
	UpToDate bool           `json:"upToDate"`
	Outputs  map[string]any `json:"outputs,omitempty"`
}

// Observer is an optional adapter extension for custom observe-before-act
// behavior.
type Observer interface {
	Observe(ctx restate.Context, key string, account string, spec any) (ObserveResult, error)
}

// ObserveStoredState is the generic observe fallback for adapters that do not
// implement Observer directly. It reuses the driver's shared GetStatus,
// GetInputs, and GetOutputs handlers.
func ObserveStoredState(ctx restate.Context, adapter Adapter, key string, desiredSpec any) (ObserveResult, error) {
	status, err := restate.Object[types.StatusResponse](ctx, adapter.ServiceName(), key, "GetStatus").Request(restate.Void{})
	if err != nil {
		return ObserveResult{}, fmt.Errorf("observe status: %w", err)
	}
	if status.Status == "" || status.Status == types.StatusPending || status.Status == types.StatusDeleted {
		return ObserveResult{Exists: false}, nil
	}

	outputs, err := fetchJSONMap(ctx, adapter.ServiceName(), key, "GetOutputs")
	if err != nil {
		return ObserveResult{}, fmt.Errorf("observe outputs: %w", err)
	}
	storedInputs, err := fetchComparableJSON(ctx, adapter.ServiceName(), key, "GetInputs")
	if err != nil {
		return ObserveResult{Exists: true, UpToDate: false, Outputs: outputs}, nil
	}
	desiredComparable, err := normalizeComparableValue(desiredSpec)
	if err != nil {
		return ObserveResult{}, fmt.Errorf("normalize desired spec: %w", err)
	}

	return ObserveResult{
		Exists:   true,
		UpToDate: reflect.DeepEqual(stripControlFields(storedInputs), stripControlFields(desiredComparable)),
		Outputs:  outputs,
	}, nil
}

func fetchJSONMap(ctx restate.Context, serviceName, key, handler string) (map[string]any, error) {
	raw, err := restate.Object[json.RawMessage](ctx, serviceName, key, handler).Request(restate.Void{})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func fetchComparableJSON(ctx restate.Context, serviceName, key, handler string) (any, error) {
	raw, err := restate.Object[json.RawMessage](ctx, serviceName, key, handler).Request(restate.Void{})
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func normalizeComparableValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func stripControlFields(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		cleaned := make(map[string]any, len(typed))
		for key, child := range typed {
			if key == "account" {
				continue
			}
			cleaned[key] = stripControlFields(child)
		}
		return cleaned
	case []any:
		cleaned := make([]any, 0, len(typed))
		for _, child := range typed {
			cleaned = append(cleaned, stripControlFields(child))
		}
		return cleaned
	default:
		return value
	}
}
