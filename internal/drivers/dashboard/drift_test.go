package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHasDrift_WhitespaceOnlyJSON_NoDrift(t *testing.T) {
	desired := DashboardSpec{DashboardBody: `{"widgets":[{"type":"text","properties":{"markdown":"hi"}}]}`}
	observed := ObservedState{DashboardBody: "{\n  \"widgets\": [ { \"properties\": { \"markdown\": \"hi\" }, \"type\": \"text\" } ]\n}"}
	assert.False(t, HasDrift(desired, observed))
}

func TestHasDrift_BodyChanged(t *testing.T) {
	desired := DashboardSpec{DashboardBody: `{"widgets":[{"type":"text","properties":{"markdown":"hi"}}]}`}
	observed := ObservedState{DashboardBody: `{"widgets":[{"type":"text","properties":{"markdown":"bye"}}]}`}
	assert.True(t, HasDrift(desired, observed))
}

func TestTruncateBody(t *testing.T) {
	assert.Equal(t, "abcdef...", truncateBody("abcdefghi", 6))
}
