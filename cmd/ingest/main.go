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

	nwsClient := nws.NewClient("(seestorm.org, contact@seestorm.org)")
	spcClient := spc.NewClient()
	pub := publisher.NewFilePublisher(snapshotDir)

	p := poller.New(poller.Config{
		NWS:          nwsClient,
		SPC:          spcClient,
		Store:        db,
		Publisher:    pub,
		PollInterval: pollInterval,
		Area:         "WI",
	})

	slog.Info("starting seestorm-ingest",
		"poll_interval", pollInterval,
		"area", "WI",
	)

	if err := p.Run(ctx); err != nil {
		return fmt.Errorf("poller: %w", err)
	}

	return nil
}
