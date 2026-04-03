package slack

import (
	"fmt"
	"time"

	restate "github.com/restatedev/sdk-go"
)

// SlackWatchConfig is a Restate Virtual Object keyed by "global".
type SlackWatchConfig struct{}

func (SlackWatchConfig) ServiceName() string { return SlackWatchConfigServiceName }

// AddWatch adds a new event-watch rule and syncs the notification sink.
func (SlackWatchConfig) AddWatch(ctx restate.ObjectContext, req AddWatchRequest) (WatchRule, error) {
	if req.Name == "" {
		return WatchRule{}, restate.TerminalError(fmt.Errorf("watch name is required"), 400)
	}

	state, err := loadWatchState(ctx)
	if err != nil {
		return WatchRule{}, err
	}

	now, err := restate.Run(ctx, func(rc restate.RunContext) (string, error) {
		return time.Now().UTC().Format(time.RFC3339), nil
	})
	if err != nil {
		return WatchRule{}, err
	}

	id := restate.UUID(ctx).String()

	rule := WatchRule{
		ID:        id,
		Name:      req.Name,
		Channel:   req.Channel,
		Filter:    req.Filter,
		CreatedBy: req.CreatedBy,
		CreatedAt: now,
		Enabled:   true,
	}

	state.Rules = append(state.Rules, rule)
	if err := saveWatchState(ctx, state); err != nil {
		return WatchRule{}, err
	}

	syncSink(ctx, state)
	return rule, nil
}

// RemoveWatch removes a watch rule by ID and syncs the notification sink.
func (SlackWatchConfig) RemoveWatch(ctx restate.ObjectContext, req RemoveWatchRequest) error {
	if req.ID == "" {
		return restate.TerminalError(fmt.Errorf("watch ID is required"), 400)
	}

	state, err := loadWatchState(ctx)
	if err != nil {
		return err
	}

	found := false
	filtered := state.Rules[:0]
	for _, r := range state.Rules {
		if r.ID == req.ID {
			found = true
			continue
		}
		filtered = append(filtered, r)
	}
	if !found {
		return restate.TerminalError(fmt.Errorf("watch %q not found", req.ID), 404)
	}

	state.Rules = filtered
	if err := saveWatchState(ctx, state); err != nil {
		return err
	}

	syncSink(ctx, state)
	return nil
}

// UpdateWatch updates an existing watch rule and syncs the notification sink.
func (SlackWatchConfig) UpdateWatch(ctx restate.ObjectContext, req UpdateWatchRequest) (WatchRule, error) {
	if req.ID == "" {
		return WatchRule{}, restate.TerminalError(fmt.Errorf("watch ID is required"), 400)
	}

	state, err := loadWatchState(ctx)
	if err != nil {
		return WatchRule{}, err
	}

	idx := -1
	for i, r := range state.Rules {
		if r.ID == req.ID {
			idx = i
			break
		}
	}
	if idx == -1 {
		return WatchRule{}, restate.TerminalError(fmt.Errorf("watch %q not found", req.ID), 404)
	}

	rule := &state.Rules[idx]
	if req.Name != nil {
		rule.Name = *req.Name
	}
	if req.Channel != nil {
		rule.Channel = *req.Channel
	}
	if req.Filter != nil {
		rule.Filter = *req.Filter
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}

	if err := saveWatchState(ctx, state); err != nil {
		return WatchRule{}, err
	}

	syncSink(ctx, state)
	return *rule, nil
}

// ListWatches returns all active watch rules.
func (SlackWatchConfig) ListWatches(ctx restate.ObjectSharedContext) ([]WatchRule, error) {
	state, err := loadWatchStateShared(ctx)
	if err != nil {
		return nil, err
	}
	return state.Rules, nil
}

// GetWatch returns a specific watch rule by ID.
func (SlackWatchConfig) GetWatch(ctx restate.ObjectSharedContext, id string) (*WatchRule, error) {
	state, err := loadWatchStateShared(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range state.Rules {
		if r.ID == id {
			return &r, nil
		}
	}
	return nil, nil
}

func loadWatchState(ctx restate.ObjectContext) (WatchState, error) {
	ptr, err := restate.Get[*WatchState](ctx, "state")
	if err != nil {
		return WatchState{}, err
	}
	if ptr == nil {
		return WatchState{SinkName: "slack-gateway"}, nil
	}
	return *ptr, nil
}

func loadWatchStateShared(ctx restate.ObjectSharedContext) (WatchState, error) {
	ptr, err := restate.Get[*WatchState](ctx, "state")
	if err != nil {
		return WatchState{}, err
	}
	if ptr == nil {
		return WatchState{SinkName: "slack-gateway"}, nil
	}
	return *ptr, nil
}

func saveWatchState(ctx restate.ObjectContext, state WatchState) error {
	restate.Set(ctx, "state", state)
	return nil
}

// syncSink merges all active watch filters and registers/updates the notification sink.
func syncSink(ctx restate.ObjectContext, state WatchState) {
	if len(state.Rules) == 0 || AllDisabled(state.Rules) {
		if state.SinkName != "" {
			restate.ObjectSend(ctx, "NotificationSinkConfig", "global", "Remove").
				Send(state.SinkName)
		}
		return
	}

	merged := MergeFilters(state.Rules)
	restate.ObjectSend(ctx, "NotificationSinkConfig", "global", "Upsert").
		Send(SinkRegistration{
			Name:    "slack-gateway",
			Type:    "restate_rpc",
			Target:  SlackEventReceiverServiceName,
			Handler: "Receive",
			Filter:  merged,
		})
}

// SinkRegistration represents a sink to register with NotificationSinkConfig.
type SinkRegistration struct {
	Name    string     `json:"name"`
	Type    string     `json:"type"`
	Target  string     `json:"target,omitempty"`
	Handler string     `json:"handler,omitempty"`
	Filter  SinkFilter `json:"filter"`
}

func MergeFilters(rules []WatchRule) SinkFilter {
	seen := struct {
		types, categories, severities, workspaces, deployments map[string]bool
	}{
		make(map[string]bool), make(map[string]bool), make(map[string]bool),
		make(map[string]bool), make(map[string]bool),
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		for _, v := range r.Filter.Types {
			seen.types[v] = true
		}
		for _, v := range r.Filter.Categories {
			seen.categories[v] = true
		}
		for _, v := range r.Filter.Severities {
			seen.severities[v] = true
		}
		for _, v := range r.Filter.Workspaces {
			seen.workspaces[v] = true
		}
		for _, v := range r.Filter.Deployments {
			seen.deployments[v] = true
		}
	}

	return SinkFilter{
		Types:       keys(seen.types),
		Categories:  keys(seen.categories),
		Severities:  keys(seen.severities),
		Workspaces:  keys(seen.workspaces),
		Deployments: keys(seen.deployments),
	}
}

func keys(m map[string]bool) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// matchesRule checks if a CloudEventEnvelope matches a WatchRule filter.
func matchesRule(rule WatchRule, event CloudEventEnvelope) bool {
	if !rule.Enabled {
		return false
	}
	f := rule.Filter
	if len(f.Types) > 0 && !containsPrefix(f.Types, event.Type) {
		return false
	}
	if len(f.Categories) > 0 && !contains(f.Categories, event.Extensions["category"]) {
		return false
	}
	if len(f.Severities) > 0 && !contains(f.Severities, event.Extensions["severity"]) {
		return false
	}
	if len(f.Workspaces) > 0 && !contains(f.Workspaces, event.Extensions["workspace"]) {
		return false
	}
	if len(f.Deployments) > 0 && !containsGlob(f.Deployments, event.Extensions["deployment"]) {
		return false
	}
	return true
}

// matchAllRules returns all rules that match the event.
func matchAllRules(rules []WatchRule, event CloudEventEnvelope) []WatchRule {
	var matched []WatchRule
	for _, r := range rules {
		if matchesRule(r, event) {
			matched = append(matched, r)
		}
	}
	return matched
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func containsPrefix(prefixes []string, value string) bool {
	for _, p := range prefixes {
		if len(value) >= len(p) && value[:len(p)] == p {
			return true
		}
	}
	return false
}

func containsGlob(patterns []string, value string) bool {
	for _, p := range patterns {
		if p == "*" || p == value {
			return true
		}
		if len(p) > 0 && p[len(p)-1] == '*' && len(value) >= len(p)-1 && value[:len(p)-1] == p[:len(p)-1] {
			return true
		}
	}
	return false
}

// AllDisabled returns true if all rules are disabled.
func AllDisabled(rules []WatchRule) bool {
	for _, r := range rules {
		if r.Enabled {
			return false
		}
	}
	return true
}
