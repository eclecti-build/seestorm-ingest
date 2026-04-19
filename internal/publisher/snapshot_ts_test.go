package publisher

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

// TestNewSnapshot_GeneratedAtAndMsDerivedFromSameInstant is the contract
// that backs the client's staleness check (Open Decisions #11, red-banner
// threshold 90s). If `generated_at_ms` ever diverges from `generated_at`
// the client will either flag a false stall or mask a real one.
func TestNewSnapshot_GeneratedAtAndMsDerivedFromSameInstant(t *testing.T) {
	t.Parallel()

	snap := NewSnapshot([]string{"WI"}, nil)

	// Round-trip through JSON to prove the wire format carries both fields
	// and that they still agree after encode/decode.
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded struct {
		GeneratedAt   time.Time `json:"generated_at"`
		GeneratedAtMs int64     `json:"generated_at_ms"`
		Areas         []string  `json:"areas"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.GeneratedAtMs == 0 {
		t.Fatalf("generated_at_ms missing from JSON: %s", raw)
	}
	if len(decoded.Areas) != 1 || decoded.Areas[0] != "WI" {
		t.Errorf("areas: got %v want [WI]", decoded.Areas)
	}

	// generated_at_ms should equal generated_at.UnixMilli(). Exact equality
	// is expected because both are derived from the same time.Time.
	wantMs := decoded.GeneratedAt.UnixMilli()
	if decoded.GeneratedAtMs != wantMs {
		t.Errorf("generated_at_ms (%d) != generated_at.UnixMilli (%d)",
			decoded.GeneratedAtMs, wantMs)
	}
}

// TestNewSnapshot_CarriesAlertCount is the happy-path sanity that the helper
// doesn't silently drop the alert count or alerts slice.
func TestNewSnapshot_CarriesAlertCount(t *testing.T) {
	t.Parallel()

	alerts := []store.ActiveAlertGeoJSON{
		{NWSID: "a"},
		{NWSID: "b"},
	}
	snap := NewSnapshot([]string{"WI"}, alerts)

	if snap.AlertCount != 2 {
		t.Errorf("AlertCount: got %d want 2", snap.AlertCount)
	}
	if len(snap.Alerts) != 2 {
		t.Errorf("Alerts len: got %d want 2", len(snap.Alerts))
	}
}

// TestSnapshotJSON_IncludesGeneratedAtMsField is a wire-contract lock-in:
// the client + UptimeRobot expect the exact key `generated_at_ms`. A rename
// here is a breaking change for both.
func TestSnapshotJSON_IncludesGeneratedAtMsField(t *testing.T) {
	t.Parallel()

	snap := NewSnapshot([]string{"WI"}, nil)
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(raw), `"generated_at_ms":`) {
		t.Fatalf("expected generated_at_ms key in JSON, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"generated_at":`) {
		t.Fatalf("expected generated_at key still present in JSON, got: %s", raw)
	}
}

// TestNewStateSnapshot_CarriesSharedInstant locks in the per-state invariant:
// every per-state file must report the same generated_at / generated_at_ms as
// the merged snapshot it was derived from. Clients that combine the merged
// file with a per-state sibling must see one timestamp, not two.
func TestNewStateSnapshot_CarriesSharedInstant(t *testing.T) {
	t.Parallel()

	merged := NewSnapshot([]string{"WI", "IL"}, nil)
	state := NewStateSnapshot("WI", nil, merged.GeneratedAt)

	if !state.GeneratedAt.Equal(merged.GeneratedAt) {
		t.Errorf("generated_at mismatch: state %v merged %v", state.GeneratedAt, merged.GeneratedAt)
	}
	if state.GeneratedAtMs != merged.GeneratedAtMs {
		t.Errorf("generated_at_ms mismatch: state %d merged %d", state.GeneratedAtMs, merged.GeneratedAtMs)
	}
	if state.AreaState != "WI" {
		t.Errorf("area_state: got %q want WI", state.AreaState)
	}
	if state.SchemaVersion != SnapshotSchemaVersion {
		t.Errorf("schema_version: got %d want %d", state.SchemaVersion, SnapshotSchemaVersion)
	}
}

// TestStateSnapshotJSON_IncludesGeneratedAtMsField — same wire-contract
// lock-in as the merged snapshot, applied to the per-state file shape.
func TestStateSnapshotJSON_IncludesGeneratedAtMsField(t *testing.T) {
	t.Parallel()

	at := time.Now().UTC()
	state := NewStateSnapshot("WI", nil, at)
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if !strings.Contains(string(raw), `"generated_at_ms":`) {
		t.Fatalf("expected generated_at_ms key in per-state JSON, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"area_state":"WI"`) {
		t.Fatalf("expected area_state key in per-state JSON, got: %s", raw)
	}
}
