package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

// newTestStore connects to TEST_DATABASE_URL (real Postgres per CLAUDE.md — no
// DB mocks), migrates, and returns a Store. Skips when the env var is unset.
func newTestStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — skipping integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s, ctx
}

// alertNoVTEC builds a minimal non-VTEC alert. No VTEC means PR1's collapse is a
// no-op for it, so these tests isolate PR2 retirement from PR1 display-collapse.
func alertNoVTEC(id, event, areaDesc string, refs ...string) nws.Alert {
	var references []nws.AlertReference
	for _, r := range refs {
		references = append(references, nws.AlertReference{Identifier: r})
	}
	return nws.Alert{
		Properties: nws.AlertProperties{
			ID:         id,
			Event:      event,
			Severity:   "Severe",
			Headline:   "Test " + id,
			AreaDesc:   areaDesc,
			Effective:  time.Now().UTC().Format(time.RFC3339),
			Expires:    time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339),
			References: references,
		},
		Geometry: json.RawMessage(`null`),
	}
}

func retiredAt(t *testing.T, s *Store, ctx context.Context, nwsID string) *time.Time {
	t.Helper()
	var ts *time.Time
	err := s.pool.QueryRow(ctx,
		"SELECT retired_at FROM weather_events WHERE nws_id = $1", nwsID).Scan(&ts)
	if err != nil {
		t.Fatalf("query retired_at for %s: %v", nwsID, err)
	}
	return ts
}

// Happy path: Y references X with matching event_type → X retired, gone from
// the snapshot, Y present.
func TestRetire_SupersededPredecessorRetired(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-retire-%'")

	x := alertNoVTEC("test-pr2-retire-x", "Flood Warning", "Dane, WI")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("upsert x: %v", err)
	}
	y := alertNoVTEC("test-pr2-retire-y", "Flood Warning", "Dane, WI", "test-pr2-retire-x")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{y}); err != nil {
		t.Fatalf("upsert y: %v", err)
	}

	if retiredAt(t, s, ctx, "test-pr2-retire-x") == nil {
		t.Errorf("x should be retired")
	}
	if retiredAt(t, s, ctx, "test-pr2-retire-y") != nil {
		t.Errorf("y should not be retired")
	}
	alerts, _ := s.GetActiveAlerts(ctx)
	for _, a := range alerts {
		if a.NWSID == "test-pr2-retire-x" {
			t.Fatalf("retired x leaked into snapshot")
		}
	}
}

// Fail-safe: event_type mismatch → predecessor NOT retired.
func TestRetire_EventTypeMismatchKeepsRow(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-mismatch-%'")

	x := alertNoVTEC("test-pr2-mismatch-x", "Tornado Warning", "Dane, WI")
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{x})
	// Y is a different product that merely references X.
	y := alertNoVTEC("test-pr2-mismatch-y", "Special Weather Statement", "Dane, WI", "test-pr2-mismatch-x")
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{y})

	if retiredAt(t, s, ctx, "test-pr2-mismatch-x") != nil {
		t.Errorf("x must NOT be retired across a different event_type (fail-safe)")
	}
}

// Missing reference → clean no-op, no error.
func TestRetire_MissingReferenceIsNoOp(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-missing-%'")

	y := alertNoVTEC("test-pr2-missing-y", "Flood Warning", "Dane, WI", "test-pr2-missing-ghost")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{y}); err != nil {
		t.Fatalf("upsert y referencing a never-ingested id should not error: %v", err)
	}
	if retiredAt(t, s, ctx, "test-pr2-missing-y") != nil {
		t.Errorf("y should not be retired")
	}
}

// Same batch contains both X and a Y that references it → X retired (ordering:
// upsert before retire).
func TestRetire_SameBatchOrdering(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-order-%'")

	x := alertNoVTEC("test-pr2-order-x", "Flood Warning", "Dane, WI")
	y := alertNoVTEC("test-pr2-order-y", "Flood Warning", "Dane, WI", "test-pr2-order-x")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x, y}); err != nil {
		t.Fatalf("upsert batch: %v", err)
	}
	if retiredAt(t, s, ctx, "test-pr2-order-x") == nil {
		t.Errorf("x should be retired even when inserted in the same batch as y")
	}
}

// Baseline: the schema has a retired_at column and the snapshot excludes a row
// once retired_at is set.
func TestSchema_RetiredRowExcludedFromSnapshot(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-schema-%'")

	x := alertNoVTEC("test-pr2-schema-x", "Flood Warning", "Dane, WI")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Manually retire it (Task 2 only adds the column + filter; retirement
	// logic arrives in Task 4).
	if _, err := s.pool.Exec(ctx,
		"UPDATE weather_events SET retired_at = NOW() WHERE nws_id = $1", "test-pr2-schema-x"); err != nil {
		t.Fatalf("manual retire: %v", err)
	}

	alerts, err := s.GetActiveAlerts(ctx)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, a := range alerts {
		if a.NWSID == "test-pr2-schema-x" {
			t.Fatalf("retired row leaked into snapshot")
		}
	}
}
