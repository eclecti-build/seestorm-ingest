package nws

import (
	"math"
	"testing"
	"time"
)

const floatTolerance = 1e-9

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

// motionParams wraps a single eventMotionDescription value in the shape
// ParseEventMotion expects.
func motionParams(v string) map[string][]string {
	return map[string][]string{"eventMotionDescription": {v}}
}

func TestParseEventMotion_SinglePointTypicalWarning(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected motion, got nil")
	}
	if !floatEq(got.OriginLat, 44.31) {
		t.Errorf("OriginLat: got %v, want 44.31", got.OriginLat)
	}
	if !floatEq(got.OriginLon, -91.8) {
		t.Errorf("OriginLon: got %v, want -91.8", got.OriginLon)
	}
	if got.DirectionDeg != 244 {
		t.Errorf("DirectionDeg: got %d, want 244", got.DirectionDeg)
	}
	if got.SpeedKt != 38 {
		t.Errorf("SpeedKt: got %d, want 38", got.SpeedKt)
	}
	if got.Points != nil {
		t.Errorf("Points should be nil for single-point input, got %v", got.Points)
	}
	want := time.Date(2026, 4, 17, 20, 29, 0, 0, time.UTC)
	if !got.ValidAt.Equal(want) {
		t.Errorf("ValidAt: got %s, want %s", got.ValidAt, want)
	}
}

func TestParseEventMotion_MultiPointStormLine(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-91.8 44.23,-91.75 44.02,-91.77",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("expected motion, got nil")
	}
	if len(got.Points) != 3 {
		t.Fatalf("Points length: got %d, want 3", len(got.Points))
	}
	if !floatEq(got.Points[0][0], 44.31) || !floatEq(got.Points[0][1], -91.8) {
		t.Errorf("Points[0]: got %v, want {44.31, -91.8}", got.Points[0])
	}
	if !floatEq(got.OriginLat, got.Points[0][0]) || !floatEq(got.OriginLon, got.Points[0][1]) {
		t.Errorf("Origin (%v, %v) must equal Points[0] %v",
			got.OriginLat, got.OriginLon, got.Points[0])
	}
}

func TestParseEventMotion_TwoDigitDirection(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...45DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DirectionDeg != 45 {
		t.Errorf("DirectionDeg: got %d, want 45", got.DirectionDeg)
	}
}

func TestParseEventMotion_OneDigitDirection(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...5DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.DirectionDeg != 5 {
		t.Errorf("DirectionDeg: got %d, want 5", got.DirectionDeg)
	}
}

func TestParseEventMotion_ZeroSpeedStationary(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...0KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error for 0KT: %v", err)
	}
	if got == nil {
		t.Fatal("expected motion, got nil")
	}
	if got.SpeedKt != 0 {
		t.Errorf("SpeedKt: got %d, want 0", got.SpeedKt)
	}
}

func TestParseEventMotion_ZTimezoneSuffix(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00Z...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 4, 17, 20, 29, 0, 0, time.UTC)
	if !got.ValidAt.Equal(want) {
		t.Errorf("ValidAt: got %s, want %s", got.ValidAt, want)
	}
	if got.ValidAt.Location() != time.UTC {
		t.Errorf("ValidAt should be UTC, got %v", got.ValidAt.Location())
	}
}

func TestParseEventMotion_PlusZeroTimezone(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00+00:00...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 4, 17, 20, 29, 0, 0, time.UTC)
	if !got.ValidAt.Equal(want) {
		t.Errorf("ValidAt: got %s, want %s", got.ValidAt, want)
	}
}

func TestParseEventMotion_MinusZeroTimezone(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 4, 17, 20, 29, 0, 0, time.UTC)
	if !got.ValidAt.Equal(want) {
		t.Errorf("ValidAt: got %s, want %s", got.ValidAt, want)
	}
}

func TestParseEventMotion_FractionalSeconds(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00.123-00:00...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 4, 17, 20, 29, 0, 123000000, time.UTC)
	if !got.ValidAt.Equal(want) {
		t.Errorf("ValidAt: got %s, want %s", got.ValidAt, want)
	}
}

func TestParseEventMotion_NilParametersMap(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(nil)
	if got != nil || err != nil {
		t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
	}
}

func TestParseEventMotion_EmptyEventMotionArray(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(map[string][]string{"eventMotionDescription": {}})
	if got != nil || err != nil {
		t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
	}
}

// Empty-string / whitespace-only entries should be treated like absence,
// not malformation. Otherwise the motionFailed counter gets polluted with
// noise whenever NWS (or an upstream stub) publishes a blank entry.
func TestParseEventMotion_EmptyStringEntry(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "   ", "\t", "\n\n"} {
		got, err := ParseEventMotion(motionParams(raw))
		if got != nil || err != nil {
			t.Errorf("input %q: expected (nil, nil), got (%+v, %v)", raw, got, err)
		}
	}
}

func TestParseEventMotion_MalformedEntry(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams("garbage"))
	if got != nil {
		t.Fatalf("expected nil motion, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected non-nil error for malformed entry")
	}
}

func TestParseEventMotion_PositiveLongitudeDefensiveNegation(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !floatEq(got.OriginLon, -91.8) {
		t.Errorf("OriginLon: got %v, want -91.8 (defensive negation)", got.OriginLon)
	}
}

func TestParseEventMotion_OutOfBoundsLatitude(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...85,-91.8",
	))
	if got != nil {
		t.Fatalf("expected nil motion, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected error for latitude 85")
	}
}

func TestParseEventMotion_OutOfBoundsLongitude(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-50",
	))
	if got != nil {
		t.Fatalf("expected nil motion, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected error for longitude -50")
	}
}

func TestParseEventMotion_UnparseableTimestamp(t *testing.T) {
	t.Parallel()
	before := time.Now().UTC()
	got, err := ParseEventMotion(motionParams(
		"not-a-timestamp...storm...244DEG...38KT...44.31,-91.8",
	))
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("unexpected error (timestamp fallback should succeed): %v", err)
	}
	if got == nil {
		t.Fatal("expected motion with fallback ValidAt, got nil")
	}
	// ValidAt must land inside [before, after] (plus 5s slack) because it
	// was sourced from time.Now() during the call.
	if got.ValidAt.Before(before.Add(-5*time.Second)) || got.ValidAt.After(after.Add(5*time.Second)) {
		t.Errorf("ValidAt %s not within 5s of now [%s, %s]", got.ValidAt, before, after)
	}
	// Coordinates still load-bearing.
	if !floatEq(got.OriginLat, 44.31) || !floatEq(got.OriginLon, -91.8) {
		t.Errorf("Origin not preserved: got (%v, %v), want (44.31, -91.8)",
			got.OriginLat, got.OriginLon)
	}
}

func TestParseEventMotion_DirectionBoundaryValid(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"0", "359"} {
		dir := dir
		t.Run(dir+"DEG", func(t *testing.T) {
			t.Parallel()
			got, err := ParseEventMotion(motionParams(
				"2026-04-17T20:29:00-00:00...storm..." + dir + "DEG...38KT...44.31,-91.8",
			))
			if err != nil {
				t.Fatalf("unexpected error for %sDEG: %v", dir, err)
			}
			if got == nil {
				t.Fatalf("expected motion for %sDEG, got nil", dir)
			}
		})
	}
}

func TestParseEventMotion_DirectionBoundary360Invalid(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...360DEG...38KT...44.31,-91.8",
	))
	if got != nil {
		t.Fatalf("expected nil motion for 360DEG, got %+v", got)
	}
	if err == nil {
		t.Fatal("expected error for 360DEG")
	}
}

func TestParseEventMotion_BearingRawValue(t *testing.T) {
	t.Parallel()
	got, err := ParseEventMotion(motionParams(
		"2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-91.8",
	))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The parser must not invert the bearing — client handles any display
	// transform. Raw 244 in means raw 244 out.
	if got.DirectionDeg != 244 {
		t.Errorf("DirectionDeg: got %d, want 244 (raw value, no inversion)", got.DirectionDeg)
	}
}
