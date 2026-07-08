// Package health tracks per-feed last-success timestamps for the ingest
// process and serves them via an HTTP health check (Tier 2 resilience
// hardening, 2026-07-08). Before this package existed, the binary had no
// HTTP server at all and nothing recorded fetch/publish success — only
// per-failure slog lines, which is not something an external check (or a
// human) can poll.
package health

import (
	"sync"
	"time"
)

// Feed names used as keys into the registry. Typed constants so a typo'd
// string key can't silently create a phantom feed that never gets
// reported stale.
type Feed string

const (
	FeedAlerts  Feed = "alerts"
	FeedSPCTorn Feed = "spc_tornado"
	FeedSPCHail Feed = "spc_hail"
	FeedSPCWind Feed = "spc_wind"
	FeedPublish Feed = "publish"
)

// Registry is a threadsafe record of each feed's last-success timestamp.
// One process-wide instance is shared between the poller (writer) and the
// health HTTP handler (reader, concurrent with the next cycle).
type Registry struct {
	mu   sync.RWMutex
	last map[Feed]time.Time
}

// NewRegistry constructs an empty Registry. Every feed starts with no
// recorded success; LastSuccess reports ok=false for those rather than a
// zero time.Time a caller might mistake for a real (very stale) value.
func NewRegistry() *Registry {
	return &Registry{last: make(map[Feed]time.Time)}
}

// RecordSuccess marks feed as having succeeded at time t. Nil-receiver
// safe: a nil *Registry no-ops, so a Config built without a Health field
// (existing/future unit tests) never nil-panics.
func (r *Registry) RecordSuccess(feed Feed, t time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[feed] = t
}

// LastSuccess returns the last recorded success time for feed and whether
// one has ever been recorded. Nil-receiver safe (see RecordSuccess).
func (r *Registry) LastSuccess(feed Feed) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.last[feed]
	return t, ok
}

// RequiredFeeds returns the feeds a process running with the given
// ingest/publish capabilities must keep fresh for /healthz to report
// healthy. An ingest-only node never publishes, so FeedPublish would be
// permanently stale on it and must not be checked; symmetrically a
// publish-only node never fetches NWS/SPC. Takes plain booleans (not a
// poller.Mode) so this package has no import dependency on
// internal/poller (which imports internal/health, not the reverse).
func RequiredFeeds(shouldIngest, shouldPublish bool) []Feed {
	var feeds []Feed
	if shouldIngest {
		feeds = append(feeds, FeedAlerts, FeedSPCTorn, FeedSPCHail, FeedSPCWind)
	}
	if shouldPublish {
		feeds = append(feeds, FeedPublish)
	}
	return feeds
}
