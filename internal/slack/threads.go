package slack

import (
	"context"
	"sync"

	restate "github.com/restatedev/sdk-go"
	"github.com/restatedev/sdk-go/ingress"
)

// ThreadTracker is a read-through cache backed by SlackThreadState Virtual Objects.
type ThreadTracker struct {
	mu            sync.RWMutex
	threads       map[string]ThreadRecord
	restateClient *ingress.Client
}

// NewThreadTracker creates a new ThreadTracker backed by the given Restate ingress client.
func NewThreadTracker(rc *ingress.Client) *ThreadTracker {
	return &ThreadTracker{
		threads:       make(map[string]ThreadRecord),
		restateClient: rc,
	}
}

// IsWatchThread checks the in-memory cache first, then falls back to a
// Restate RPC lookup on a cache miss.
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
