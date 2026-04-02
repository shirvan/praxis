package concierge

import (
"testing"

"github.com/stretchr/testify/assert"
)

func TestTrimHistoryUnderLimit(t *testing.T) {
msgs := []Message{
{Role: "system", Content: "You are a helper"},
{Role: "user", Content: "Hello"},
{Role: "assistant", Content: "Hi there"},
}
cfg := ConciergeConfiguration{MaxMessages: 10}
got := trimHistory(msgs, cfg)
assert.Len(t, got, 3)
}

func TestTrimHistoryPreservesSystem(t *testing.T) {
msgs := []Message{
{Role: "system", Content: "system prompt"},
{Role: "user", Content: "m1"},
{Role: "assistant", Content: "m2"},
{Role: "user", Content: "m3"},
{Role: "assistant", Content: "m4"},
}
cfg := ConciergeConfiguration{MaxMessages: 3}
got := trimHistory(msgs, cfg)

assert.Len(t, got, 3)
assert.Equal(t, "system", got[0].Role)
assert.Equal(t, "system prompt", got[0].Content)
assert.Equal(t, "m3", got[1].Content)
assert.Equal(t, "m4", got[2].Content)
}

func TestTrimHistoryNoSystem(t *testing.T) {
msgs := []Message{
{Role: "user", Content: "m1"},
{Role: "assistant", Content: "m2"},
{Role: "user", Content: "m3"},
}
cfg := ConciergeConfiguration{MaxMessages: 2}
got := trimHistory(msgs, cfg)

assert.Len(t, got, 2)
assert.Equal(t, "m2", got[0].Content)
assert.Equal(t, "m3", got[1].Content)
}

func TestTrimHistoryDefaultLimit(t *testing.T) {
msgs := make([]Message, 300)
for i := range msgs {
msgs[i] = Message{Role: "user", Content: "msg"}
}
cfg := ConciergeConfiguration{MaxMessages: 0}
got := trimHistory(msgs, cfg)

assert.Len(t, got, 200, "default limit is 200")
}
