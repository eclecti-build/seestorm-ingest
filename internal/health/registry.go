// Package health tracks per-feed last-success timestamps and per-state
// publish-put failure counts for the ingest process, and serves them via
// an HTTP health check (Tier 2 resilience hardening, 2026-07-08).
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

// Registry is a threadsafe record of each feed's last-success timestamp
// AND the current publish cycle's per-state put failure counts. One
// process-wide instance is shared between the poller (writer) and the
// health HTTP handler (reader, concurrent with the next cycle).
type Registry struct {
	mu              sync.RWMutex
	last            map[Feed]time.Time
	publishFailures map[string]int
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		last:            make(map[Feed]time.Time),
		publishFailures: make(map[string]int),
	}
}

// RecordSuccess marks feed as having succeeded at time t. Nil-receiver
// safe: a nil *Registry no-ops.
func (r *Registry) RecordSuccess(feed Feed, t time.Time) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[feed] = t
}

// LastSuccess returns the last recorded success time for feed and whether
// one has ever been recorded. Nil-receiver safe.
func (r *Registry) LastSuccess(feed Feed) (time.Time, bool) {
	if r == nil {
		return time.Time{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.last[feed]
	return t, ok
}

// RecordPublishPutFailure increments the failure counter for a per-state
// publish put that exhausted its retries this cycle. Nil-receiver safe.
func (r *Registry) RecordPublishPutFailure(state string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishFailures[state]++
}

// ResetPublishFailures clears the per-state failure counters. Called by
// the poller at the start of each publish phase so /healthz reflects
// only the most recent cycle's degradation, not a historical total that
// never recovers. Nil-receiver safe.
func (r *Registry) ResetPublishFailures() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishFailures = make(map[string]int)
}

// PublishPutFailures returns a snapshot of state -> failure-count for the
// current cycle. Informational — does not affect /healthz's 200/503
// status. Nil-receiver safe (returns an empty, non-nil map).
func (r *Registry) PublishPutFailures() map[string]int {
	if r == nil {
		return map[string]int{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]int, len(r.publishFailures))
	for k, v := range r.publishFailures {
		out[k] = v
	}
	return out
}

// RequiredFeeds returns the feeds a process running with the given
// ingest/publish capabilities must keep fresh for /healthz to report
// healthy. Takes plain booleans (not a poller.Mode) so this package has
// no import dependency on internal/poller.
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
