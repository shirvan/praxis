package slack

import (
	"testing"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
	restatetest "github.com/restatedev/sdk-go/testing"
)

// stubSinkConfig is a no-op stand-in for NotificationSinkConfig so that the
// one-way sends from syncSink are accepted by the Restate test environment.
type stubSinkConfig struct{}

func (stubSinkConfig) ServiceName() string                                        { return "NotificationSinkConfig" }
func (stubSinkConfig) Upsert(ctx restate.ObjectContext, _ SinkRegistration) error { return nil }
func (stubSinkConfig) Remove(ctx restate.ObjectContext, _ string) error           { return nil }

func TestMergeFilters(t *testing.T) {
	rules := []WatchRule{
		{ID: "1", Enabled: true, Filter: WatchFilter{Types: []string{"a", "b"}, Workspaces: []string{"prod"}}},
		{ID: "2", Enabled: true, Filter: WatchFilter{Types: []string{"b", "c"}, Severities: []string{"error"}}},
		{ID: "3", Enabled: false, Filter: WatchFilter{Types: []string{"d"}}},
	}
	merged := MergeFilters(rules)

	// Should contain a, b, c but not d (disabled)
	if len(merged.Types) != 3 {
		t.Errorf("expected 3 types, got %d: %v", len(merged.Types), merged.Types)
	}
	typeSet := map[string]bool{}
	for _, v := range merged.Types {
		typeSet[v] = true
	}
	if !typeSet["a"] || !typeSet["b"] || !typeSet["c"] {
		t.Errorf("expected types a, b, c; got %v", merged.Types)
	}
	if typeSet["d"] {
		t.Error("disabled rule's type 'd' should not be included")
	}

	if len(merged.Workspaces) != 1 || merged.Workspaces[0] != "prod" {
		t.Errorf("expected workspaces [prod], got %v", merged.Workspaces)
	}

	if len(merged.Severities) != 1 || merged.Severities[0] != "error" {
		t.Errorf("expected severities [error], got %v", merged.Severities)
	}
}

func TestMergeFilters_AllDisabled(t *testing.T) {
	rules := []WatchRule{
		{ID: "1", Enabled: false, Filter: WatchFilter{Types: []string{"a"}}},
	}
	merged := MergeFilters(rules)
	if len(merged.Types) != 0 {
		t.Errorf("expected no types for all-disabled rules, got %v", merged.Types)
	}
}

func TestMergeFilters_Empty(t *testing.T) {
	merged := MergeFilters(nil)
	if len(merged.Types) != 0 || len(merged.Categories) != 0 {
		t.Errorf("expected empty filter for nil rules")
	}
}

func TestAllDisabled(t *testing.T) {
	tests := []struct {
		name     string
		rules    []WatchRule
		expected bool
	}{
		{"all disabled", []WatchRule{{Enabled: false}, {Enabled: false}}, true},
		{"one enabled", []WatchRule{{Enabled: false}, {Enabled: true}}, false},
		{"empty", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AllDisabled(tt.rules)
			if got != tt.expected {
				t.Errorf("AllDisabled() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestContainsGlob(t *testing.T) {
	tests := []struct {
		name     string
		patterns []string
		value    string
		expected bool
	}{
		{"exact match", []string{"dev.praxis.deploy.failed"}, "dev.praxis.deploy.failed", true},
		{"wildcard star", []string{"*"}, "anything", true},
		{"prefix glob", []string{"dev.praxis.*"}, "dev.praxis.deploy.failed", true},
		{"prefix glob no match", []string{"dev.praxis.deploy.*"}, "dev.praxis.drift.detected", false},
		{"no match", []string{"a", "b"}, "c", false},
		{"empty patterns", nil, "x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containsGlob(tt.patterns, tt.value)
			if got != tt.expected {
				t.Errorf("containsGlob(%v, %q) = %v, want %v", tt.patterns, tt.value, got, tt.expected)
			}
		})
	}
}

func TestMatchesRule(t *testing.T) {
	rule := WatchRule{
		ID:      "test",
		Enabled: true,
		Filter: WatchFilter{
			Types:      []string{"dev.praxis.deployment."},
			Workspaces: []string{"production"},
		},
	}

	event := CloudEventEnvelope{
		Type:       "dev.praxis.deployment.failed",
		Extensions: map[string]string{"workspace": "production"},
	}
	if !matchesRule(rule, event) {
		t.Error("expected rule to match event")
	}

	event2 := CloudEventEnvelope{
		Type:       "dev.praxis.drift.detected",
		Extensions: map[string]string{"workspace": "production"},
	}
	if matchesRule(rule, event2) {
		t.Error("expected rule to not match drift event")
	}

	event3 := CloudEventEnvelope{
		Type:       "dev.praxis.deployment.failed",
		Extensions: map[string]string{"workspace": "staging"},
	}
	if matchesRule(rule, event3) {
		t.Error("expected rule to not match wrong workspace")
	}

	disabledRule := rule
	disabledRule.Enabled = false
	if matchesRule(disabledRule, event) {
		t.Error("disabled rule should not match")
	}
}

func TestMatchesRule_EmptyFilter(t *testing.T) {
	rule := WatchRule{
		ID:      "all",
		Enabled: true,
		Filter:  WatchFilter{},
	}
	event := CloudEventEnvelope{
		Type:       "anything",
		Extensions: map[string]string{},
	}
	if !matchesRule(rule, event) {
		t.Error("rule with empty filter should match everything")
	}
}

func TestSlackWatchConfig_AddAndList(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackWatchConfig{}),
		restate.Reflect(stubSinkConfig{}),
	)
	client := env.Ingress()

	// Add a watch
	rule, err := ingress.Object[AddWatchRequest, WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "AddWatch",
	).Request(t.Context(), AddWatchRequest{
		Name:    "prod-failures",
		Channel: "#alerts",
		Filter: WatchFilter{
			Types:      []string{"dev.praxis.deployment.failed"},
			Workspaces: []string{"production"},
		},
	})
	if err != nil {
		t.Fatalf("AddWatch: %v", err)
	}
	if rule.ID == "" {
		t.Error("expected non-empty rule ID")
	}
	if rule.Name != "prod-failures" {
		t.Errorf("expected name 'prod-failures', got %q", rule.Name)
	}
	if !rule.Enabled {
		t.Error("new rule should be enabled by default")
	}

	// List watches
	rules, err := ingress.Object[restate.Void, []WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "ListWatches",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("ListWatches: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ID != rule.ID {
		t.Errorf("expected rule ID %q, got %q", rule.ID, rules[0].ID)
	}
}

func TestSlackWatchConfig_UpdateAndRemove(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackWatchConfig{}),
		restate.Reflect(stubSinkConfig{}),
	)
	client := env.Ingress()

	// Add
	rule, err := ingress.Object[AddWatchRequest, WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "AddWatch",
	).Request(t.Context(), AddWatchRequest{
		Name: "test-watch",
		Filter: WatchFilter{
			Types: []string{"dev.praxis.deployment.failed"},
		},
	})
	if err != nil {
		t.Fatalf("AddWatch: %v", err)
	}

	// Update — disable
	disabled := false
	updatedName := "updated-watch"
	updated, err := ingress.Object[UpdateWatchRequest, WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "UpdateWatch",
	).Request(t.Context(), UpdateWatchRequest{
		ID:      rule.ID,
		Name:    &updatedName,
		Enabled: &disabled,
	})
	if err != nil {
		t.Fatalf("UpdateWatch: %v", err)
	}
	if updated.Name != "updated-watch" {
		t.Errorf("expected updated name, got %q", updated.Name)
	}
	if updated.Enabled {
		t.Error("expected watch to be disabled")
	}

	// Remove
	_, err = ingress.Object[RemoveWatchRequest, restate.Void](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "RemoveWatch",
	).Request(t.Context(), RemoveWatchRequest{ID: rule.ID})
	if err != nil {
		t.Fatalf("RemoveWatch: %v", err)
	}

	// List should be empty
	rules, err := ingress.Object[restate.Void, []WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "ListWatches",
	).Request(t.Context(), restate.Void{})
	if err != nil {
		t.Fatalf("ListWatches: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected 0 rules after remove, got %d", len(rules))
	}
}

func TestSlackWatchConfig_AddWatch_RequiresName(t *testing.T) {
	env := restatetest.Start(t,
		restate.Reflect(SlackWatchConfig{}),
		restate.Reflect(stubSinkConfig{}),
	)
	client := env.Ingress()

	_, err := ingress.Object[AddWatchRequest, WatchRule](
		client, SlackWatchConfigServiceName, SlackWatchConfigGlobalKey, "AddWatch",
	).Request(t.Context(), AddWatchRequest{
		Filter: WatchFilter{Types: []string{"a"}},
	})
	if err == nil {
		t.Error("expected error for missing name")
	}
}
