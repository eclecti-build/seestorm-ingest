package publisher

import (
	"encoding/json"
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
