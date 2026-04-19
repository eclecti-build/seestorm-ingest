// Package publisher distributes snapshot JSON to one or more destinations.
// Destinations implement the Publisher interface. Multi composes them so the
// poller can fan out to local disk + R2 without coupling to either.
package publisher

import (
	"context"
	"log/slog"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// SnapshotKey is the canonical object/file name for the merged multi-state
// snapshot. Changing it is a breaking change for the public Worker allowlist.
const SnapshotKey = "active-events.json"

// CacheControlLive is the Cache-Control header value for live snapshots
// (merged AND per-state files). Aligned to the 30s ingest cadence so the
// edge serves at most one R2 GET per PoP per cycle.
//
// MUST match the worker's LIVE_CACHE_CONTROL constant — when these drift,
// R2 declares one freshness contract and the worker declares another, which
// confuses CDN debugging and surfaces as inconsistent caching behavior.
// Centralized here so a future cadence change touches one symbol.
const CacheControlLive = "public, max-age=30, s-maxage=30"

// CacheControlHistory is the Cache-Control header value for archived
// history snapshots (immutable once written, year-long cache).
const CacheControlHistory = "public, max-age=31536000, immutable"

// PerStateKeyPrefix is the R2 prefix (and filesystem subdirectory) for
// per-state snapshots. Choosing a subdirectory rather than a flat suffix
// (`active-events-WI.json`) keeps per-state listing trivially clean:
// `bucket.list({prefix: "active-events/"})` returns ONLY per-state files
// because the merged `active-events.json` lives at the top level under a
// different prefix.
//
// The full per-state key shape is `active-events/{STATE}.json`, where STATE
// is a USPS 2-letter code. See PerStateKey.
const PerStateKeyPrefix = "active-events/"

// PerStateKey returns the R2/file key for the per-state snapshot of the
// given state code, e.g. PerStateKey("WI") == "active-events/WI.json".
// Validates nothing — callers are expected to have already checked the
// state code against nws.IsValidStateCode.
func PerStateKey(state string) string {
	return PerStateKeyPrefix + state + ".json"
}

// SnapshotSchemaVersion is the wire-format version published in every snapshot
// (merged AND per-state). Bump this when the shape changes in a way the
// client must adapt to. The client refuses to render an unrecognized version
// rather than silently mis-rendering.
//
// v2 (2026-04): replaced top-level `area string` with `areas []string` on the
// merged file and added per-alert `states []string`. Introduced per-state
// snapshots at PerStateKey() with `area_state string` (singular).
const SnapshotSchemaVersion = 2

// Snapshot is the merged multi-state CDN-cacheable JSON published after each
// poll cycle. Written to SnapshotKey (`active-events.json`).
//
// GeneratedAtMs is a redundant epoch-ms representation of GeneratedAt. The
// client staleness check (Open Decisions #11 — red banner at 90s) diffs it
// against serverNow() without parsing RFC3339, which is what keeps the
// staleness calculation robust to client-clock skew. Both timestamp fields
// MUST be populated from the same time.Now().UTC() call per cycle — drift
// between them would cause the client to see phantom staleness or mask a
// real stall. The poller wires this in publishSnapshot: one `now`, fanned
// out to the merged Snapshot AND every per-state StateSnapshot so clients
// merging in-memory don't see split-time payloads either.
//
// Old clients that predate the field ignore unknown JSON keys and stay on
// the RFC3339 `generated_at` field, so this is a safe additive change to
// the v2 envelope (no schema_version bump required).
type Snapshot struct {
	SchemaVersion int                        `json:"schema_version"`
	GeneratedAt   time.Time                  `json:"generated_at"`
	GeneratedAtMs int64                      `json:"generated_at_ms"`
	Areas         []string                   `json:"areas"`
	AlertCount    int                        `json:"alert_count"`
	Alerts        []store.ActiveAlertGeoJSON `json:"alerts"`
}

// StateSnapshot is the per-state slice of the merged Snapshot, published
// alongside it at PerStateKey(state) (e.g. `active-events/WI.json`). Clients
// that scope to a subset of states can fetch only the files they care about
// and skip the bulk of the merged payload — this is the primary scaling
// lever for the multi-state architecture.
//
// Shape mirrors Snapshot but with `area_state` (singular: this file's scope)
// in place of `areas` (plural: multi-state coverage). Cross-border alerts
// (e.g. an alert with `states: ["WI","IL"]`) appear in BOTH `WI.json` and
// `IL.json` — natural semantics for an alert whose footprint touches
// multiple states.
//
// GeneratedAtMs mirrors Snapshot.GeneratedAtMs and MUST be populated from the
// same time.Time as the merged Snapshot (and every sibling per-state file) in
// a given poll cycle. Use NewSnapshot + NewStateSnapshot to enforce this.
type StateSnapshot struct {
	SchemaVersion int                        `json:"schema_version"`
	GeneratedAt   time.Time                  `json:"generated_at"`
	GeneratedAtMs int64                      `json:"generated_at_ms"`
	AreaState     string                     `json:"area_state"`
	AlertCount    int                        `json:"alert_count"`
	Alerts        []store.ActiveAlertGeoJSON `json:"alerts"`
}

// NewSnapshot builds a merged Snapshot with both generated_at and
// generated_at_ms derived from a single time.Now().UTC() call. Callers must
// pass the returned Snapshot's GeneratedAt to NewStateSnapshot for every
// per-state sibling so the whole fan-out carries one timestamp.
func NewSnapshot(areas []string, alerts []store.ActiveAlertGeoJSON) Snapshot {
	if alerts == nil {
		alerts = []store.ActiveAlertGeoJSON{}
	}
	now := time.Now().UTC()
	return Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		GeneratedAt:   now,
		GeneratedAtMs: now.UnixMilli(),
		Areas:         areas,
		AlertCount:    len(alerts),
		Alerts:        alerts,
	}
}

// NewStateSnapshot builds a per-state slice that shares the merged snapshot's
// timestamp. Takes `at` explicitly (rather than calling time.Now) so the
// merged file and every per-state sibling carry the exact same
// generated_at / generated_at_ms — the invariant the client's staleness
// check relies on.
func NewStateSnapshot(state string, alerts []store.ActiveAlertGeoJSON, at time.Time) StateSnapshot {
	if alerts == nil {
		alerts = []store.ActiveAlertGeoJSON{}
	}
	at = at.UTC()
	return StateSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		GeneratedAt:   at,
		GeneratedAtMs: at.UnixMilli(),
		AreaState:     state,
		AlertCount:    len(alerts),
		Alerts:        alerts,
	}
}

// Publisher writes snapshots somewhere durable and reachable.
// Implementations log their own success/failure with destination context.
//
// Two methods because the merged and per-state snapshots have different
// shapes AND different lifecycle rules — the merged file is also archived
// to a versioned history key, while per-state files are overwritten in
// place only (history stays unsharded to keep R2 object count bounded).
type Publisher interface {
	Publish(ctx context.Context, snapshot Snapshot) error
	PublishState(ctx context.Context, snapshot StateSnapshot) error
}

// Multi fans out a snapshot to every registered publisher. A failure in one
// destination never stops the others — durability of the local file should
// not hinge on R2 being up, and vice versa.
type Multi struct {
	publishers []Publisher
}

// NewMulti builds a composite publisher.
func NewMulti(publishers ...Publisher) *Multi {
	return &Multi{publishers: publishers}
}

// Publish forwards the merged snapshot to every registered publisher.
// Returns the first error encountered (all publishers still run).
func (m *Multi) Publish(ctx context.Context, snapshot Snapshot) error {
	var firstErr error
	for _, p := range m.publishers {
		if err := p.Publish(ctx, snapshot); err != nil {
			slog.ErrorContext(ctx, "publisher failed", "error", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// PublishState forwards a per-state snapshot to every registered publisher.
// Returns the first error encountered (all publishers still run). One
// per-state failure does not stop fan-out for the same reason Publish doesn't:
// R2 should not block local file writes and vice versa.
func (m *Multi) PublishState(ctx context.Context, snapshot StateSnapshot) error {
	var firstErr error
	for _, p := range m.publishers {
		if err := p.PublishState(ctx, snapshot); err != nil {
			slog.ErrorContext(ctx, "publisher PublishState failed",
				"area_state", snapshot.AreaState,
				"error", err,
			)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
