// Package slack implements the Slack integration gateway for Praxis.
//
// The package contains four Restate services and one external process:
//
//   - SlackGatewayConfig (Virtual Object, key="global"): stores Slack
//     connection credentials, bot user ID, and the allow-list of users.
//   - SlackWatchConfig (Virtual Object, key="global"): manages event-watch
//     rules that determine which Praxis events trigger Slack threads.
//   - SlackThreadState (Virtual Object, key=dedupeKey or channelID:threadTS):
//     persists thread metadata for deduplication and reverse lookup.
//   - SlackEventReceiver (stateless Service): receives CloudEvents from the
//     SinkRouter and creates threads + analysis replies.
//   - Gateway (external process): the Socket Mode WebSocket client that
//     bridges DM and thread messages to the ConciergeSession.
package slack

const (
	// SlackGatewayConfigServiceName is the Restate Virtual Object name for
	// the Slack connection configuration store.
	SlackGatewayConfigServiceName = "SlackGatewayConfig"

	// SlackWatchConfigServiceName is the Restate Virtual Object name for
	// the event-watch rule manager.
	SlackWatchConfigServiceName = "SlackWatchConfig"

	// SlackThreadStateServiceName is the Restate Virtual Object name for
	// thread persistence and deduplication.
	SlackThreadStateServiceName = "SlackThreadState"

	// SlackEventReceiverServiceName is the Restate stateless service name
	// for the CloudEvent receiver.
	SlackEventReceiverServiceName = "SlackEventReceiver"

	// SlackGatewayConfigGlobalKey is the single Virtual Object key used
	// for the global gateway configuration instance.
	SlackGatewayConfigGlobalKey = "global"

	// SlackWatchConfigGlobalKey is the single Virtual Object key used
	// for the global watch configuration instance.
	SlackWatchConfigGlobalKey = "global"
)

// SlackGatewayConfiguration holds the Slack connection settings.
// Stored as Restate Virtual Object state under the "config" key.
// Tokens can be provided as literal values (for dev/test) or as SSM
// parameter references (for production) via the Ref variants.
type SlackGatewayConfiguration struct {
	// BotToken is the literal Slack bot token (xoxb-...). Mutually exclusive
	// with BotTokenRef. Redacted in Get responses.
	BotToken string `json:"botToken,omitempty"`

	// BotTokenRef is an SSM parameter path for the bot token. Used in
	// production where tokens should not be stored in Restate state.
	BotTokenRef string `json:"botTokenRef,omitempty"`

	// AppToken is the literal Slack app-level token (xapp-...). Required
	// for Socket Mode. Mutually exclusive with AppTokenRef.
	AppToken string `json:"appToken,omitempty"`

	// AppTokenRef is an SSM parameter path for the app token.
	AppTokenRef string `json:"appTokenRef,omitempty"`

	// TeamID is the Slack workspace (team) identifier.
	TeamID string `json:"teamId"`

	// BotUserID is the Slack user ID of the bot, used to filter out
	// the bot's own messages and avoid self-reply loops.
	BotUserID string `json:"botUserId"`

	// EventChannel is the default Slack channel for event threads when
	// a watch rule does not specify a channel override.
	EventChannel string `json:"eventChannel"`

	// Workspace is the Praxis workspace namespace passed to the concierge
	// for scoping resource access in Slack-initiated requests.
	Workspace string `json:"workspace,omitempty"`

	// AllowedUsers is the Slack user ID allow-list. When non-empty, only
	// these users can interact with the bot. Empty means open access.
	AllowedUsers []string `json:"allowedUsers"`

	// Version is a monotonically increasing counter bumped on every config
	// change. The gateway polls this to detect config changes and reconnect.
	Version int `json:"version"`
}

// Redacted returns a copy with literal tokens masked.
func (c SlackGatewayConfiguration) Redacted() SlackGatewayConfiguration {
	if c.BotToken != "" {
		c.BotToken = "***"
	}
	if c.AppToken != "" {
		c.AppToken = "***"
	}
	return c
}

// SlackConfigRequest is the input to SlackGatewayConfig.Configure.
type SlackConfigRequest struct {
	BotToken     string   `json:"botToken,omitempty"`
	BotTokenRef  string   `json:"botTokenRef,omitempty"`
	AppToken     string   `json:"appToken,omitempty"`
	AppTokenRef  string   `json:"appTokenRef,omitempty"`
	TeamID       string   `json:"teamId,omitempty"`
	BotUserID    string   `json:"botUserId,omitempty"`
	EventChannel string   `json:"eventChannel,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
	AllowedUsers []string `json:"allowedUsers,omitempty"`
}

// SetAllowedUsersRequest replaces the allowed-user list.
type SetAllowedUsersRequest struct {
	UserIDs []string `json:"userIds"`
}

// WatchRule defines a single event-watch subscription.
// Each rule specifies a filter for matching CloudEvents and an optional
// channel override. When an event matches, the SlackEventReceiver creates
// a new thread in the target channel with an automated analysis.
type WatchRule struct {
	// ID is a UUID assigned at creation time via restate.UUID for uniqueness.
	ID string `json:"id"`

	// Name is a human-readable label for display in `praxis slack watch list`.
	Name string `json:"name"`

	// Channel overrides the default EventChannel for this rule's threads.
	// Empty means use the gateway's default EventChannel.
	Channel string `json:"channel"`

	// Filter determines which CloudEvents match this rule.
	Filter WatchFilter `json:"filter"`

	// CreatedBy is the Slack user ID of the rule creator, for audit purposes.
	CreatedBy string `json:"createdBy"`

	// CreatedAt is the RFC 3339 timestamp when the rule was created.
	CreatedAt string `json:"createdAt"`

	// Enabled controls whether the rule is active. Disabled rules are
	// retained but excluded from event matching and sink filter merging.
	Enabled bool `json:"enabled"`
}

// WatchFilter determines which Praxis events trigger thread creation.
// All fields are optional; empty fields match everything. When multiple
// fields are set, they are ANDed together (all must match). Within a
// field's list, values are ORed (any value can match).
type WatchFilter struct {
	Types       []string `json:"types,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Severities  []string `json:"severities,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
	Deployments []string `json:"deployments,omitempty"`
}

// WatchState is the full set of watches stored in the Virtual Object.
type WatchState struct {
	Rules    []WatchRule `json:"rules"`
	SinkName string      `json:"sinkName"`
}

// AddWatchRequest is the input to SlackWatchConfig.AddWatch.
type AddWatchRequest struct {
	Name      string      `json:"name"`
	Channel   string      `json:"channel,omitempty"`
	Filter    WatchFilter `json:"filter"`
	CreatedBy string      `json:"createdBy,omitempty"`
}

// RemoveWatchRequest is the input to SlackWatchConfig.RemoveWatch.
type RemoveWatchRequest struct {
	ID string `json:"id"`
}

// UpdateWatchRequest is the input to SlackWatchConfig.UpdateWatch.
type UpdateWatchRequest struct {
	ID      string       `json:"id"`
	Name    *string      `json:"name,omitempty"`
	Channel *string      `json:"channel,omitempty"`
	Filter  *WatchFilter `json:"filter,omitempty"`
	Enabled *bool        `json:"enabled,omitempty"`
}

// ThreadRecord holds all metadata for a Praxis-initiated Slack thread.
// Persisted by SlackThreadState for deduplication (ensuring the same event
// doesn't create duplicate threads) and for reverse lookup (mapping a
// channel+threadTS back to the concierge session key for message routing).
type ThreadRecord struct {
	ChannelID   string `json:"channelId"`
	ThreadTS    string `json:"threadTs"`
	SessionKey  string `json:"sessionKey"`
	WatchRuleID string `json:"watchRuleId"`
	EventID     string `json:"eventId"`
	EventType   string `json:"eventType"`
	CreatedAt   string `json:"createdAt"`
}

// CloudEventEnvelope is a simplified event structure received from the SinkRouter.
// The SinkRouter serializes CloudEvents into this envelope before sending them
// to the SlackEventReceiver. The Extensions map carries Praxis-specific metadata
// (deployment, workspace, severity, category) as CloudEvent extension attributes.
type CloudEventEnvelope struct {
	ID         string            `json:"id"`
	Source     string            `json:"source"`
	Type       string            `json:"type"`
	Subject    string            `json:"subject"`
	Time       string            `json:"time"`
	DataJSON   []byte            `json:"dataJson"`
	Extensions map[string]string `json:"extensions"`
}

// AnalyzeAndReplyRequest contains everything needed to ask the concierge
// and post the analysis as a thread reply.
type AnalyzeAndReplyRequest struct {
	SessionKey string `json:"sessionKey"`
	Prompt     string `json:"prompt"`
	Workspace  string `json:"workspace"`
	ChannelID  string `json:"channelId"`
	ThreadTS   string `json:"threadTs"`
}

// AskRequest mirrors the concierge AskRequest for Slack-originated prompts.
type AskRequest struct {
	Prompt    string `json:"prompt"`
	Workspace string `json:"workspace,omitempty"`
	Source    string `json:"source,omitempty"`
}

// AskResponse mirrors the concierge AskResponse.
type AskResponse struct {
	Response  string `json:"response"`
	SessionID string `json:"sessionId"`
	TurnCount int    `json:"turnCount"`
}

// SessionStatus mirrors the concierge SessionStatus.
type SessionStatus struct {
	PendingApproval *ApprovalInfo `json:"pendingApproval,omitempty"`
}

// ApprovalInfo mirrors the concierge ApprovalInfo.
type ApprovalInfo struct {
	AwakeableID string `json:"awakeableId"`
	Action      string `json:"action"`
	Description string `json:"description"`
	RequestedAt string `json:"requestedAt"`
}

// ApprovalRelayRequest mirrors the concierge ApprovalRelayRequest.
type ApprovalRelayRequest struct {
	AwakeableID string `json:"awakeableId"`
	Approved    bool   `json:"approved"`
	Reason      string `json:"reason,omitempty"`
	Actor       string `json:"actor,omitempty"`
}

// SinkFilter matches the orchestrator's notification SinkFilter JSON contract.
type SinkFilter struct {
	Types       []string `json:"types,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	Severities  []string `json:"severities,omitempty"`
	Workspaces  []string `json:"workspaces,omitempty"`
	Deployments []string `json:"deployments,omitempty"`
}
