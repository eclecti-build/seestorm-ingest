package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// These tests assert the JSON wire contract for the ingest->client boundary.
// The client depends on specific key names and the omitempty behavior of
// StormMotion — a rename here is a breaking change.

func TestSnapshotJSON_IncludesStormMotionWhenPresent(t *testing.T) {
	t.Parallel()

	validAt := time.Date(2026, 4, 17, 23, 45, 0, 0, time.UTC)
	snap := Snapshot{
		GeneratedAt:   time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		SchemaVersion: SnapshotSchemaVersion,
		Areas:         []string{"WI"},
		AlertCount:    1,
		Alerts: []store.ActiveAlertGeoJSON{
			{
				NWSID:       "urn:oid:2.49.0.1.840.0.test",
				EventType:   "Tornado Warning",
				Severity:    "Extreme",
				Description: "TIME...MOT...LOC 2345Z 230DEG 35KT 4268 8895",
				EffectiveAt: validAt,
				ExpiresAt:   validAt.Add(30 * time.Minute),
				StormMotion: &nws.StormMotion{
					OriginLat:    42.68,
					OriginLon:    -88.95,
					DirectionDeg: 230,
					SpeedKt:      35,
					ValidAt:      validAt,
				},
			},
		},
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling snapshot: %v", err)
	}

	if !strings.Contains(string(raw), `"storm_motion"`) {
		t.Fatalf("expected storm_motion key in JSON, got:\n%s", raw)
	}

	// Verify nested shape: decode and check every field name.
	var decoded struct {
		Alerts []struct {
			StormMotion *struct {
				OriginLat    float64   `json:"origin_lat"`
				OriginLon    float64   `json:"origin_lon"`
				DirectionDeg int       `json:"direction_deg"`
				SpeedKt      int       `json:"speed_kt"`
				ValidAt      time.Time `json:"valid_at"`
			} `json:"storm_motion"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshaling snapshot: %v", err)
	}

	if len(decoded.Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(decoded.Alerts))
	}
	sm := decoded.Alerts[0].StormMotion
	if sm == nil {
		t.Fatal("expected non-nil storm_motion in decoded JSON")
	}
	if sm.OriginLat != 42.68 || sm.OriginLon != -88.95 {
		t.Errorf("origin mismatch: got (%v, %v)", sm.OriginLat, sm.OriginLon)
	}
	if sm.DirectionDeg != 230 {
		t.Errorf("direction_deg: got %d want 230", sm.DirectionDeg)
	}
	if sm.SpeedKt != 35 {
		t.Errorf("speed_kt: got %d want 35", sm.SpeedKt)
	}
	if !sm.ValidAt.Equal(validAt) {
		t.Errorf("valid_at: got %s want %s", sm.ValidAt, validAt)
	}
}

func TestSnapshotJSON_OmitsStormMotionWhenNil(t *testing.T) {
	t.Parallel()

	snap := Snapshot{
		GeneratedAt:   time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		SchemaVersion: SnapshotSchemaVersion,
		Areas:         []string{"WI"},
		AlertCount:    1,
		Alerts: []store.ActiveAlertGeoJSON{
			{
				NWSID:       "urn:oid:2.49.0.1.840.0.nomotion",
				EventType:   "Flood Watch",
				Severity:    "Moderate",
				Description: "No motion block present in this alert.",
				EffectiveAt: time.Date(2026, 4, 17, 20, 0, 0, 0, time.UTC),
				ExpiresAt:   time.Date(2026, 4, 18, 6, 0, 0, 0, time.UTC),
				StormMotion: nil,
			},
		},
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling snapshot: %v", err)
	}

	if strings.Contains(string(raw), `"storm_motion"`) {
		t.Fatalf("expected storm_motion to be omitted, got:\n%s", raw)
	}
}

// stubPublisher records calls for fan-out tests.
type stubPublisher struct {
	mergedCalls []Snapshot
	stateCalls  []StateSnapshot
	failMerged  error
	failState   error
}

func (s *stubPublisher) Publish(_ context.Context, snap Snapshot) error {
	s.mergedCalls = append(s.mergedCalls, snap)
	return s.failMerged
}

func (s *stubPublisher) PublishState(_ context.Context, snap StateSnapshot) error {
	s.stateCalls = append(s.stateCalls, snap)
	return s.failState
}

// TestMultiFanOutBoth asserts both Publish AND PublishState fan out to
// every registered destination, even when one fails. This is the contract
// that lets us add R2 next to the local file publisher without coupling
// failures (an R2 hiccup must not silence the on-disk debug snapshot).
//
// Both methods exercise the failure path independently — a regression in
// either fan-out's first-error-wins behavior should trip exactly one
// sub-test, making the failure point obvious.
func TestMultiFanOutBoth(t *testing.T) {
	t.Parallel()

	t.Run("Publish fans out and surfaces first error", func(t *testing.T) {
		t.Parallel()
		a := &stubPublisher{}
		b := &stubPublisher{failMerged: errors.New("R2 merged boom")}
		multi := NewMulti(a, b)

		err := multi.Publish(context.Background(), Snapshot{
			SchemaVersion: SnapshotSchemaVersion,
			Areas:         []string{"WI"},
		})
		if err == nil {
			t.Fatalf("expected first error to surface, got nil")
		}
		if len(a.mergedCalls) != 1 || len(b.mergedCalls) != 1 {
			t.Errorf("Publish fan-out: a=%d b=%d, want 1 each", len(a.mergedCalls), len(b.mergedCalls))
		}
	})

	t.Run("PublishState fans out and surfaces first error", func(t *testing.T) {
		t.Parallel()
		a := &stubPublisher{}
		b := &stubPublisher{failState: errors.New("R2 per-state boom")}
		multi := NewMulti(a, b)

		err := multi.PublishState(context.Background(), StateSnapshot{
			SchemaVersion: SnapshotSchemaVersion,
			AreaState:     "WI",
		})
		if err == nil {
			t.Fatalf("expected first error to surface, got nil")
		}
		if len(a.stateCalls) != 1 || len(b.stateCalls) != 1 {
			t.Errorf("PublishState fan-out: a=%d b=%d, want 1 each", len(a.stateCalls), len(b.stateCalls))
		}
	})

	t.Run("PublishState succeeds when no destination fails", func(t *testing.T) {
		t.Parallel()
		a := &stubPublisher{}
		b := &stubPublisher{}
		multi := NewMulti(a, b)

		err := multi.PublishState(context.Background(), StateSnapshot{
			SchemaVersion: SnapshotSchemaVersion,
			AreaState:     "WI",
		})
		if err != nil {
			t.Errorf("expected nil error on all-success path, got %v", err)
		}
		if len(a.stateCalls) != 1 || len(b.stateCalls) != 1 {
			t.Errorf("PublishState fan-out: a=%d b=%d, want 1 each", len(a.stateCalls), len(b.stateCalls))
		}
	})
}

// TestPerStateKey asserts the key helper produces the canonical R2/file
// path. Worker route handler matches against this exact shape, so any
// change here must coordinate with worker/index.ts.
func TestPerStateKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		state string
		want  string
	}{
		{"WI", "active-events/WI.json"},
		{"IL", "active-events/IL.json"},
		{"MN", "active-events/MN.json"},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			if got := PerStateKey(tc.state); got != tc.want {
				t.Errorf("PerStateKey(%q) = %q, want %q", tc.state, got, tc.want)
			}
		})
	}

	// Prefix matters for R2 listing — keep it under the asserted constant.
	if PerStateKeyPrefix != "active-events/" {
		t.Errorf("PerStateKeyPrefix changed: got %q want %q", PerStateKeyPrefix, "active-events/")
	}
	// The merged file MUST live outside the per-state prefix so a
	// `list({prefix: PerStateKeyPrefix})` doesn't accidentally include it.
	if strings.HasPrefix(SnapshotKey, PerStateKeyPrefix) {
		t.Errorf("SnapshotKey %q must not live under PerStateKeyPrefix %q — it would pollute list() results",
			SnapshotKey, PerStateKeyPrefix)
	}
}

// TestStateSnapshotJSON asserts the per-state wire contract:
// - top-level `area_state` (singular) is a string, not an array
// - `schema_version` and `alerts[]` keys match the merged shape
// - merged-only fields like `areas` are NOT present
func TestStateSnapshotJSON(t *testing.T) {
	t.Parallel()

	snap := StateSnapshot{
		SchemaVersion: SnapshotSchemaVersion,
		GeneratedAt:   time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		AreaState:     "WI",
		AlertCount:    1,
		Alerts: []store.ActiveAlertGeoJSON{
			{
				NWSID:     "urn:oid:2.49.0.1.840.0.test",
				EventType: "Tornado Warning",
				States:    []string{"WI"},
			},
		},
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling state snapshot: %v", err)
	}

	var decoded struct {
		SchemaVersion int    `json:"schema_version"`
		AreaState     string `json:"area_state"`
		AlertCount    int    `json:"alert_count"`
		Alerts        []struct {
			States []string `json:"states"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshaling state snapshot: %v", err)
	}

	if decoded.SchemaVersion != SnapshotSchemaVersion {
		t.Errorf("schema_version: got %d want %d", decoded.SchemaVersion, SnapshotSchemaVersion)
	}
	if decoded.AreaState != "WI" {
		t.Errorf("area_state: got %q want %q", decoded.AreaState, "WI")
	}
	if len(decoded.Alerts) != 1 || len(decoded.Alerts[0].States) != 1 || decoded.Alerts[0].States[0] != "WI" {
		t.Errorf("alerts[0].states: got %v want [WI]", decoded.Alerts[0].States)
	}

	// Per-state file must NOT publish merged-only fields. If `areas` shows
	// up here, the client gets a confusing dual-shape payload.
	if strings.Contains(string(raw), `"areas":`) {
		t.Errorf("per-state snapshot must not include `areas`, got:\n%s", raw)
	}
}

// TestSnapshotJSON_SchemaVersionAndAreas asserts the v2 wire contract:
// - top-level `schema_version` is published as the constant defined here
// - top-level `areas` is an array (not a string) and reflects the configured set
// - per-alert `states` is an array
// The client refuses to render a snapshot it doesn't understand, so a
// silent rename here would break production.
func TestSnapshotJSON_SchemaVersionAndAreas(t *testing.T) {
	t.Parallel()

	snap := Snapshot{
		SchemaVersion: SnapshotSchemaVersion,
		GeneratedAt:   time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		Areas:         []string{"MN", "WI", "IL", "IN", "MI", "OH", "PA", "NY"},
		AlertCount:    1,
		Alerts: []store.ActiveAlertGeoJSON{
			{
				NWSID:       "urn:oid:2.49.0.1.840.0.multistate",
				EventType:   "Flood Warning",
				Severity:    "Moderate",
				AreaDesc:    "Crawford, WI; Allamakee, IA",
				States:      []string{"IA", "WI"},
				EffectiveAt: time.Date(2026, 4, 17, 20, 0, 0, 0, time.UTC),
				ExpiresAt:   time.Date(2026, 4, 18, 6, 0, 0, 0, time.UTC),
			},
		},
	}

	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshaling snapshot: %v", err)
	}

	var decoded struct {
		SchemaVersion int      `json:"schema_version"`
		Areas         []string `json:"areas"`
		Alerts        []struct {
			States []string `json:"states"`
		} `json:"alerts"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshaling snapshot: %v", err)
	}

	if decoded.SchemaVersion != SnapshotSchemaVersion {
		t.Errorf("schema_version: got %d want %d", decoded.SchemaVersion, SnapshotSchemaVersion)
	}
	if len(decoded.Areas) != 8 {
		t.Errorf("areas length: got %d want 8", len(decoded.Areas))
	}
	if len(decoded.Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(decoded.Alerts))
	}
	gotStates := decoded.Alerts[0].States
	if len(gotStates) != 2 || gotStates[0] != "IA" || gotStates[1] != "WI" {
		t.Errorf("per-alert states: got %v want [IA WI]", gotStates)
	}

	// Belt-and-suspenders: top-level `area` (singular) must NOT appear in v2 —
	// the client treats its absence as a v2 marker.
	if strings.Contains(string(raw), `"area":`) {
		t.Errorf("v2 snapshot must not contain singular `area` key, got:\n%s", raw)
	}
}
