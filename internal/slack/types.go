package slack

const (
	SlackGatewayConfigServiceName = "SlackGatewayConfig"
	SlackWatchConfigServiceName   = "SlackWatchConfig"
	SlackThreadStateServiceName   = "SlackThreadState"
	SlackEventReceiverServiceName = "SlackEventReceiver"

	SlackGatewayConfigGlobalKey = "global"
	SlackWatchConfigGlobalKey   = "global"
)

// SlackGatewayConfiguration holds the Slack connection settings.
type SlackGatewayConfiguration struct {
	BotToken     string   `json:"botToken,omitempty"`
	BotTokenRef  string   `json:"botTokenRef,omitempty"`
	AppToken     string   `json:"appToken,omitempty"`
	AppTokenRef  string   `json:"appTokenRef,omitempty"`
	TeamID       string   `json:"teamId"`
	BotUserID    string   `json:"botUserId"`
	EventChannel string   `json:"eventChannel"`
	Workspace    string   `json:"workspace,omitempty"`
	AllowedUsers []string `json:"allowedUsers"`
	Version      int      `json:"version"`
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
type WatchRule struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Channel   string      `json:"channel"`
	Filter    WatchFilter `json:"filter"`
	CreatedBy string      `json:"createdBy"`
	CreatedAt string      `json:"createdAt"`
	Enabled   bool        `json:"enabled"`
}

// WatchFilter determines which Praxis events trigger thread creation.
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
