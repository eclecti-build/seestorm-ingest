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
		GeneratedAt: time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		Area:        "WI",
		AlertCount:  1,
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
		GeneratedAt: time.Date(2026, 4, 17, 23, 46, 0, 0, time.UTC),
		Area:        "WI",
		AlertCount:  1,
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
