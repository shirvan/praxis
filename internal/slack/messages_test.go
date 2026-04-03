package slack

import (
	"testing"
)

func TestConvertMarkdown(t *testing.T) {
	tests := []struct {
		name, input, expected string
	}{
		{"bold", "**hello**", "*hello*"},
		{"no change", "plain text", "plain text"},
		{"multiple bold", "**a** and **b**", "*a* and *b*"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertMarkdown(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestTruncateID(t *testing.T) {
	if truncateID("abc") != "abc" {
		t.Error("short ID should pass through")
	}
	if truncateID("12345678") != "12345678" {
		t.Error("8-char ID should pass through")
	}
	if truncateID("123456789abcdef") != "12345678" {
		t.Error("long ID should be truncated to 8")
	}
}

func TestEventTypeEmoji(t *testing.T) {
	if eventTypeEmoji("dev.praxis.deployment.failed") != ":red_circle:" {
		t.Error("failed should get :red_circle:")
	}
	if eventTypeEmoji("dev.praxis.deployment.completed") != ":white_check_mark:" {
		t.Error("completed should get check mark")
	}
	if eventTypeEmoji("dev.praxis.drift.detected") != ":warning:" {
		t.Error("drift should get warning")
	}
	if eventTypeEmoji("dev.praxis.other") != ":information_source:" {
		t.Error("unknown should get info")
	}
}

func TestEventTypeTitle(t *testing.T) {
	got := eventTypeTitle("dev.praxis.deployment.failed")
	if got != "Deployment Failed" {
		t.Errorf("got %q", got)
	}
}

func TestIsUserAllowed(t *testing.T) {
	if !isUserAllowed("U1", nil) {
		t.Error("nil list should allow all")
	}
	if !isUserAllowed("U1", []string{}) {
		t.Error("empty list should allow all")
	}
	if !isUserAllowed("U1", []string{"U1", "U2"}) {
		t.Error("user in list should be allowed")
	}
	if isUserAllowed("U3", []string{"U1", "U2"}) {
		t.Error("user not in list should be denied")
	}
}

func TestFormatResponse(t *testing.T) {
	blocks := formatResponse(AskResponse{
		Response: "Hello", SessionID: "sess-123", TurnCount: 1,
	})
	if len(blocks) < 2 {
		t.Fatalf("got %d blocks", len(blocks))
	}
}

func TestFormatApproval(t *testing.T) {
	blocks := formatApproval(&ApprovalInfo{
		AwakeableID: "awk-1", Action: "delete",
		Description: "desc", RequestedAt: "now",
	})
	if len(blocks) < 2 {
		t.Fatalf("got %d blocks", len(blocks))
	}
}

func TestFormatEventSummary(t *testing.T) {
	s := formatEventSummary(CloudEventEnvelope{
		Type: "dev.praxis.deployment.failed", Subject: "prod/stack",
	})
	if s == "" {
		t.Error("empty summary")
	}
}
