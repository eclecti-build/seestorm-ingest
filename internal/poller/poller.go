package poller

import (
	"context"
	"log/slog"
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
}

type Poller struct {
	cfg Config
}

func New(cfg Config) *Poller {
	return &Poller{cfg: cfg}
}

// Run drives the polling loop with absolute-time scheduling. Unlike a naive
// time.Ticker, which drifts when a poll cycle runs long and silently coalesces
// missed ticks, this loop computes each wake-up as start + N*interval. When
// a cycle runs past its slot we log a "poll cycle missed" warning and advance
// to the next future tick rather than firing back-to-back catch-up polls —
// so load never amplifies during upstream slowness.
func (p *Poller) Run(ctx context.Context) error {
	// Run immediately on start.
	p.poll(ctx)

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
			p.poll(ctx)
			cycle++
		}
	}
}

func (p *Poller) poll(pollCtx context.Context) {
	start := time.Now()

	// Scope the whole cycle under a per-cycle deadline. Without this, a slow
	// upstream (NWS under load, SPC hung socket) can stretch a single cycle
	// past the 30s interval, cause a parade of "poll cycle missed" warnings,
	// and — under outbreak load — hold pool connections long enough to
	// starve the next cycle. PollCycleTimeoutSec is deliberately < the
	// 30s interval so we always relinquish before the next tick fires.
	ctx, cancel := context.WithTimeout(pollCtx, config.PollCycleTimeoutSec*time.Second)
	defer cancel()

	// Fetch NWS alerts
	alertCount := p.pollAlerts(ctx)

	// Fetch SPC storm reports
	reportCount := p.pollStormReports(ctx)

	// Publish snapshot
	p.publishSnapshot(ctx)

	slog.Info("poll cycle complete",
		"alerts_processed", alertCount,
		"reports_processed", reportCount,
		"duration", time.Since(start),
	)
}

func (p *Poller) pollAlerts(ctx context.Context) int {
	// NWS supports a comma-separated `area` value natively, so multi-state
	// polling is one request rather than N. See nws.FetchActiveAlerts docs.
	alerts, err := p.cfg.NWS.FetchActiveAlerts(ctx, strings.Join(p.cfg.Areas, ","))
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch alerts", "error", err)
		return 0
	}

	// Batch upsert — whole-cycle round-trip count drops from O(n) per-alert
	// statements to one transactional batch, keeping us under
	// PollCycleTimeoutSec under outbreak load. On tx failure the Store falls
	// back to per-alert inserts and logs degraded_path=batch_upsert_fallback.
	count, degraded, err := p.cfg.Store.UpsertAlertsBatch(ctx, alerts.Features)
	if err != nil {
		slog.ErrorContext(ctx, "failed to batch-upsert alerts", "error", err)
		return count
	}
	if degraded {
		slog.WarnContext(ctx, "alerts upserted via fallback path",
			"count", count,
			"total", len(alerts.Features),
		)
	}

	return count
}

func (p *Poller) pollStormReports(ctx context.Context) int {
	reports, err := p.cfg.SPC.FetchTodayTornadoReports(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch tornado reports", "error", err)
		// A tornado-feed failure shouldn't kill hail + wind ingest — keep going.
		reports = nil
	}

	// Also fetch hail and wind
	hailReports, err := p.cfg.SPC.FetchTodayHailReports(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to fetch hail reports", "error", err)
	} else {
		reports = append(reports, hailReports...)
	}

	windReports, err := p.cfg.SPC.FetchTodayWindReports(ctx)
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
