package cli

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/core/provider"
)

// GetResourceInputs masks secrets using the CLI-side sensitiveInputPaths map,
// while plan-output masking is driven by the adapters' declared
// SensitiveFields. A drifted entry means a sensitive driver leaks plaintext
// through `praxis get --inputs` / `-o json` — exactly the bug class the
// masking exists to prevent. This guard makes any divergence a test failure:
// every adapter declaring SensitiveFields must have a matching
// sensitiveInputPaths entry (with the "spec." prefix stripped, since GetInputs
// returns the bare spec object), and the CLI map must not carry entries for
// kinds that no longer declare sensitive fields.
func TestSensitiveInputPaths_MatchesProviderRegistry(t *testing.T) {
	type sensitiveDeclarer interface{ SensitiveFields() []string }

	declared := map[string][]string{}
	for kind, adapter := range provider.NewRegistry(nil).All() {
		sd, ok := adapter.(sensitiveDeclarer)
		if !ok {
			continue
		}
		fields := sd.SensitiveFields()
		if len(fields) == 0 {
			continue
		}
		stripped := make([]string, 0, len(fields))
		for _, f := range fields {
			stripped = append(stripped, strings.TrimPrefix(f, "spec."))
		}
		declared[kind] = stripped
	}

	assert.NotEmpty(t, declared, "expected at least one adapter to declare SensitiveFields; if the mechanism moved, update this guard")

	for kind, wantPaths := range declared {
		gotPaths, ok := sensitiveInputPaths[kind]
		if !ok {
			t.Errorf("kind %q declares SensitiveFields %v in its adapter descriptor but has no sensitiveInputPaths entry in client.go; `praxis get --inputs` would print its secrets in plaintext", kind, wantPaths)
			continue
		}
		assert.ElementsMatch(t, wantPaths, gotPaths, "sensitiveInputPaths entry for %q disagrees with the adapter's SensitiveFields", kind)
	}

	for kind := range sensitiveInputPaths {
		_, ok := declared[kind]
		assert.True(t, ok, "sensitiveInputPaths lists %q but its adapter declares no SensitiveFields; remove the stale entry or declare the fields on the descriptor", kind)
	}
}
