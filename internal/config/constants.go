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

	// PublishPhaseBudgetSec is the guaranteed budget reserved for the publish
	// phase inside each poll cycle. The cycle context is split into a
	// fetch+store phase (cycleTimeout - PublishPhaseBudgetSec) and a separate
	// publish phase (PublishPhaseBudgetSec) so a slow upstream fetch can't
	// starve publish of time to push a fresh snapshot to R2. Total wall-clock
	// bound is unchanged — the two phases run sequentially within cycleTimeout.
	// See poller.splitCycleBudget and Codex review C3.
	//
	// Sized at 15s (was 5s) after 2026-04-21 IA outbreak day: the phase runs
	// a merged snapshot + 8 per-state R2 puts sequentially under a single
	// shared context, so one slow R2 PutObject starves every later put. 15s
	// gives ~1.6s average per put with headroom; the proper fix (per-put
	// timeout + retry) is tracked separately. Fetch+store retains 10s, which
	// observed cycles finish inside comfortably.
	PublishPhaseBudgetSec = 15

	NWSResponseMaxBytes = 32 * 1024 * 1024 // io.LimitReader cap
	SPCResponseMaxBytes = 4 * 1024 * 1024  // io.LimitReader cap

	// HTTPClientTimeoutSec deviates from the 2026-04-18 audit's paste-ready value
	// of 15. Rationale: multi-state ingest (PR #8, 2026-04-17) merges N states
	// into a single NWS request, and outbreak-day payloads for the 8-state
	// Great Lakes basin can cross several MB of GeoJSON. The 15s cap was sized
	// for the single-state (WI-only) payload shape the audit was written
	// against; 30s is the load-driven ceiling from the multi-state work.
	//
	// Higher than PollCycleTimeoutSec (25s) by design — the cycle context
	// fires first and cancels the in-flight request, so this timeout is the
	// slow-network ceiling only, not the binding constraint. Left generous
	// so an HTTP-level abort reads as "cycle gave up" in traces rather than
	// two racing deadlines firing near-simultaneously.
	HTTPClientTimeoutSec = 30 // per-request timeout on upstream NWS/SPC

	RetentionDays       = 30
	RetentionRunHourUTC = 4 // daily at 04:00 UTC
)
