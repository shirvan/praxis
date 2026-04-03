package concierge

// trimHistory removes the oldest non-system messages when the conversation
// exceeds the configured MaxMessages limit. This implements a sliding window
// over the conversation: the system prompt is always preserved (it must remain
// first for the LLM to maintain its persona), and the most recent messages
// are kept up to the limit.
//
// This is critical for long-running sessions where the conversation history
// could grow unbounded, eventually exceeding the LLM's context window.
// The trimmed history is written back to Restate's KV store on each turn.
func trimHistory(msgs []Message, cfg ConciergeConfiguration) []Message {
	max := cfg.MaxMessages
	if max <= 0 {
		max = 200
	}
	if len(msgs) <= max {
		return msgs
	}

	// Preserve the system prompt (first message if role == "system") and
	// keep the most recent messages up to the limit.
	var system *Message
	start := 0
	if len(msgs) > 0 && msgs[0].Role == "system" {
		system = &msgs[0]
		start = 1
	}

	rest := msgs[start:]
	keep := max
	if system != nil {
		keep = max - 1
	}
	if keep < 1 {
		keep = 1
	}
	if len(rest) > keep {
		rest = rest[len(rest)-keep:]
	}

	if system != nil {
		return append([]Message{*system}, rest...)
	}
	return rest
}
