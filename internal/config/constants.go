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

	// HTTPRetryMaxAttempts bounds in-cycle retries for the NWS/SPC HTTP
	// clients: up to this many TOTAL attempts (1 initial + up to
	// HTTPRetryMaxAttempts-1 retries) before the caller gives up and the
	// fetch+store phase moves on. Kept small — retries share the same
	// PollCycleTimeoutSec-derived fetch+store budget as every other ingest
	// step in the cycle (see poller.splitCycleBudget) and must never
	// extend it.
	HTTPRetryMaxAttempts = 3

	// HTTPRetryBaseDelayMs / HTTPRetryMaxDelayMs bound the exponential
	// backoff (before full jitter) between retry attempts: 500ms doubling
	// up to a 2s cap. Full jitter (uniform random in [0, computed]) avoids
	// a synchronized retry storm across the 8-node fleet hitting the same
	// upstream at the same instant after a shared NWS/SPC blip.
	HTTPRetryBaseDelayMs = 500
	HTTPRetryMaxDelayMs  = 2000

	// HTTPRetryNextAttemptFloorMs is the minimum remaining ctx budget
	// required before starting another retry attempt. Below this, a retry
	// is more likely to be killed mid-flight by the cycle deadline than to
	// complete, so clients stop retrying and return whatever
	// response/error they already have.
	HTTPRetryNextAttemptFloorMs = 250

	// AlertsFetchBudgetPercent bounds how much of the REMAINING fetch+store
	// phase budget the NWS alerts fetch (including any internal/retry
	// retries) may consume, evaluated against whatever time is left in the
	// shared fetchStoreCtx AT THE MOMENT the fetch begins — not a fixed
	// fraction of the whole phase. Alerts fetches first in pollAlerts/
	// pollStormReports' calling order; without this cap a slow/retrying
	// upstream could consume the ENTIRE fetch+store budget, leaving zero
	// time for the three SPC CSV fetches or the store-upsert steps that
	// run after them (review amendment — the same starvation class Task 6
	// fixes for the publish fan-out, applied to the fetch side; see
	// internal/poller's withBudgetFraction).
	AlertsFetchBudgetPercent = 50

	// SPCFetchBudgetPercent bounds how much of the REMAINING fetch+store
	// phase budget EACH of the three SPC CSV fetches (tornado, hail, wind
	// — each including its own internal/retry retries) may consume,
	// evaluated fresh against whatever time remains when that specific
	// fetch begins. Three fetches at 20% each of a shrinking remainder
	// still leaves a guaranteed positive floor for the trailing
	// store-upsert call even in the worst case where every fetch (alerts
	// + all three SPC calls) consumes its full cap — see
	// withBudgetFraction and this task's "retry budget fairness" design
	// decision.
	SPCFetchBudgetPercent = 20

	// HealthPort is the TCP port the /healthz HTTP server listens on
	// inside the Fly Machine. Not exposed via any [[services]]/
	// [http_service] block in fly.toml — only Fly's internal top-level
	// [checks] mechanism (private networking) reaches it.
	HealthPort = 8080

	// HealthStalenessMultiplier is how many PollIntervals a required feed
	// may go without a recorded success before /healthz reports it stale.
	// 3x tolerates the ~7% single-missed-tick rate poller.Run already logs
	// as normal (see its doc comment) without flapping the health check;
	// 3 consecutive misses (90s at the default 30s interval) is well past
	// that noise floor and is the point an operator should be paged.
	HealthStalenessMultiplier = 3
)
