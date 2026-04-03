package slack

import (
	"fmt"

	restate "github.com/restatedev/sdk-go"
)

// SlackGatewayConfig is a Restate Virtual Object keyed by "global".
// It stores the Slack connection configuration (tokens, bot user ID, allowed
// users) and exposes Configure/Get/SetAllowedUsers/AddAllowedUser/RemoveAllowedUser
// handlers over Restate RPC.
//
// Token storage: tokens can be provided as literal values (for dev/test) or as
// SSM parameter references. When a literal token is set, the corresponding Ref
// field is cleared and vice versa, ensuring exactly one source of truth.
//
// Versioning: every mutation bumps a monotonic Version counter. The external
// Gateway process polls this counter to detect config changes and reconnect
// the Socket Mode WebSocket client without a process restart.
type SlackGatewayConfig struct{}

func (SlackGatewayConfig) ServiceName() string { return SlackGatewayConfigServiceName }

// Configure sets or updates Slack connection credentials and gateway settings.
// Uses a merge-patch strategy: only non-zero fields in the request are applied.
// When a literal token is set, the corresponding Ref is cleared (and vice versa)
// to ensure a single source of truth for each token type.
func (SlackGatewayConfig) Configure(ctx restate.ObjectContext, req SlackConfigRequest) error {
	existing, err := restate.Get[*SlackGatewayConfiguration](ctx, "config")
	if err != nil {
		return err
	}

	var cfg SlackGatewayConfiguration
	if existing != nil {
		cfg = *existing
	}

	if req.BotToken != "" {
		cfg.BotToken = req.BotToken
		cfg.BotTokenRef = ""
	}
	if req.BotTokenRef != "" {
		cfg.BotTokenRef = req.BotTokenRef
		cfg.BotToken = ""
	}
	if req.AppToken != "" {
		cfg.AppToken = req.AppToken
		cfg.AppTokenRef = ""
	}
	if req.AppTokenRef != "" {
		cfg.AppTokenRef = req.AppTokenRef
		cfg.AppToken = ""
	}
	if req.TeamID != "" {
		cfg.TeamID = req.TeamID
	}
	if req.BotUserID != "" {
		cfg.BotUserID = req.BotUserID
	}
	if req.EventChannel != "" {
		cfg.EventChannel = req.EventChannel
	}
	if req.Workspace != "" {
		cfg.Workspace = req.Workspace
	}
	if req.AllowedUsers != nil {
		cfg.AllowedUsers = req.AllowedUsers
	}

	cfg.Version++
	restate.Set(ctx, "config", cfg)
	return nil
}

// Get returns the current configuration with secrets redacted.
// This is a shared handler (ObjectSharedContext) so it can run concurrently
// with Configure without blocking, since it only reads state.
func (SlackGatewayConfig) Get(ctx restate.ObjectSharedContext) (SlackGatewayConfiguration, error) {
	cfgPtr, err := restate.Get[*SlackGatewayConfiguration](ctx, "config")
	if err != nil {
		return SlackGatewayConfiguration{}, err
	}
	if cfgPtr == nil {
		return SlackGatewayConfiguration{}, nil
	}
	return cfgPtr.Redacted(), nil
}

// SetAllowedUsers replaces the allowed-user list.
func (SlackGatewayConfig) SetAllowedUsers(ctx restate.ObjectContext, req SetAllowedUsersRequest) error {
	cfgPtr, err := restate.Get[*SlackGatewayConfiguration](ctx, "config")
	if err != nil {
		return err
	}
	if cfgPtr == nil {
		return restate.TerminalError(fmt.Errorf("slack gateway not configured"), 400)
	}
	cfgPtr.AllowedUsers = req.UserIDs
	cfgPtr.Version++
	restate.Set(ctx, "config", *cfgPtr)
	return nil
}

// AddAllowedUser appends a single Slack user ID to the allow-list.
func (SlackGatewayConfig) AddAllowedUser(ctx restate.ObjectContext, userID string) error {
	cfgPtr, err := restate.Get[*SlackGatewayConfiguration](ctx, "config")
	if err != nil {
		return err
	}
	if cfgPtr == nil {
		return restate.TerminalError(fmt.Errorf("slack gateway not configured"), 400)
	}
	for _, id := range cfgPtr.AllowedUsers {
		if id == userID {
			return nil
		}
	}
	cfgPtr.AllowedUsers = append(cfgPtr.AllowedUsers, userID)
	cfgPtr.Version++
	restate.Set(ctx, "config", *cfgPtr)
	return nil
}

// RemoveAllowedUser removes a single Slack user ID from the allow-list.
func (SlackGatewayConfig) RemoveAllowedUser(ctx restate.ObjectContext, userID string) error {
	cfgPtr, err := restate.Get[*SlackGatewayConfiguration](ctx, "config")
	if err != nil {
		return err
	}
	if cfgPtr == nil {
		return restate.TerminalError(fmt.Errorf("slack gateway not configured"), 400)
	}
	filtered := cfgPtr.AllowedUsers[:0]
	for _, id := range cfgPtr.AllowedUsers {
		if id != userID {
			filtered = append(filtered, id)
		}
	}
	cfgPtr.AllowedUsers = filtered
	cfgPtr.Version++
	restate.Set(ctx, "config", *cfgPtr)
	return nil
}
