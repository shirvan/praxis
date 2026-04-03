package slack

import (
	"context"
	"sync"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
)

// ThreadTracker is a read-through cache backed by SlackThreadState Virtual Objects.
// It lives in the Gateway process (not in Restate) to avoid an RPC round-trip on
// every incoming message. When a thread is not found in the local cache, it falls
// back to a Restate RPC lookup and caches the result for subsequent messages.
//
// Thread-safety: all cache access is protected by a sync.RWMutex since the
// Gateway's event loop and the config-version watcher run in separate goroutines.
type ThreadTracker struct {
	mu            sync.RWMutex            // protects the threads map
	threads       map[string]ThreadRecord // key: "channelID:threadTS"
	restateClient *ingress.Client         // for fallback RPC lookups
}

// NewThreadTracker creates a new ThreadTracker backed by the given Restate ingress client.
func NewThreadTracker(rc *ingress.Client) *ThreadTracker {
	return &ThreadTracker{
		threads:       make(map[string]ThreadRecord),
		restateClient: rc,
	}
}

// IsWatchThread checks the in-memory cache first, then falls back to a
// Restate RPC lookup on a cache miss. This is called for every incoming
// threaded message to determine whether the thread is a Praxis-managed
// watch thread (and thus should be routed to the concierge session).
// Returns false for regular user threads that Praxis did not create.
func (t *ThreadTracker) IsWatchThread(ctx context.Context, channelID, threadTS string) bool {
	key := channelID + ":" + threadTS

	t.mu.RLock()
	_, ok := t.threads[key]
	t.mu.RUnlock()
	if ok {
		return true
	}

	record, err := ingress.Object[restate.Void, *ThreadRecord](
		t.restateClient, SlackThreadStateServiceName, key, "GetRecord",
	).Request(ctx, restate.Void{})
	if err != nil || record == nil {
		return false
	}

	t.mu.Lock()
	t.threads[key] = *record
	t.mu.Unlock()
	return true
}

// RecordThread adds a thread record to the in-memory cache.
func (t *ThreadTracker) RecordThread(channelID, threadTS string, record ThreadRecord) {
	key := channelID + ":" + threadTS
	t.mu.Lock()
	t.threads[key] = record
	t.mu.Unlock()
}
