package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
	"github.com/eclecti-build/seestorm-ingest/internal/health"
	"github.com/eclecti-build/seestorm-ingest/internal/healthcheck"
	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/poller"
	"github.com/eclecti-build/seestorm-ingest/internal/publisher"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// stateCodeRE validates each token in the NWS_AREA list. NWS expects
// uppercase 2-letter USPS state codes; anything else is dropped with a warning
// rather than failing startup.
var stateCodeRE = regexp.MustCompile(`^[A-Z]{2}$`)

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

	areas := parseAreas(os.Getenv("NWS_AREA"))
	if len(areas) == 0 {
		return fmt.Errorf("NWS_AREA resolved to no valid state codes")
	}

	pollInterval := 30 * time.Second
	if v := os.Getenv("POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil {
			pollInterval = d
		}
	}

	// HEALTHCHECK_PING_URL is unset by default (local dev): healthcheck.New("")
	// returns a no-op Pinger, so this line never needs an env-presence branch.
	healthPingURL := os.Getenv("HEALTHCHECK_PING_URL")

	// MODE splits the fleet into region-scoped ingesters and a single snapshot
	// publisher. Unset defaults to "both" (poll + publish), preserving the
	// historical single-node behavior. An invalid value fails fast rather than
	// silently falling back to "both" — see poller.ParseMode.
	rawMode := os.Getenv("MODE")
	if err := requireExplicitModeOnFly(os.Getenv("FLY_APP_NAME"), rawMode); err != nil {
		return err
	}

	mode, err := poller.ParseMode(rawMode)
	if err != nil {
		return err
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

	// Only nodes that actually publish need R2. Gating on the mode means an
	// ingest-only node never constructs the R2 client — so it can't fail
	// startup on absent or invalid R2 credentials it would never use.
	if !mode.ShouldPublish() {
		slog.Info("r2 publisher skipped (ingest-only mode)")
	} else if r2AccountID := os.Getenv("R2_ACCOUNT_ID"); r2AccountID != "" {
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
	healthReg := health.NewRegistry()

	p := poller.New(poller.Config{
		NWS:          nwsClient,
		SPC:          spcClient,
		Store:        db,
		Publisher:    pub,
		PollInterval: pollInterval,
		Areas:        areas,
		Mode:         mode,
		Health:       healthReg,
		HealthPing:   healthcheck.New(healthPingURL),
	})

	maxAge := time.Duration(health.StalenessMultiplier) * pollInterval
	feeds := health.RequiredFeeds(mode.ShouldIngest(), mode.ShouldPublish())
	mux := http.NewServeMux()
	mux.Handle("/healthz", health.NewHandler(healthReg, feeds, maxAge, time.Now))
	healthSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", config.HealthPort),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := healthSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("health server failed", "error", err)
		}
	}()
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()

	slog.Info("starting seestorm-ingest",
		"mode", string(mode),
		"poll_interval", pollInterval,
		"areas", areas,
		"user_agent", nwsUserAgent,
		"publishers", len(publishers),
		"healthcheck_ping_configured", healthPingURL != "",
	)

	if err := p.Run(ctx); err != nil {
		return fmt.Errorf("poller: %w", err)
	}

	return nil
}

// parseAreas turns a raw NWS_AREA env value into the slice of state codes
// passed to the poller. Tokens are trimmed, uppercased, validated against
// both the USPS 2-letter pattern AND the actual NWS state/territory
// allowlist, and de-duplicated. Invalid tokens are logged and skipped
// rather than failing startup so a single typo doesn't take down the whole
// service. Empty / unset input falls back to ["WI"] to preserve the
// historical single-state default.
//
// Validating against the allowlist (not just the regex) prevents a typo
// like `ZZ` or `XX` from passing through to api.weather.gov — those would
// match the regex shape but return zero alerts and silently drop coverage
// for whatever state the user meant.
func parseAreas(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{"WI"}
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	for _, token := range strings.Split(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(token))
		if !stateCodeRE.MatchString(code) || !nws.IsValidStateCode(code) {
			slog.Warn("ignoring invalid NWS_AREA token", "token", token)
			continue
		}
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out
}

// requireExplicitModeOnFly returns an error when running on Fly
// (FLY_APP_NAME set, which Fly always sets) without an explicit MODE
// secret. Fly's fleet model relies on MODE to split region-scoped
// ingesters from the single publisher (see poller.Mode's docs);
// ParseMode's empty->"both" default is correct for local dev but would
// silently turn a regional app into a second publisher if its MODE secret
// were ever missing — reintroducing the 2026-05 history-amplification
// incident (poller/mode.go:8-14). Local dev (no FLY_APP_NAME) is
// unaffected and keeps the "both" default.
func requireExplicitModeOnFly(flyAppName, rawMode string) error {
	if flyAppName == "" {
		return nil
	}
	if strings.TrimSpace(rawMode) != "" {
		return nil
	}
	return fmt.Errorf(
		"MODE is required when running on Fly (FLY_APP_NAME=%q) but is unset; fix: fly secrets set MODE=ingest -a %s (or MODE=publish for the single publisher app)",
		flyAppName, flyAppName,
	)
}
