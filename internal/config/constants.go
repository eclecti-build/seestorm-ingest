// Package config centralizes named numeric constants shared across the ingest
// service. Keeping these in one place (rather than scattered as literals in
// client/pool/poller code) keeps the three surfaces — client, worker, ingest —
// readable as one system. Values here are copied verbatim from the audit's
// "Constants — paste-ready" block (see docs/SWARM_AUDIT_2026-04-18.md).
//
// RetentionDays / RetentionRunHourUTC live here even though retention itself
// ships in Tier 2 #7 — the home for named constants is singular.
package config

const (
	PollIntervalSec     = 30
	PollCycleTimeoutSec = 25 // context.WithTimeout wrapper for each poll cycle

	NWSResponseMaxBytes = 32 * 1024 * 1024 // io.LimitReader cap
	SPCResponseMaxBytes = 4 * 1024 * 1024  // io.LimitReader cap

	// HTTPClientTimeoutSec deviates from the 2026-04-18 audit's paste-ready value
	// of 15. Rationale: multi-state ingest (PR #8, 2026-04-17) merges N states
	// into a single NWS request, and outbreak-day payloads for the 8-state
	// Great Lakes basin can cross several MB of GeoJSON. The 15s cap was sized
	// for the single-state (WI-only) payload shape the audit was written
	// against; 30s is the load-driven ceiling from the multi-state work.
	// Still well below PollCycleTimeoutSec so the cycle deadline remains the
	// binding constraint on slow upstreams.
	HTTPClientTimeoutSec = 30 // per-request timeout on upstream NWS/SPC

	RetentionDays       = 30
	RetentionRunHourUTC = 4 // daily at 04:00 UTC
)
