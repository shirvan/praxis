package kernel

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shirvan/praxis/pkg/types"
)

func validateLifecyclePolicy(policy types.LifecyclePolicy) error {
	return types.ValidateLifecyclePolicy(policy)
}

// hasDriftIgnoring evaluates the resource's own semantic drift function after
// replacing ignored desired fields with their observed values. This keeps the
// provider-specific comparison authoritative while applying one shared
// lifecycle policy to every typed driver.
func hasDriftIgnoring[S, Obs any](hasDrift func(S, Obs) bool, desired S, observed Obs, ignoreChanges []string) (bool, error) {
	rawDrift := hasDrift(desired, observed)
	if !rawDrift || len(ignoreChanges) == 0 {
		return rawDrift, nil
	}

	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return false, fmt.Errorf("marshal desired state for ignoreChanges: %w", err)
	}
	observedJSON, err := json.Marshal(observed)
	if err != nil {
		return false, fmt.Errorf("marshal observed state for ignoreChanges: %w", err)
	}
	var desiredDoc map[string]any
	var observedDoc map[string]any
	if err := json.Unmarshal(desiredJSON, &desiredDoc); err != nil {
		return false, fmt.Errorf("decode desired state for ignoreChanges: %w", err)
	}
	if err := json.Unmarshal(observedJSON, &observedDoc); err != nil {
		return false, fmt.Errorf("decode observed state for ignoreChanges: %w", err)
	}
	for _, path := range ignoreChanges {
		parts := strings.Split(path, ".")
		if value, ok := jsonPathValue(observedDoc, parts); ok {
			setJSONPath(desiredDoc, parts, value)
		} else {
			deleteJSONPath(desiredDoc, parts)
		}
	}

	maskedJSON, err := json.Marshal(desiredDoc)
	if err != nil {
		return false, fmt.Errorf("marshal masked desired state for ignoreChanges: %w", err)
	}
	var masked S
	if err := json.Unmarshal(maskedJSON, &masked); err != nil {
		return false, fmt.Errorf("decode masked desired state for ignoreChanges: %w", err)
	}
	return hasDrift(masked, observed), nil
}

func jsonPathValue(doc map[string]any, parts []string) (any, bool) {
	var current any = doc
	for _, part := range parts {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func setJSONPath(doc map[string]any, parts []string, value any) {
	current := doc
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = make(map[string]any)
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func deleteJSONPath(doc map[string]any, parts []string) {
	current := doc
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, parts[len(parts)-1])
}
