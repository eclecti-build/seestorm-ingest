package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
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

// TestUpsertAlertsBatch_CtxCanceled_ShortCircuits locks in the Codex S4 fix:
// when the batch tx fails because the cycle ctx was canceled (deadline fired,
// shutdown signal), we must NOT fall back to per-row inserts. Per-row retries
// against a dead ctx fan out into O(n) doomed statements and end with a silent
// degraded-path "success" that drops the tail. The fix surfaces the ctx error
// instead.
func TestUpsertAlertsBatch_CtxCanceled_ShortCircuits(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	s, err := New(setupCtx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	if err := s.Migrate(setupCtx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Pre-canceled ctx — the BeginTx call will fail immediately with
	// context.Canceled, putting us on the exact error path the fix guards.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	alerts := []nws.Alert{
		{
			Properties: nws.AlertProperties{
				ID:        "test-batch-ctx-1",
				Event:     "Tornado Warning",
				Severity:  "Extreme",
				AreaDesc:  "Test County, WI",
				Effective: time.Now().UTC().Format(time.RFC3339),
				Expires:   time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			},
			Geometry: json.RawMessage(`null`),
		},
	}

	count, degraded, err := s.UpsertAlertsBatch(ctx, alerts)
	if err == nil {
		t.Fatal("expected ctx error to propagate, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	// Crucially: degraded must be false — we did NOT fall back to per-row.
	if degraded {
		t.Error("expected degraded=false on ctx cancellation (no fallback); got true")
	}
	if count != 0 {
		t.Errorf("expected count=0 on ctx cancellation, got %d", count)
	}
}

// TestUpsertStormReportsBatch_HappyPath_WritesEveryRowInOneTx mirrors
// TestUpsertAlertsBatch_HappyPath for the storm-reports batch path.
// See Codex S5 — storm reports share the same tx-first / per-row fallback
// shape as alerts but had no coverage prior to this test.
func TestUpsertStormReportsBatch_HappyPath_WritesEveryRowInOneTx(t *testing.T) {
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

	// Storm reports are unique by (report_type, location, reported_at), so
	// we vary the location per row to get N distinct inserts.
	base := time.Now().UTC().Truncate(time.Second)
	_, _ = s.pool.Exec(ctx,
		"DELETE FROM storm_reports WHERE location LIKE 'test-sr-happy-%'")

	var reports []spc.StormReport
	for i := 0; i < 5; i++ {
		reports = append(reports, spc.StormReport{
			Type:      "tornado",
			Magnitude: "EF1",
			Location:  fmt.Sprintf("test-sr-happy-%d", i),
			County:    "Test County",
			State:     "WI",
			Comments:  "integration test",
			Lat:       44.0 + float64(i)*0.01,
			Lon:       -89.0 - float64(i)*0.01,
			Time:      base.Add(time.Duration(i) * time.Minute),
		})
	}

	count, degraded, err := s.UpsertStormReportsBatch(ctx, reports)
	if err != nil {
		t.Fatalf("happy-path batch err: %v", err)
	}
	if degraded {
		t.Fatal("did not expect degraded path on happy batch")
	}
	if count != len(reports) {
		t.Errorf("inserted count: got %d want %d", count, len(reports))
	}

	var n int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM storm_reports WHERE location LIKE 'test-sr-happy-%'",
	).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if n != len(reports) {
		t.Errorf("rows in DB: got %d want %d", n, len(reports))
	}

	if _, err := s.pool.Exec(ctx,
		"DELETE FROM storm_reports WHERE location LIKE 'test-sr-happy-%'"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// TestUpsertStormReportsBatch_PartialFailure_FallsBackAndWritesRemainder
// mirrors the alert-batch partial-failure test for storm reports. Poisons
// one row with an out-of-range longitude so ST_MakePoint rejects it at
// commit time, forcing the tx-first path to fail and the per-row fallback
// to kick in. See Codex S5.
func TestUpsertStormReportsBatch_PartialFailure_FallsBackAndWritesRemainder(t *testing.T) {
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

	base := time.Now().UTC().Truncate(time.Second)
	_, _ = s.pool.Exec(ctx,
		"DELETE FROM storm_reports WHERE location LIKE 'test-sr-fallback-%'")

	good := func(i int) spc.StormReport {
		return spc.StormReport{
			Type:      "hail",
			Magnitude: "1.00",
			Location:  fmt.Sprintf("test-sr-fallback-%d", i),
			County:    "Test County",
			State:     "WI",
			Comments:  "integration test",
			Lat:       44.0,
			Lon:       -89.0,
			Time:      base.Add(time.Duration(i) * time.Minute),
		}
	}

	// Poison the middle row with an out-of-range longitude. PostGIS +
	// GEOMETRY(Point, 4326) will reject this at commit time, which is
	// the realistic "bad row kills the tx" shape the fallback guards.
	bad := good(99)
	bad.Location = "test-sr-fallback-bad"
	bad.Lon = 9999.0 // outside valid lon range — PostGIS rejects

	reports := []spc.StormReport{good(1), bad, good(2)}

	count, degraded, err := s.UpsertStormReportsBatch(ctx, reports)
	if err != nil {
		t.Fatalf("UpsertStormReportsBatch returned err: %v", err)
	}
	if !degraded {
		t.Fatal("expected degraded=true after malformed-row failure")
	}
	// Two good rows land; the bad row's per-row insert also fails
	// and is logged+skipped — count should be 2.
	if count != 2 {
		t.Errorf("fallback inserted count: got %d want 2", count)
	}

	var n int
	if err := s.pool.QueryRow(ctx,
		"SELECT COUNT(*) FROM storm_reports WHERE location LIKE 'test-sr-fallback-%'",
	).Scan(&n); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if n != 2 {
		t.Errorf("rows in DB: got %d want 2", n)
	}

	if _, err := s.pool.Exec(ctx,
		"DELETE FROM storm_reports WHERE location LIKE 'test-sr-fallback-%'"); err != nil {
		t.Logf("cleanup: %v", err)
	}
}

// TestUpsertStormReportsBatch_CtxCanceled_ShortCircuits — same invariant as
// the alert-batch ctx-cancel test, applied to storm reports. See Codex S4.
func TestUpsertStormReportsBatch_CtxCanceled_ShortCircuits(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}

	setupCtx, setupCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer setupCancel()

	s, err := New(setupCtx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()

	if err := s.Migrate(setupCtx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reports := []spc.StormReport{{
		Type: "wind", Magnitude: "60", Location: "test-sr-ctx-1",
		County: "Test County", State: "WI",
		Lat: 44.0, Lon: -89.0, Time: time.Now().UTC(),
	}}

	count, degraded, err := s.UpsertStormReportsBatch(ctx, reports)
	if err == nil {
		t.Fatal("expected ctx error to propagate, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if degraded {
		t.Error("expected degraded=false on ctx cancellation (no fallback); got true")
	}
	if count != 0 {
		t.Errorf("expected count=0 on ctx cancellation, got %d", count)
	}
}
