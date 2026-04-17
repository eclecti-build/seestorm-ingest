package nws

import (
	"math"
	"testing"
	"time"
)

const floatTolerance = 1e-9

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing reference time %q: %v", s, err)
	}
	return ts
}

func floatEq(a, b float64) bool {
	return math.Abs(a-b) < floatTolerance
}

// realWorldSample mirrors an actual NWS Wisconsin tornado warning body, with
// prose, &&-delimited section, and the TIME...MOT...LOC payload inline.
const realWorldSample = `At 645 PM CDT, a confirmed tornado was located near Janesville, moving northeast at 40 mph.

HAZARD...Damaging tornado.

SOURCE...Law enforcement confirmed tornado.

IMPACT...Flying debris will be dangerous to those caught without
shelter. Mobile homes will be damaged or destroyed. Damage to roofs,
windows, and vehicles will occur.  Tree damage is likely.

&&

TIME...MOT...LOC 2345Z 230DEG 35KT 4268 8895 4272 8880

HAILCAP...<.75IN
TORNADOCAP...OBSERVED
`

// outcome classifies the expected return triple from ParseStormMotion.
type outcome int

const (
	// outcomeOK: (non-nil motion, nil error) — the happy path.
	outcomeOK outcome = iota
	// outcomeAbsent: (nil, nil) — no TIME...MOT...LOC header at all.
	outcomeAbsent
	// outcomeMalformed: (nil, non-nil error) — header present, parse failed.
	outcomeMalformed
)

func TestParseStormMotion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		description string
		issuedAt    time.Time
		outcome     outcome
		want        *StormMotion // only checked when outcome == outcomeOK
	}{
		{
			name:        "happy path",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 4258 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -89.47,
				DirectionDeg: 270,
				SpeedKt:      30,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			name:        "5-digit longitude",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 4258 10345",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -103.45,
				DirectionDeg: 270,
				SpeedKt:      30,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			name:        "no header returns (nil, nil)",
			description: "SEVERE THUNDERSTORM WARNING for south-central Wisconsin.",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeAbsent,
		},
		{
			name:        "truncated after header returns error",
			description: "TIME...MOT...LOC ",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			name:        "non-digit in direction returns error",
			description: "TIME...MOT...LOC 0145Z 27ADEG 30KT 4258 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			name:        "day rollover backward subtracts 24h",
			description: "TIME...MOT...LOC 2355Z 180DEG 25KT 4300 8900",
			issuedAt:    mustTime(t, "2026-04-17T00:05:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    43.00,
				OriginLon:    -89.00,
				DirectionDeg: 180,
				SpeedKt:      25,
				ValidAt:      mustTime(t, "2026-04-16T23:55:00Z"),
			},
		},
		{
			name:        "day rollover forward adds 24h",
			description: "TIME...MOT...LOC 0005Z 180DEG 25KT 4300 8900",
			issuedAt:    mustTime(t, "2026-04-16T23:55:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    43.00,
				OriginLon:    -89.00,
				DirectionDeg: 180,
				SpeedKt:      25,
				ValidAt:      mustTime(t, "2026-04-17T00:05:00Z"),
			},
		},
		{
			// Simulates a 6h-late anchor (e.g. effective_at drifts from sent_at).
			// Naive combine = 2026-04-17T00:01Z, diff = -5h59m, within ±12h so
			// no flip — still resolves to the same UTC date as the anchor.
			name:        "anchor drift within window does not flip",
			description: "TIME...MOT...LOC 0001Z 180DEG 25KT 4300 8900",
			issuedAt:    mustTime(t, "2026-04-17T06:00:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    43.00,
				OriginLon:    -89.00,
				DirectionDeg: 180,
				SpeedKt:      25,
				ValidAt:      mustTime(t, "2026-04-17T00:01:00Z"),
			},
		},
		{
			name:        "same-day normal no adjustment",
			description: "TIME...MOT...LOC 2355Z 180DEG 25KT 4300 8900",
			issuedAt:    mustTime(t, "2026-04-17T23:55:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    43.00,
				OriginLon:    -89.00,
				DirectionDeg: 180,
				SpeedKt:      25,
				ValidAt:      mustTime(t, "2026-04-17T23:55:00Z"),
			},
		},
		{
			name:        "zero speed still parses",
			description: "TIME...MOT...LOC 0145Z 270DEG 0KT 4258 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -89.47,
				DirectionDeg: 270,
				SpeedKt:      0,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			name:        "tabs and double spaces between tokens",
			description: "TIME...MOT...LOC\t0145Z  270DEG\t30KT  4258\t8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -89.47,
				DirectionDeg: 270,
				SpeedKt:      30,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			name: "multiple blocks first wins",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 4258 8947\n" +
				"... later ...\n" +
				"TIME...MOT...LOC 0200Z 090DEG 50KT 4300 9000",
			issuedAt: mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:  outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -89.47,
				DirectionDeg: 270,
				SpeedKt:      30,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			name: "leading and trailing prose",
			description: "At 845 PM CDT a severe storm was located...\n\n" +
				"TIME...MOT...LOC 0145Z 270DEG 30KT 4258 8947\n\n" +
				"HAZARD...60 mph wind gusts.\n",
			issuedAt: mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:  outcomeOK,
			want: &StormMotion{
				OriginLat:    42.58,
				OriginLon:    -89.47,
				DirectionDeg: 270,
				SpeedKt:      30,
				ValidAt:      mustTime(t, "2026-04-17T01:45:00Z"),
			},
		},
		{
			// Multi-vertex real-NWS sample: the first pair (4268 8895) wins,
			// and the \b boundary after the first longitude is satisfied by
			// the whitespace before the second vertex.
			name:        "real-world WI tornado warning sample",
			description: realWorldSample,
			issuedAt:    mustTime(t, "2026-04-17T23:45:00Z"),
			outcome:     outcomeOK,
			want: &StormMotion{
				OriginLat:    42.68,
				OriginLon:    -88.95,
				DirectionDeg: 230,
				SpeedKt:      35,
				ValidAt:      mustTime(t, "2026-04-17T23:45:00Z"),
			},
		},
		{
			name:        "direction out of range 360 returns error",
			description: "TIME...MOT...LOC 0145Z 360DEG 30KT 4258 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			name:        "lat out of CONUS range returns error",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 9999 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			name:        "lon out of range returns error",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 4258 19999",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			name:        "invalid hour HHMM returns error",
			description: "TIME...MOT...LOC 2545Z 270DEG 30KT 4258 8947",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
		{
			// 6-digit longitude 103456 must NOT silently truncate to 10345.
			// The \b boundary after the longitude capture rejects this.
			name:        "6-digit longitude rejected",
			description: "TIME...MOT...LOC 0145Z 270DEG 30KT 4258 103456",
			issuedAt:    mustTime(t, "2026-04-17T01:40:00Z"),
			outcome:     outcomeMalformed,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseStormMotion(tt.description, tt.issuedAt)

			switch tt.outcome {
			case outcomeAbsent:
				if got != nil || err != nil {
					t.Fatalf("expected (nil, nil), got (%+v, %v)", got, err)
				}
				return
			case outcomeMalformed:
				if got != nil {
					t.Fatalf("expected nil motion, got %+v", got)
				}
				if err == nil {
					t.Fatal("expected non-nil error for malformed block, got nil")
				}
				return
			case outcomeOK:
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got == nil {
					t.Fatalf("expected %+v, got nil", tt.want)
				}
				if !floatEq(got.OriginLat, tt.want.OriginLat) {
					t.Errorf("OriginLat: got %v, want %v", got.OriginLat, tt.want.OriginLat)
				}
				if !floatEq(got.OriginLon, tt.want.OriginLon) {
					t.Errorf("OriginLon: got %v, want %v", got.OriginLon, tt.want.OriginLon)
				}
				if got.DirectionDeg != tt.want.DirectionDeg {
					t.Errorf("DirectionDeg: got %d, want %d", got.DirectionDeg, tt.want.DirectionDeg)
				}
				if got.SpeedKt != tt.want.SpeedKt {
					t.Errorf("SpeedKt: got %d, want %d", got.SpeedKt, tt.want.SpeedKt)
				}
				if !got.ValidAt.Equal(tt.want.ValidAt) {
					t.Errorf("ValidAt: got %s, want %s", got.ValidAt, tt.want.ValidAt)
				}
			}
		})
	}
}
