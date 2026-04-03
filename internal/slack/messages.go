package slack

import (
	"fmt"
	"strings"

	slackpkg "github.com/slack-go/slack"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// formatResponse converts an AskResponse into Slack Block Kit blocks.
func formatResponse(resp AskResponse) []slackpkg.Block {
	blocks := []slackpkg.Block{
		slackpkg.NewSectionBlock(
			slackpkg.NewTextBlockObject("mrkdwn", convertMarkdown(resp.Response), false, false),
			nil, nil,
		),
	}

	blocks = append(blocks, slackpkg.NewContextBlock("",
		slackpkg.NewTextBlockObject("mrkdwn",
			fmt.Sprintf("_Turn %d · Session %s_", resp.TurnCount, truncateID(resp.SessionID)),
			false, false,
		),
	))

	return blocks
}

// formatApproval renders a pending approval as an interactive Block Kit message.
func formatApproval(approval *ApprovalInfo) []slackpkg.Block {
	return []slackpkg.Block{
		slackpkg.NewSectionBlock(
			slackpkg.NewTextBlockObject("mrkdwn",
				fmt.Sprintf(":lock: *The concierge wants to perform a destructive action:*\n\n"+
					"*Action:* `%s`\n%s",
					approval.Action, approval.Description),
				false, false,
			),
			nil, nil,
		),
		slackpkg.NewActionBlock("approval_actions",
			slackpkg.NewButtonBlockElement("approve", approval.AwakeableID,
				slackpkg.NewTextBlockObject("plain_text", "Approve", false, false),
			).WithStyle(slackpkg.StyleDanger),
			slackpkg.NewButtonBlockElement("reject", approval.AwakeableID,
				slackpkg.NewTextBlockObject("plain_text", "Reject", false, false),
			),
		),
	}
}

// formatEventSummary returns a mrkdwn string summarizing a CloudEvent.
func formatEventSummary(event CloudEventEnvelope) string {
	var sb strings.Builder
	if event.Subject != "" {
		fmt.Fprintf(&sb, "*Resource:* `%s`\n", event.Subject)
	}
	if event.Time != "" {
		fmt.Fprintf(&sb, "*Time:* %s\n", event.Time)
	}
	return sb.String()
}

// postEventThread creates a new thread in the target channel with the event summary.
func postEventThread(botToken, channel string, event CloudEventEnvelope) (string, error) {
	blocks := []slackpkg.Block{
		slackpkg.NewHeaderBlock(
			slackpkg.NewTextBlockObject("plain_text",
				eventTypeEmoji(event.Type)+" "+eventTypeTitle(event.Type), false, false),
		),
		slackpkg.NewSectionBlock(
			slackpkg.NewTextBlockObject("mrkdwn", formatEventSummary(event), false, false),
			[]*slackpkg.TextBlockObject{
				slackpkg.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("*Deployment:*\n`%s`", event.Extensions["deployment"]), false, false),
				slackpkg.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("*Workspace:*\n`%s`", event.Extensions["workspace"]), false, false),
				slackpkg.NewTextBlockObject("mrkdwn",
					fmt.Sprintf("*Severity:*\n%s", event.Extensions["severity"]), false, false),
			},
			nil,
		),
		slackpkg.NewDividerBlock(),
		slackpkg.NewContextBlock("",
			slackpkg.NewTextBlockObject("mrkdwn",
				":robot_face: _Analyzing... the concierge is investigating this event._", false, false),
		),
	}

	_, ts, err := slackpkg.New(botToken).PostMessage(channel,
		slackpkg.MsgOptionBlocks(blocks...),
		slackpkg.MsgOptionText(eventTypeTitle(event.Type), false),
	)
	return ts, err
}

// postThreadReply posts a text reply to an existing thread.
func postThreadReply(botToken, channel, threadTS, text string) error {
	_, _, err := slackpkg.New(botToken).PostMessage(channel,
		slackpkg.MsgOptionText(text, false),
		slackpkg.MsgOptionTS(threadTS),
	)
	return err
}

// convertMarkdown performs minimal markdown conversion for Slack's mrkdwn format.
func convertMarkdown(md string) string {
	md = strings.ReplaceAll(md, "**", "*")
	return md
}

// truncateID returns a short prefix of a session ID for display.
func truncateID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// eventTypeEmoji returns an emoji for the event type.
func eventTypeEmoji(eventType string) string {
	switch {
	case strings.Contains(eventType, "failed") || strings.Contains(eventType, "error"):
		return ":red_circle:"
	case strings.Contains(eventType, "drift"):
		return ":warning:"
	case strings.Contains(eventType, "completed") || strings.Contains(eventType, "ready"):
		return ":white_check_mark:"
	case strings.Contains(eventType, "started") || strings.Contains(eventType, "submitted"):
		return ":arrow_forward:"
	default:
		return ":information_source:"
	}
}

// eventTypeTitle returns a human-readable title for an event type.
func eventTypeTitle(eventType string) string {
	parts := strings.Split(eventType, ".")
	if len(parts) < 4 {
		return eventType
	}
	category := parts[2]
	action := strings.Join(parts[3:], " ")
	caser := cases.Title(language.English)
	return caser.String(category) + " " + caser.String(action)
}

// isUserAllowed checks if the user is in the configured allow-list.
func isUserAllowed(userID string, allowedUsers []string) bool {
	if len(allowedUsers) == 0 {
		return true
	}
	for _, id := range allowedUsers {
		if id == userID {
			return true
		}
	}
	return false
}

const notAllowedMessage = "Sorry, you don't have access to Praxis. " +
	"Contact your workspace administrator to request access."
