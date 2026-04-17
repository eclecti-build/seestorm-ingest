package poller

import (
	"context"
	"log/slog"
	"time"

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
	Area         string
}

type Poller struct {
	cfg Config
}

func New(cfg Config) *Poller {
	return &Poller{cfg: cfg}
}

func (p *Poller) Run(ctx context.Context) error {
	// Run immediately on start
	p.poll(ctx)

	ticker := time.NewTicker(p.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down poller")
			return nil
		case <-ticker.C:
			p.poll(ctx)
		}
	}
}

func (p *Poller) poll(ctx context.Context) {
	start := time.Now()

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
	alerts, err := p.cfg.NWS.FetchActiveAlerts(ctx, p.cfg.Area)
	if err != nil {
		slog.Error("failed to fetch alerts", "error", err)
		return 0
	}

	count := 0
	for _, alert := range alerts.Features {
		if err := p.cfg.Store.UpsertAlert(ctx, alert); err != nil {
			slog.Error("failed to upsert alert",
				"nws_id", alert.Properties.ID,
				"error", err,
			)
			continue
		}
		count++
	}

	return count
}

func (p *Poller) pollStormReports(ctx context.Context) int {
	reports, err := p.cfg.SPC.FetchTodayTornadoReports(ctx)
	if err != nil {
		slog.Error("failed to fetch tornado reports", "error", err)
		return 0
	}

	// Also fetch hail and wind
	hailReports, err := p.cfg.SPC.FetchTodayHailReports(ctx)
	if err != nil {
		slog.Error("failed to fetch hail reports", "error", err)
	} else {
		reports = append(reports, hailReports...)
	}

	windReports, err := p.cfg.SPC.FetchTodayWindReports(ctx)
	if err != nil {
		slog.Error("failed to fetch wind reports", "error", err)
	} else {
		reports = append(reports, windReports...)
	}

	count := 0
	for _, report := range reports {
		if err := p.cfg.Store.UpsertStormReport(ctx, report); err != nil {
			slog.Error("failed to upsert storm report", "error", err)
			continue
		}
		count++
	}

	return count
}

func (p *Poller) publishSnapshot(ctx context.Context) {
	alerts, err := p.cfg.Store.GetActiveAlerts(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "failed to get active alerts for snapshot", "error", err)
		return
	}

	snapshot := publisher.Snapshot{
		GeneratedAt: time.Now().UTC(),
		Area:        p.cfg.Area,
		AlertCount:  len(alerts),
		Alerts:      alerts,
	}

	if err := p.cfg.Publisher.Publish(ctx, snapshot); err != nil {
		slog.ErrorContext(ctx, "failed to publish snapshot", "error", err)
	}
}
