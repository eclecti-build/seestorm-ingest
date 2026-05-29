package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

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

	// MODE splits the fleet into region-scoped ingesters and a single snapshot
	// publisher. Unset defaults to "both" (poll + publish), preserving the
	// historical single-node behavior. An invalid value fails fast rather than
	// silently falling back to "both" — see poller.ParseMode.
	mode, err := poller.ParseMode(os.Getenv("MODE"))
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

	p := poller.New(poller.Config{
		NWS:          nwsClient,
		SPC:          spcClient,
		Store:        db,
		Publisher:    pub,
		PollInterval: pollInterval,
		Areas:        areas,
		Mode:         mode,
	})

	slog.Info("starting seestorm-ingest",
		"mode", string(mode),
		"poll_interval", pollInterval,
		"areas", areas,
		"user_agent", nwsUserAgent,
		"publishers", len(publishers),
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
