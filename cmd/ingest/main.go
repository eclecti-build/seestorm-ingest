package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/poller"
	"github.com/eclecti-build/seestorm-ingest/internal/publisher"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}

	fmt.Println("shutdown complete")
}

func run() error {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}

	snapshotDir := os.Getenv("SNAPSHOT_DIR")
	if snapshotDir == "" {
		snapshotDir = "./snapshots"
	}

	nwsUserAgent := os.Getenv("NWS_USER_AGENT")
	if nwsUserAgent == "" {
		nwsUserAgent = "(seestorm.org, contact@seestorm.org)"
	}

	area := os.Getenv("NWS_AREA")
	if area == "" {
		area = "WI"
	}

	pollInterval := 30 * time.Second
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			pollInterval = d
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := store.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect db: %w", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// Publishers are composed so file + R2 fan out in parallel.
	// File always runs (useful for on-Fly debugging via ssh console).
	// R2 runs when all four R2_* env vars are present — in local dev, leaving
	// them empty gracefully falls back to file-only.
	publishers := []publisher.Publisher{publisher.NewFile(snapshotDir)}

	if r2AccountID := os.Getenv("R2_ACCOUNT_ID"); r2AccountID != "" {
		r2Pub, err := publisher.NewR2(ctx, publisher.R2Config{
			AccountID:       r2AccountID,
			AccessKeyID:     os.Getenv("R2_ACCESS_KEY_ID"),
			SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
			Bucket:          os.Getenv("R2_BUCKET_NAME"),
		})
		if err != nil {
			return fmt.Errorf("init r2 publisher: %w", err)
		}
		publishers = append(publishers, r2Pub)
		slog.Info("r2 publisher enabled", "bucket", os.Getenv("R2_BUCKET_NAME"))
	} else {
		slog.Info("r2 publisher disabled (R2_ACCOUNT_ID not set) — publishing to file only")
	}

	nwsClient := nws.NewClient(nwsUserAgent)
	spcClient := spc.NewClient()
	pub := publisher.NewMulti(publishers...)

	p := poller.New(poller.Config{
		NWS:          nwsClient,
		SPC:          spcClient,
		Store:        db,
		Publisher:    pub,
		PollInterval: pollInterval,
		Area:         area,
	})

	slog.Info("starting seestorm-ingest",
		"poll_interval", pollInterval,
		"area", area,
		"user_agent", nwsUserAgent,
		"publishers", len(publishers),
	)

	if err := p.Run(ctx); err != nil {
		return fmt.Errorf("poller: %w", err)
	}

	return nil
}
