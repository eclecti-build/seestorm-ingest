package poller

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/publisher"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

type Config struct {
	NWS          *nws.Client
	SPC          *spc.Client
	Store        *store.Store
	Publisher    publisher.Publisher
	PollInterval time.Duration
	// Areas is the set of US state codes to poll. The NWS API accepts a
	// comma-separated list as a single `?area=` query, so multi-state
	// polling stays one HTTP request regardless of slice length.
	Areas []string
	// Mode selects which phases of the cycle this node runs (ingest, publish,
	// or both). The zero value behaves as ModeBoth. See Mode.
	Mode Mode
	// jitterFunc overrides startup-jitter delay computation for tests.
	// nil in production, which uses startupJitter's real math/rand/v2
	// implementation. Unexported: only this package's own tests can set
	// it — not part of the public Config surface cmd/ingest/main.go uses.
	jitterFunc func(time.Duration) time.Duration
}

type Poller struct {
	cfg Config
}

func New(cfg Config) *Poller {
	return &Poller{cfg: cfg}
}

// pollSafely invokes fn (one poll cycle) recovering from any panic so a
// single bad cycle can't unwind through Run and crash the process. This
// is the sanctioned exception to the "no panic() in library code"
// convention (seestorm-ingest/CLAUDE.md) — containment at one
// well-defined boundary around each cycle, not control flow. The next
// scheduled tick in Run's loop proceeds unaffected; nothing here retries
// the panicking cycle early or otherwise changes scheduling.
func pollSafely(ctx context.Context, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			slog.ErrorContext(ctx, "poll cycle panicked",
				"degraded_path", "cycle_panic_recovered",
				"panic", fmt.Sprint(r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn(ctx)
}

// Run drives the polling loop with absolute-time scheduling. Unlike a naive
// time.Ticker, which drifts when a poll cycle runs long and silently coalesces
// missed ticks, this loop computes each wake-up as start + N*interval. When
// a cycle runs past its slot we log a "poll cycle missed" warning and advance
// to the next future tick rather than firing back-to-back catch-up polls —
// so load never amplifies during upstream slowness.
func (p *Poller) Run(ctx context.Context) error {
	// One-time random phase offset in [0, PollInterval) before the FIRST
	// cycle only — NOT a per-cycle change. Without it, a fleet-wide
	// redeploy leaves every node's absolute-time schedule (see this
	// function's top-level doc comment) anchored to the same instant
	// forever, so the 7 ingest-role apps hit NWS/SPC in lockstep every
	// cycle indefinitely. The publisher is exempt (Poller.startupJitter
	// returns 0 when Mode.ShouldIngest() is false, review amendment) — it
	// never polls upstreams, so this rationale doesn't apply to it and
	// jitter there would only add pure post-deploy staleness. The
	// schedule that follows is still pure start + N*interval absolute-time
	// scheduling — this only moves where `start` falls relative to the
	// fleet-wide redeploy instant.
	if jitter := p.startupJitter(); jitter > 0 {
		slog.InfoContext(ctx, "poller startup jitter", "delay", jitter)
		timer := time.NewTimer(jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			slog.InfoContext(ctx, "shutting down poller during startup jitter")
			return nil
		case <-timer.C:
		}
	}

	// Run immediately (post-jitter) on start.
	pollSafely(ctx, p.poll)

	start := time.Now()
	cycle := int64(1)

	for {
		nextAt := start.Add(time.Duration(cycle) * p.cfg.PollInterval)

		// Skip past any ticks we already missed (e.g. a long upstream call).
		// Logs the skip so the ~7% missed-tick rate observed in prod stops being invisible.
		for time.Now().After(nextAt) {
			slog.WarnContext(ctx, "poll cycle missed",
				"missed_tick_at", nextAt.Format(time.RFC3339Nano),
				"cycle", cycle,
			)
			cycle++
			nextAt = start.Add(time.Duration(cycle) * p.cfg.PollInterval)
		}

		timer := time.NewTimer(time.Until(nextAt))
		select {
		case <-ctx.Done():
			timer.Stop()
			slog.InfoContext(ctx, "shutting down poller")
			return nil
		case <-timer.C:
			pollSafely(ctx, p.poll)
			cycle++
		}
	}
}

// cycleSlack is the tail we reserve inside each PollInterval for publish +
// bookkeeping after the derived cycle ctx fires. Keeps the "timeout < interval"
// invariant robust even if the operator picks an unusual PollInterval.
const cycleSlack = 5 * time.Second

// cycleFloor is the minimum cycle timeout we'll ever apply. Prevents the
// derived timeout from collapsing to zero if PollInterval is ever set below
// cycleSlack (misconfig or aggressive test setup).
const cycleFloor = 1 * time.Second

// deriveCycleTimeout returns the per-cycle context timeout given the
// configured PollInterval. Logic:
//   - Clamp at PollCycleTimeoutSec (the fixed ceiling baked into the audit).
//   - Never exceed PollInterval - cycleSlack, so publish/slack always fits
//     inside the interval and the next tick isn't starved.
//   - Never drop below cycleFloor, guarding misconfigured tiny intervals.
//
// This preserves the audit-settled 25s ceiling for the normal 30s interval
// while making the invariant "cycle timeout < interval" hold for any
// PollInterval the operator picks.
func deriveCycleTimeout(interval time.Duration) time.Duration {
	ceiling := time.Duration(config.PollCycleTimeoutSec) * time.Second
	derived := interval - cycleSlack
	if derived > ceiling {
		derived = ceiling
	}
	if derived < cycleFloor {
		derived = cycleFloor
	}
	return derived
}

// startupJitter returns a random duration in [0, interval) used once as
// an initial delay before the FIRST poll cycle, so a fleet-wide redeploy
// (all 8 apps restarting within the same second) doesn't leave every
// node's absolute-time schedule anchored to the same instant forever —
// this loop never re-jitters after the first cycle, so without this a
// lockstep fleet stays locked in step indefinitely, all 8 hitting
// NWS/SPC at the same moment every cycle. math/rand/v2 is auto-seeded,
// so this is intentionally non-deterministic across process restarts;
// Config.jitterFunc overrides it for deterministic tests.
func startupJitter(interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(interval)))
}

func (p *Poller) startupJitter() time.Duration {
	// Publish-only nodes never poll NWS/SPC (Mode.ShouldIngest() is
	// false) — the lockstep-avoidance rationale above (de-sync the 8
	// fleet nodes' upstream polling after a fleet-wide redeploy) doesn't
	// apply to them. Applying jitter anyway would only add up to
	// PollInterval of pure post-deploy staleness on the merged/history R2
	// snapshot for zero benefit (review amendment).
	if !p.cfg.Mode.ShouldIngest() {
		return 0
	}
	if p.cfg.jitterFunc != nil {
		return p.cfg.jitterFunc(p.cfg.PollInterval)
	}
	return startupJitter(p.cfg.PollInterval)
}

// splitCycleBudget divides the per-cycle wall-clock budget into a fetch+store
// phase budget and a separate publish-phase budget. The publish phase aims
// for PublishPhaseBudgetSec (15s) regardless of how long fetch+store took,
// so a slow NWS/SPC or Postgres batch can never starve publish. A 1s floor
// per phase guards against pathological tiny cycleTimeout values.
//
// The two phases run sequentially inside a single pollCtx.WithTimeout(…,
// cycleTimeout), so total wall-clock is still bounded by cycleTimeout; this
// function only controls how that budget is apportioned between phases.
//
// When cycleTimeout is smaller than PublishPhaseBudgetSec + the 1s fetch+store
// floor (e.g. an operator-set POLL_INTERVAL that derives a short cycleTimeout),
// publish is capped at cycleTimeout - 1s so the two phases actually fit
// sequentially. Without the cap, the outer pollCtx cancels publish mid-flight
// and the cycle is silently dropped.
//
// See Codex review C3 (publish starvation) and the 2026-04-21 follow-up
// review which caught the interval-headroom regression when we raised
// PublishPhaseBudgetSec from 5s to 15s.
func splitCycleBudget(cycleTimeout time.Duration) (fetchStoreBudget, publishBudget time.Duration) {
	const phaseFloor = time.Second
	publishBudget = time.Duration(config.PublishPhaseBudgetSec) * time.Second
	if maxPublish := cycleTimeout - phaseFloor; publishBudget > maxPublish {
		publishBudget = maxPublish
	}
	if publishBudget < phaseFloor {
		publishBudget = phaseFloor
	}
	fetchStoreBudget = cycleTimeout - publishBudget
	if fetchStoreBudget < phaseFloor {
		fetchStoreBudget = phaseFloor
	}
	return fetchStoreBudget, publishBudget
}

// withBudgetFraction derives a child context capped at pct percent of
// ctx's CURRENTLY REMAINING time until its deadline — not a fixed
// fraction of the whole phase, since "remaining" shrinks as earlier
// fetches in the same phase consume time. If ctx has no deadline (not
// expected in production — poll always derives fetchStoreCtx with
// context.WithTimeout — but kept safe for tests/callers that pass a bare
// context.Background()), returns a cancelable copy of ctx unchanged so
// callers still get a valid CancelFunc to defer. See this task's "retry
// budget fairness" design decision.
func withBudgetFraction(ctx context.Context, pct int) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok {
		return context.WithCancel(ctx)
	}
	remaining := time.Until(dl)
	if remaining <= 0 {
		return context.WithDeadline(ctx, dl)
	}
	capped := time.Duration(int64(remaining) * int64(pct) / 100)
	return context.WithTimeout(ctx, capped)
}

// poll runs one polling cycle. The cycle runs under a derived cycleTimeout
// (see deriveCycleTimeout) that's split into two sequential phases with
// SEPARATE deadlines both derived from pollCtx:
//
//  1. fetch+store phase — pollAlerts + pollStormReports, bounded at
//     cycleTimeout - publishBudget.
//  2. publish phase — publishSnapshot, bounded at publishBudget (fixed 15s).
//
// Each phase is gated by the configured Mode: an ingest-only node skips the
// publish phase, and a publish-only node skips fetch+store entirely (it never
// touches NWS/SPC and publishes purely from the shared database).
//
// The phases share pollCtx as parent, so caller-level cancellation
// propagates to both. Critically, the publish phase gets its own fresh
// deadline regardless of how long fetch+store took — a slow upstream fetch
// or a slow batch upsert cannot starve publish and leave the R2 snapshot
// stale while Postgres already has the fresh data.
//
// Total wall-clock for one cycle is bounded by cycleTimeout for any
// operator-reachable PollInterval (default 30s → 25s cycleTimeout →
// 20s + 5s = 25s total). The phases run sequentially, so their budgets
// sum rather than overlap. In pathological configs where cycleTimeout is
// below PublishPhaseBudgetSec + fetchStoreFloor (1s) — only reachable with
// sub-6s PollInterval values — we intentionally favor publish getting its
// full 5s over strict cycleTimeout adherence, since a 1s interval is
// already a misconfig. See splitCycleBudget and Codex review C3.
func (p *Poller) poll(pollCtx context.Context) {
	start := time.Now()

	// Scope the whole cycle under a per-cycle deadline. Without this, a slow
	// upstream (NWS under load, SPC hung socket) can stretch a single cycle
	// past the interval, cause a parade of "poll cycle missed" warnings,
	// and — under outbreak load — hold pool connections long enough to
	// starve the next cycle. The timeout is derived from PollInterval so
	// the "cycle timeout < interval" invariant holds for any operator-set
	// interval, not just the default 30s. See deriveCycleTimeout.
	cycleTimeout := deriveCycleTimeout(p.cfg.PollInterval)
	fetchStoreBudget, publishBudget := splitCycleBudget(cycleTimeout)

	var alertCount, reportCount, alertsSkippedUnparseable int

	// Fetch+store phase (ingest + both modes): bounded so a slow NWS/SPC can't
	// consume the whole cycle budget. When this context expires, in-flight
	// fetches/upserts are cancelled but the publish phase below still gets its
	// own fresh budget. Publish-only nodes skip this entirely.
	if p.cfg.Mode.ShouldIngest() {
		fetchStoreCtx, fetchStoreCancel := context.WithTimeout(pollCtx, fetchStoreBudget)
		alertCount, alertsSkippedUnparseable = p.pollAlerts(fetchStoreCtx)
		reportCount = p.pollStormReports(fetchStoreCtx)
		// PR2: drop expired dead rows so the table stops growing unboundedly.
		// Runs only on ingest-role nodes (writers); idempotent across the fleet.
		if n, err := p.cfg.Store.PurgeExpired(fetchStoreCtx); err != nil {
			slog.ErrorContext(fetchStoreCtx, "failed to purge expired rows", "error", err)
		} else if n > 0 {
			slog.InfoContext(fetchStoreCtx, "purged expired rows", "deleted", n)
		}
		fetchStoreCancel()
	}

	// Publish phase (publish + both modes): independent deadline derived from
	// pollCtx, not from the (possibly nearly-exhausted) fetchStoreCtx. This is
	// the fix for Codex C3 — a cycle where fetch/store consumed ~all its budget
	// previously left publish with <1s and the R2 snapshot went stale.
	// Ingest-only nodes skip this so only the designated publisher writes the
	// merged + history snapshot.
	if p.cfg.Mode.ShouldPublish() {
		publishCtx, publishCancel := context.WithTimeout(pollCtx, publishBudget)
		p.publishSnapshot(publishCtx)
		publishCancel()
	}

	slog.Info("poll cycle complete",
		"mode", string(p.cfg.Mode),
		"alerts_processed", alertCount,
		"alerts_skipped_unparseable", alertsSkippedUnparseable,
		"reports_processed", reportCount,
		"duration", time.Since(start),
		"cycle_timeout", cycleTimeout,
		"fetch_store_budget", fetchStoreBudget,
		"publish_budget", publishBudget,
	)
}

func (p *Poller) pollAlerts(ctx context.Context) (count int, skippedUnparseable int) {
	// Cap the alerts fetch (including any internal/retry retries) at
	// AlertsFetchBudgetPercent of whatever fetch+store budget remains
	// right now. Alerts fetches first among this phase's upstream calls
	// — without this cap, a slow/retrying NWS could consume the ENTIRE
	// fetch+store budget and starve the three SPC CSV fetches and the
	// store-upsert step below (review amendment; see withBudgetFraction).
	// The store-upsert call keeps using ctx directly, NOT this
	// sub-context, so it always gets whatever's left of the real budget.
	fetchCtx, fetchCancel := withBudgetFraction(ctx, config.AlertsFetchBudgetPercent)
	alerts, err := p.cfg.NWS.FetchActiveAlerts(fetchCtx, strings.Join(p.cfg.Areas, ","))
	fetchCancel()
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch alerts", "error", err)
		return 0, 0
	}

	// Batch upsert — whole-cycle round-trip count drops from O(n) per-alert
	// statements to one transactional batch, keeping us under
	// PollCycleTimeoutSec under outbreak load. On tx failure the Store falls
	// back to per-alert inserts and logs degraded_path=batch_upsert_fallback.
	// skipped counts alerts deliberately excluded for an unparseable
	// `expires` timestamp (Store.parseAlertTimes) — not a failure, but
	// aggregated into poll()'s cycle-summary log line below so a sustained
	// rise is visible without an operator grepping individual per-row WARNs
	// (REVIEW AMENDMENT, 2026-07-08 Tier 1 plan).
	count, skipped, degraded, err := p.cfg.Store.UpsertAlertsBatch(ctx, alerts.Features)
	if err != nil {
		slog.ErrorContext(ctx, "failed to batch-upsert alerts", "error", err)
		return count, skipped
	}
	if degraded {
		slog.WarnContext(ctx, "alerts upserted via fallback path",
			"count", count,
			"total", len(alerts.Features),
		)
	}

	return count, skipped
}

func (p *Poller) pollStormReports(ctx context.Context) int {
	tornCtx, tornCancel := withBudgetFraction(ctx, config.SPCFetchBudgetPercent)
	reports, err := p.cfg.SPC.FetchTodayTornadoReports(tornCtx)
	tornCancel()
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch tornado reports", "error", err)
		// A tornado-feed failure shouldn't kill hail + wind ingest — keep going.
		reports = nil
	}

	hailCtx, hailCancel := withBudgetFraction(ctx, config.SPCFetchBudgetPercent)
	hailReports, err := p.cfg.SPC.FetchTodayHailReports(hailCtx)
	hailCancel()
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch hail reports", "error", err)
	} else {
		reports = append(reports, hailReports...)
	}

	windCtx, windCancel := withBudgetFraction(ctx, config.SPCFetchBudgetPercent)
	windReports, err := p.cfg.SPC.FetchTodayWindReports(windCtx)
	windCancel()
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch wind reports", "error", err)
	} else {
		reports = append(reports, windReports...)
	}

	// Batch upsert — see pollAlerts comment. Same tx-first / fallback shape.
	count, degraded, err := p.cfg.Store.UpsertStormReportsBatch(ctx, reports)
	if err != nil {
		slog.ErrorContext(ctx, "failed to batch-upsert storm reports", "error", err)
		return count
	}
	if degraded {
		slog.WarnContext(ctx, "storm reports upserted via fallback path",
			"count", count,
			"total", len(reports),
		)
	}

	return count
}

// publishTimeout is the per-call deadline for a single Publish or
// PublishState invocation. Bounds the worst case so a single hung R2 PUT
// can't block the rest of the cycle. Generous vs. typical R2 PUT latency
// (sub-second) but well below the 30s polling interval.
const publishTimeout = 5 * time.Second

func (p *Poller) publishSnapshot(ctx context.Context) {
	alerts, err := p.cfg.Store.GetActiveAlerts(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get active alerts for snapshot", "error", err)
		return
	}

	// Build the merged snapshot first so the per-state siblings can reuse
	// its GeneratedAt — NewSnapshot derives both generated_at and
	// generated_at_ms from a single time.Now().UTC() call so the RFC3339 and
	// epoch-ms fields can't drift sub-second. The client's staleness check
	// depends on them agreeing, and merging clients that read both the
	// merged file and a per-state sibling must see the same instant.
	snapshot := publisher.NewSnapshot(p.cfg.Areas, alerts)

	// Merged snapshot — the back-compat path and the "view all" UX. Goes
	// through the publisher's history archival as well.
	p.publishWithTimeout(ctx, "merged", "", func(timeoutCtx context.Context) error {
		return p.cfg.Publisher.Publish(timeoutCtx, snapshot)
	})

	// Per-state snapshots — one per configured area, written to
	// active-events/<STATE>.json. Cross-border alerts (states ⊃ {STATE})
	// appear in every matching state's file, which is the natural
	// semantics for an alert footprint that genuinely touches multiple
	// states. Alerts with no resolved States[] don't appear in any
	// per-state file (they remain in the merged snapshot only) — this
	// degrades gracefully for upstream payloads we can't classify.
	for _, state := range p.cfg.Areas {
		filtered := filterAlertsByState(alerts, state)
		stateSnapshot := publisher.NewStateSnapshot(state, filtered, snapshot.GeneratedAt)
		p.publishWithTimeout(ctx, "state", state, func(timeoutCtx context.Context) error {
			return p.cfg.Publisher.PublishState(timeoutCtx, stateSnapshot)
		})
	}
}

// publishWithTimeout wraps a single publish call in a bounded deadline so
// one hung destination (typically an R2 PUT during a Cloudflare degradation)
// can't starve the rest of the publish fan-out. Logs failures with kind +
// state context for triage; never returns an error because publish failures
// are non-fatal for the polling loop (next cycle in 30s will overwrite).
func (p *Poller) publishWithTimeout(parent context.Context, kind, state string, fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(parent, publishTimeout)
	defer cancel()
	if err := fn(ctx); err != nil {
		attrs := []any{"kind", kind, "error", err}
		if state != "" {
			attrs = append(attrs, "state", state)
		}
		slog.ErrorContext(parent, "publish failed", attrs...)
	}
}

// filterAlertsByState returns the subset of alerts whose States slice
// contains the given state code. Used to shard the merged snapshot into
// per-state files. O(n*m) where n = alert count, m = average states/alert
// (typically 1-2) — fine at our volumes (low hundreds of alerts during a
// major outbreak).
func filterAlertsByState(alerts []store.ActiveAlertGeoJSON, state string) []store.ActiveAlertGeoJSON {
	out := make([]store.ActiveAlertGeoJSON, 0, len(alerts))
	for _, a := range alerts {
		if slices.Contains(a.States, state) {
			out = append(out, a)
		}
	}
	return out
}
