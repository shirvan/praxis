package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/shirvan/praxis/internal/core/provider"
)

// resolveResourceKey silently falls back to scopeRegion for kinds missing from
// kindScopes, so a forgotten entry never errors — it just builds keys with a
// possibly-wrong scope. This guard makes the omission a test failure instead:
// every kind registered in the provider registry must have an explicit
// kindScopes entry whose scope matches the adapter's declared KeyScope, and
// kindScopes must not accumulate entries for kinds that no longer exist.
func TestKindScopes_MatchesProviderRegistry(t *testing.T) {
	adapterScopeToCLI := map[provider.KeyScope]keyScope{
		provider.KeyScopeGlobal: scopeGlobal,
		provider.KeyScopeRegion: scopeRegion,
		provider.KeyScopeCustom: scopeCustom,
	}

	registered := provider.NewRegistry(nil).All()

	for kind, adapter := range registered {
		scope, ok := kindScopes[kind]
		if !ok {
			t.Errorf("kind %q is registered in the provider registry but missing from kindScopes in root.go; resolveResourceKey would silently default it to scopeRegion", kind)
			continue
		}
		want, ok := adapterScopeToCLI[adapter.Scope()]
		if !ok {
			t.Errorf("kind %q declares unknown KeyScope %v", kind, adapter.Scope())
			continue
		}
		assert.Equal(t, want, scope, "kindScopes entry for %q disagrees with the adapter's declared scope", kind)
	}

	for kind := range kindScopes {
		_, ok := registered[kind]
		assert.True(t, ok, "kindScopes lists %q but no adapter with that kind is registered; remove the stale entry or register the adapter", kind)
	}
}
