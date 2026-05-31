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
