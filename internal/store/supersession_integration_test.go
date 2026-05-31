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

func retiredAt(ctx context.Context, t *testing.T, s *Store, nwsID string) *time.Time {
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

	if retiredAt(ctx, t, s, "test-pr2-retire-x") == nil {
		t.Errorf("x should be retired")
	}
	if retiredAt(ctx, t, s, "test-pr2-retire-y") != nil {
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

	if retiredAt(ctx, t, s, "test-pr2-mismatch-x") != nil {
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
	if retiredAt(ctx, t, s, "test-pr2-missing-y") != nil {
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
	if retiredAt(ctx, t, s, "test-pr2-order-x") == nil {
		t.Errorf("x should be retired even when inserted in the same batch as y")
	}
}

// On the degraded fallback path (a poisoned row aborts the batch tx), a good
// referencing alert must still retire its predecessor.
func TestRetire_FallbackPathRetires(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-fallback-%'")

	// Seed the predecessor in its own clean batch.
	x := alertNoVTEC("test-pr2-fallback-x", "Flood Warning", "Dane, WI")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("seed x: %v", err)
	}

	// poisoned has malformed geometry that fails ST_GeomFromGeoJSON, forcing the
	// whole batch tx to roll back and the per-alert fallback to run.
	poisoned := alertNoVTEC("test-pr2-fallback-bad", "Flood Warning", "Dane, WI")
	poisoned.Geometry = json.RawMessage(`{"type":"NotAGeometry"}`)
	good := alertNoVTEC("test-pr2-fallback-y", "Flood Warning", "Dane, WI", "test-pr2-fallback-x")

	_, degraded, err := s.UpsertAlertsBatch(ctx, []nws.Alert{poisoned, good})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !degraded {
		t.Fatalf("expected the degraded fallback path to run")
	}
	if retiredAt(ctx, t, s, "test-pr2-fallback-x") == nil {
		t.Errorf("predecessor must be retired even on the fallback path")
	}
}

// A stale re-insert of a retired id must NOT un-retire it (ON CONFLICT must not
// refresh retired_at — PR2 Decision 6).
func TestRetire_ReinsertKeepsRetired(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-reinsert-%'")

	x := alertNoVTEC("test-pr2-reinsert-x", "Flood Warning", "Dane, WI")
	y := alertNoVTEC("test-pr2-reinsert-y", "Flood Warning", "Dane, WI", "test-pr2-reinsert-x")
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{x})
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{y})
	if retiredAt(ctx, t, s, "test-pr2-reinsert-x") == nil {
		t.Fatalf("precondition: x should be retired")
	}

	// Stale re-upsert of x (same id) — must keep retired_at set.
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("re-upsert x: %v", err)
	}
	if retiredAt(ctx, t, s, "test-pr2-reinsert-x") == nil {
		t.Errorf("re-inserting a retired id must NOT clear retired_at")
	}
}

// Retiring twice changes nothing the second time (idempotent).
func TestRetire_Idempotent(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-idem-%'")

	x := alertNoVTEC("test-pr2-idem-x", "Flood Warning", "Dane, WI")
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{x})
	y := alertNoVTEC("test-pr2-idem-y", "Flood Warning", "Dane, WI", "test-pr2-idem-x")
	_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{y})
	first := retiredAt(ctx, t, s, "test-pr2-idem-x")

	// Apply the same retirement again directly; row count affected should be 0.
	n, err := retireReferenced(ctx, s.pool, []nws.Alert{y})
	if err != nil {
		t.Fatalf("second retire: %v", err)
	}
	if n != 0 {
		t.Errorf("second retire affected %d rows, want 0 (idempotent)", n)
	}
	if second := retiredAt(ctx, t, s, "test-pr2-idem-x"); second == nil || !second.Equal(*first) {
		t.Errorf("retired_at changed on second apply: %v -> %v", first, second)
	}
}

// Purge deletes ALL expired rows (superseded AND naturally expired), keeps live
// and retired-but-unexpired rows (Codex P2 — most dead rows simply expire).
func TestPurgeExpired(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-purge-%'")

	// live: not expired, not retired -> kept
	// retiredLive: retired but not expired -> kept (soft-delete recoverable)
	// expiredNatural: expired, never retired -> deleted
	for _, a := range []nws.Alert{
		alertNoVTEC("test-pr2-purge-live", "Flood Warning", "Dane, WI"),
		alertNoVTEC("test-pr2-purge-retiredlive", "Flood Warning", "Dane, WI"),
		alertNoVTEC("test-pr2-purge-expired", "Flood Warning", "Dane, WI"),
	} {
		_, _, _ = s.UpsertAlertsBatch(ctx, []nws.Alert{a})
	}
	// Make retiredlive retired-but-unexpired, and expired naturally expired.
	_, _ = s.pool.Exec(ctx, "UPDATE weather_events SET retired_at = NOW() WHERE nws_id = 'test-pr2-purge-retiredlive'")
	_, _ = s.pool.Exec(ctx, "UPDATE weather_events SET expires_at = NOW() - INTERVAL '1 hour' WHERE nws_id = 'test-pr2-purge-expired'")

	n, err := s.PurgeExpired(ctx)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n < 1 {
		t.Errorf("expected at least the naturally-expired row deleted, got %d", n)
	}
	exists := func(id string) bool {
		var c int
		_ = s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM weather_events WHERE nws_id = $1", id).Scan(&c)
		return c > 0
	}
	if !exists("test-pr2-purge-live") {
		t.Errorf("live row must be kept")
	}
	if !exists("test-pr2-purge-retiredlive") {
		t.Errorf("retired-but-unexpired row must be kept")
	}
	if exists("test-pr2-purge-expired") {
		t.Errorf("naturally-expired row must be purged")
	}
}

// Fail-safe: on the fallback path, if the REFERENCING alert is itself the
// poisoned row (never lands), it must NOT retire its predecessor — otherwise we
// transiently drop a live warning whose replacement never arrived.
func TestRetire_FallbackPoisonedReferencerKeepsPredecessor(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-poisonref-%'")

	// Seed predecessor X in a clean batch.
	x := alertNoVTEC("test-pr2-poisonref-x", "Flood Warning", "Dane, WI")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("seed x: %v", err)
	}

	// A separate poisoned non-referencing row forces the batch tx to roll back
	// (so the fallback path runs). Y references X but is ITSELF poisoned —
	// malformed geometry — so Y never lands.
	poison := alertNoVTEC("test-pr2-poisonref-trigger", "Flood Warning", "Dane, WI")
	poison.Geometry = json.RawMessage(`{"type":"NotAGeometry"}`)
	y := alertNoVTEC("test-pr2-poisonref-y", "Flood Warning", "Dane, WI", "test-pr2-poisonref-x")
	y.Geometry = json.RawMessage(`{"type":"NotAGeometry"}`)

	_, degraded, err := s.UpsertAlertsBatch(ctx, []nws.Alert{poison, y})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !degraded {
		t.Fatalf("expected the degraded fallback path to run")
	}
	// Y failed to land, so X must remain live (NOT retired).
	if retiredAt(ctx, t, s, "test-pr2-poisonref-x") != nil {
		t.Errorf("X must stay live: a referencing alert that never landed must not retire its predecessor")
	}
}

// ACCEPTED KNOWN LEAK (spec Decision 5): out-of-order arrival — Y (references X)
// is ingested BEFORE X, and X has a changed footprint (different area_desc) and
// no VTEC. Y's retire no-op'd (X absent), then X lands un-retired; PR1 cannot
// collapse it (no shared VTEC eventID), so X stays in the snapshot until expiry.
// This test pins the leak as intentional and bounded; if a future change retires
// X here, that is a behavior change to review, not an automatic win.
func TestRetire_OutOfOrderArrival_AcceptedLeak(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-ooo-%'")

	// Y arrives first, referencing an X that is not in the DB yet.
	y := alertNoVTEC("test-pr2-ooo-y", "Flood Warning", "Dane, WI", "test-pr2-ooo-x")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{y}); err != nil {
		t.Fatalf("upsert y: %v", err)
	}
	// X arrives late with a different footprint and no VTEC.
	x := alertNoVTEC("test-pr2-ooo-x", "Flood Warning", "Sauk, WI")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{x}); err != nil {
		t.Fatalf("upsert x: %v", err)
	}

	if retiredAt(ctx, t, s, "test-pr2-ooo-x") != nil {
		t.Errorf("documented behavior: late X is NOT retired (no signal retires it)")
	}
	var inSnapshot bool
	alerts, _ := s.GetActiveAlerts(ctx)
	for _, a := range alerts {
		if a.NWSID == "test-pr2-ooo-x" {
			inSnapshot = true
		}
	}
	if !inSnapshot {
		t.Errorf("documented leak: late X stays in the snapshot until expiry")
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

// Real prod case (KSGF.SV.W.0235, MO, 2026-05-31): a Severe Thunderstorm Warning
// issued NEW for 4 counties (AWIPS SVRSGF, WMO WUUS53 KSGF 311712) was continued
// minutes later by a Severe Weather Statement for Phelps only (AWIPS SVSSGF, WMO
// WWUS53 KSGF 311757) that `references` the original. Across that supersession
// BOTH AWIPSidentifier and WMOidentifier change AND the footprint shrinks — so
// neither identifier can dedupe, and PR1's (eventID, event_type, area_desc) key
// misses it because area_desc changed. PR2 retires the original via references +
// matching event_type. This pins that behavior against the real payload shape.
func TestRetire_RealWorld_SVRtoSVSContinuation(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _ = s.pool.Exec(ctx, "DELETE FROM weather_events WHERE nws_id LIKE 'test-pr2-svrsvs-%'")

	// Same event_type + same VTEC ETN, but a changed area_desc — the exact shape
	// that defeats PR1's area_desc-keyed collapse. Parameters carry the P-VTEC so
	// the snapshot derives the same eventID for both (proving PR1 would keep both).
	withVTEC := func(id, area, vtec string, refs ...string) nws.Alert {
		a := alertNoVTEC(id, "Severe Thunderstorm Warning", area, refs...)
		a.Properties.Parameters = map[string][]string{"VTEC": {vtec}}
		return a
	}

	neu := withVTEC("test-pr2-svrsvs-new",
		"Maries, MO; Miller, MO; Phelps, MO; Pulaski, MO",
		"/O.NEW.KSGF.SV.W.0235.260531T1712Z-260531T1815Z/")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{neu}); err != nil {
		t.Fatalf("upsert NEW: %v", err)
	}

	// Continuation references the NEW message; footprint shrinks to Phelps.
	con := withVTEC("test-pr2-svrsvs-con", "Phelps, MO",
		"/O.CON.KSGF.SV.W.0235.000000T0000Z-260531T1815Z/", "test-pr2-svrsvs-new")
	if _, _, err := s.UpsertAlertsBatch(ctx, []nws.Alert{con}); err != nil {
		t.Fatalf("upsert CON: %v", err)
	}

	// retired_at proves PR2's write-time retirement fired (PR1 never writes it).
	if retiredAt(ctx, t, s, "test-pr2-svrsvs-new") == nil {
		t.Errorf("superseded NEW warning must be retired via references despite area_desc/AWIPS/WMO change")
	}

	var sawNew, sawCon bool
	alerts, _ := s.GetActiveAlerts(ctx)
	for _, a := range alerts {
		switch a.NWSID {
		case "test-pr2-svrsvs-new":
			sawNew = true
		case "test-pr2-svrsvs-con":
			sawCon = true
		}
	}
	if sawNew {
		t.Errorf("superseded 4-county NEW must not appear in the snapshot")
	}
	if !sawCon {
		t.Errorf("the Phelps continuation must remain in the snapshot")
	}
}
