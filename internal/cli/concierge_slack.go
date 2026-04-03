package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newConciergeSlackCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "slack",
		Short: "Manage the Slack gateway integration",
	}

	cmd.AddCommand(
		newSlackConfigureCmd(flags),
		newSlackGetConfigCmd(flags),
		newSlackAllowedUsersCmd(flags),
		newSlackWatchCmd(flags),
	)

	return cmd
}

// --------------------------------------------------------------------------
// slack configure
// --------------------------------------------------------------------------

func newSlackConfigureCmd(flags *rootFlags) *cobra.Command {
	var (
		botToken     string
		botTokenRef  string
		appToken     string
		appTokenRef  string
		eventChannel string
		allowedUsers string
	)

	cmd := &cobra.Command{
		Use:   "configure",
		Short: "Configure the Slack gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			req := slackConfigRequest{
				BotToken:     botToken,
				BotTokenRef:  botTokenRef,
				AppToken:     appToken,
				AppTokenRef:  appTokenRef,
				EventChannel: eventChannel,
			}
			if allowedUsers != "" {
				req.AllowedUsers = strings.Split(allowedUsers, ",")
			}

			if err := client.SlackConfigure(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack configure: %w", err)
			}

			r := flags.renderer()
			r.successLine("Slack gateway configured")
			return nil
		},
	}

	cmd.Flags().StringVar(&botToken, "bot-token", "", "Slack bot token (xoxb-...)")
	cmd.Flags().StringVar(&botTokenRef, "bot-token-ref", "", "SSM parameter name for bot token")
	cmd.Flags().StringVar(&appToken, "app-token", "", "Slack app-level token (xapp-...)")
	cmd.Flags().StringVar(&appTokenRef, "app-token-ref", "", "SSM parameter name for app token")
	cmd.Flags().StringVar(&eventChannel, "event-channel", "", "Default channel for event notifications")
	cmd.Flags().StringVar(&allowedUsers, "allowed-users", "", "Comma-separated Slack user IDs")
	return cmd
}

func newSlackGetConfigCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "get-config",
		Short: "Show current Slack gateway configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			cfg, err := client.SlackGetConfig(context.Background())
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack get-config: %w", err)
			}
			return json.NewEncoder(os.Stdout).Encode(cfg)
		},
	}
}

// --------------------------------------------------------------------------
// slack allowed-users
// --------------------------------------------------------------------------

func newSlackAllowedUsersCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "allowed-users",
		Short: "Manage the Slack allowed-user list",
	}

	cmd.AddCommand(
		newSlackAllowedUsersSetCmd(flags),
		newSlackAllowedUsersAddCmd(flags),
		newSlackAllowedUsersRemoveCmd(flags),
		newSlackAllowedUsersListCmd(flags),
	)

	return cmd
}

func newSlackAllowedUsersSetCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "set <user-ids>",
		Short: "Replace the allowed-user list (comma-separated, or empty string to clear)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			var ids []string
			if args[0] != "" {
				ids = strings.Split(args[0], ",")
			}
			req := slackSetAllowedUsersRequest{UserIDs: ids}
			if err := client.SlackSetAllowedUsers(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack allowed-users set: %w", err)
			}
			r := flags.renderer()
			r.successLine("Allowed-user list updated")
			return nil
		},
	}
}

func newSlackAllowedUsersAddCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "add <user-id>",
		Short: "Add a user to the allowed list",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			if err := client.SlackAddAllowedUser(context.Background(), args[0]); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack allowed-users add: %w", err)
			}
			r := flags.renderer()
			r.successLine(fmt.Sprintf("User %s added", args[0]))
			return nil
		},
	}
}

func newSlackAllowedUsersRemoveCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <user-id>",
		Short: "Remove a user from the allowed list",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			if err := client.SlackRemoveAllowedUser(context.Background(), args[0]); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack allowed-users remove: %w", err)
			}
			r := flags.renderer()
			r.successLine(fmt.Sprintf("User %s removed", args[0]))
			return nil
		},
	}
}

func newSlackAllowedUsersListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List allowed users",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			cfg, err := client.SlackGetConfig(context.Background())
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack allowed-users list: %w", err)
			}
			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(cfg.AllowedUsers)
			}
			if len(cfg.AllowedUsers) == 0 {
				fmt.Println("No allowed users configured (all users permitted)")
				return nil
			}
			for _, u := range cfg.AllowedUsers {
				fmt.Println(u)
			}
			return nil
		},
	}
}

// --------------------------------------------------------------------------
// slack watch
// --------------------------------------------------------------------------

func newSlackWatchCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Manage event watch rules",
	}

	cmd.AddCommand(
		newSlackWatchAddCmd(flags),
		newSlackWatchListCmd(flags),
		newSlackWatchRemoveCmd(flags),
		newSlackWatchUpdateCmd(flags),
	)

	return cmd
}

func newSlackWatchAddCmd(flags *rootFlags) *cobra.Command {
	var (
		name        string
		channel     string
		types       string
		categories  string
		severities  string
		workspaces  string
		deployments string
	)

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Add an event watch rule",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			client := flags.newClient()
			req := slackAddWatchRequest{
				Name:    name,
				Channel: channel,
				Filter:  buildWatchFilter(types, categories, severities, workspaces, deployments),
			}
			rule, err := client.SlackAddWatch(context.Background(), req)
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack watch add: %w", err)
			}
			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(rule)
			}
			r := flags.renderer()
			r.successLine(fmt.Sprintf("Watch %q created (id: %s)", rule.Name, rule.ID))
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Watch rule name (required)")
	cmd.Flags().StringVar(&channel, "channel", "", "Slack channel for notifications")
	cmd.Flags().StringVar(&types, "types", "", "Comma-separated event types (supports trailing *)")
	cmd.Flags().StringVar(&categories, "categories", "", "Comma-separated categories")
	cmd.Flags().StringVar(&severities, "severities", "", "Comma-separated severities")
	cmd.Flags().StringVar(&workspaces, "workspaces", "", "Comma-separated workspaces")
	cmd.Flags().StringVar(&deployments, "deployments", "", "Comma-separated deployments")
	return cmd
}

func newSlackWatchListCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List all event watch rules",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := flags.newClient()
			rules, err := client.SlackListWatches(context.Background())
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack watch list: %w", err)
			}
			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(rules)
			}
			if len(rules) == 0 {
				fmt.Println("No watch rules configured")
				return nil
			}
			for _, r := range rules {
				status := "enabled"
				if !r.Enabled {
					status = "disabled"
				}
				fmt.Printf("%-20s %-12s %-10s %s\n", r.Name, r.ID, status, r.Channel)
			}
			return nil
		},
	}
}

func newSlackWatchRemoveCmd(flags *rootFlags) *cobra.Command {
	var id string

	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Remove an event watch rule",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			client := flags.newClient()
			req := slackRemoveWatchRequest{ID: id}
			if err := client.SlackRemoveWatch(context.Background(), req); err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack watch remove: %w", err)
			}
			r := flags.renderer()
			r.successLine(fmt.Sprintf("Watch %s removed", id))
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Watch rule ID (required)")
	return cmd
}

func newSlackWatchUpdateCmd(flags *rootFlags) *cobra.Command {
	var (
		id      string
		enabled string
		name    string
	)

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update an event watch rule",
		RunE: func(cmd *cobra.Command, args []string) error {
			if id == "" {
				return fmt.Errorf("--id is required")
			}
			client := flags.newClient()
			req := slackUpdateWatchRequest{ID: id}
			if name != "" {
				req.Name = &name
			}
			if enabled != "" {
				val := enabled == "true"
				req.Enabled = &val
			}
			rule, err := client.SlackUpdateWatch(context.Background(), req)
			if err != nil {
				if isConciergeUnavailable(err) {
					fmt.Fprint(os.Stderr, conciergeUnavailableMsg)
					return nil
				}
				return fmt.Errorf("slack watch update: %w", err)
			}
			if flags.outputFormat() == OutputJSON {
				return json.NewEncoder(os.Stdout).Encode(rule)
			}
			r := flags.renderer()
			r.successLine(fmt.Sprintf("Watch %s updated", rule.ID))
			return nil
		},
	}

	cmd.Flags().StringVar(&id, "id", "", "Watch rule ID (required)")
	cmd.Flags().StringVar(&enabled, "enabled", "", "Enable or disable (true/false)")
	cmd.Flags().StringVar(&name, "name", "", "New name")
	return cmd
}

func buildWatchFilter(types, categories, severities, workspaces, deployments string) slackWatchFilter {
	var f slackWatchFilter
	if types != "" {
		f.Types = strings.Split(types, ",")
	}
	if categories != "" {
		f.Categories = strings.Split(categories, ",")
	}
	if severities != "" {
		f.Severities = strings.Split(severities, ",")
	}
	if workspaces != "" {
		f.Workspaces = strings.Split(workspaces, ",")
	}
	if deployments != "" {
		f.Deployments = strings.Split(deployments, ",")
	}
	return f
}
