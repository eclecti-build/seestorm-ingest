package store

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

// Batch-fallback integration test. Skipped unless TEST_DATABASE_URL is set —
// per CLAUDE.md the ingest repo tests against a real Postgres (no DB mocks).
// CI wires this up with a throwaway Neon branch or a local Docker Postgres.
//
// Behavior under test (audit Open Decisions #8): the batch transaction is
// attempted first; if ANY row in the batch poisons the tx, we fall back to
// per-row inserts so the good rows still land. The degraded path is logged
// with the "batch_upsert_fallback" key so ops can tell the two apart.
func TestUpsertAlertsBatch_PartialFailure_FallsBackAndWritesRemainder(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Clean slate for the ids we're about to write so the assertion is
	// deterministic across repeated local runs.
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-batch-fallback-%'")

	good := func(id string) nws.Alert {
		return nws.Alert{
			Properties: nws.AlertProperties{
				ID:        id,
				Event:     "Tornado Warning",
				Severity:  "Extreme",
				Headline:  "Test alert " + id,
				AreaDesc:  "Test County, WI",
				Effective: time.Now().UTC().Format(time.RFC3339),
				Expires:   time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			},
			Geometry: json.RawMessage(`null`),
		}
	}

	// Poison the batch by shoving malformed GeoJSON into one row — the
	// ST_GeomFromGeoJSON call will fail at commit time, which is the
	// realistic outbreak-load failure shape (one bad polygon kills the tx).
	bad := good("test-batch-fallback-bad")
	bad.Geometry = json.RawMessage(`"not a geometry"`)

	alerts := []nws.Alert{
		good("test-batch-fallback-1"),
		bad,
		good("test-batch-fallback-2"),
	}

	count, degraded, err := s.UpsertAlertsBatch(ctx, alerts)
	if err != nil {
		t.Fatalf("UpsertAlertsBatch returned err: %v", err)
	}
	if !degraded {
		t.Fatal("expected degraded=true after malformed-row failure")
	}
	// In the fallback, the two good rows land; the bad row's per-row insert
	// also fails and is logged+skipped — count should be 2.
	if count != 2 {
		t.Errorf("fallback inserted count: got %d want 2", count)
	}

	// Verify the good rows actually made it to the DB.
	var n int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM weather_events WHERE nws_id LIKE 'test-batch-fallback-%'",
	).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if n != 2 {
		t.Errorf("rows in DB: got %d want 2", n)
	}

	// Cleanup so reruns start from a clean slate.
	if _, err := s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-batch-fallback-%'"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// TestUpsertAlertsBatch_HappyPath_WritesEveryRowInOneTx — happy-path coverage
// to complement the failure-mode test above. Also skipped without a real DB.
func TestUpsertAlertsBatch_HappyPath_WritesEveryRowInOneTx(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-batch-happy-%'")

	alerts := []nws.Alert{}
	for i := 0; i < 5; i++ {
		alerts = append(alerts, nws.Alert{
			Properties: nws.AlertProperties{
				ID:        fmt.Sprintf("test-batch-happy-%d", i),
				Event:     "Severe Thunderstorm Warning",
				Severity:  "Severe",
				AreaDesc:  "Test County, WI",
				Effective: time.Now().UTC().Format(time.RFC3339),
				Expires:   time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			},
			Geometry: json.RawMessage(`null`),
		})
	}

	count, degraded, err := s.UpsertAlertsBatch(ctx, alerts)
	if err != nil {
		t.Fatalf("happy-path batch err: %v", err)
	}
	if degraded {
		t.Fatal("did not expect degraded path on happy batch")
	}
	if count != len(alerts) {
		t.Errorf("inserted count: got %d want %d", count, len(alerts))
	}

	if _, err := s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-batch-happy-%'"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}
