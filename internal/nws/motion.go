// Package nws also provides a parser for the NWS storm-motion description
// published under `properties.parameters.eventMotionDescription`.
//
// Canonical format (single line, `...`-separated segments; coord list is
// space-separated):
//
//	<timestamp>...storm...<DDD>DEG...<SS>KT...<lat>,<lon> [<lat>,<lon> ...]
//
// Example:
//
//	2026-04-17T20:29:00-00:00...storm...244DEG...38KT...44.31,-91.8 44.23,-91.75
//
// Where:
//   - timestamp is RFC3339 (fractional seconds and `Z`/`±HH:MM` offsets OK).
//     NWS has been observed to emit `-00:00`; we normalize that to `+00:00`
//     on the fallback parse.
//   - DDD is the compass bearing in degrees [0,360). This parser does not
//     transform FROM vs TO semantics — that's a display concern handled by
//     the client.
//   - SS is the speed in knots (>= 0; `0KT` is valid for stationary cells).
//   - lat,lon pairs are decimal degrees; the first pair is the canonical
//     origin. Additional pairs populate Points for multi-cell / polyline
//     warnings.
//
// ParseEventMotion distinguishes three outcomes via its (motion, error)
// signature:
//
//   - (nil, nil)  — parameters map is nil/empty or lacks eventMotionDescription.
//   - (nil, err)  — entry present but malformed; err wraps the specific reason.
//     Callers should log this so NWS format drift surfaces.
//   - (motion, nil) — parsed successfully.
package nws

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StormMotion captures the parsed eventMotionDescription fields.
// DirectionDeg is the raw compass bearing from the NWS payload; this parser
// does not invert or otherwise transform it.
type StormMotion struct {
	OriginLat    float64      `json:"origin_lat"`
	OriginLon    float64      `json:"origin_lon"`
	DirectionDeg int          `json:"direction_deg"`
	SpeedKt      int          `json:"speed_kt"`
	ValidAt      time.Time    `json:"valid_at"`
	Points       [][2]float64 `json:"points,omitempty"` // lat,lon; [0]==Origin; omitted when len<=1
}

// motionRe is segment-anchored on the `...storm...<dir>DEG...<kt>KT...<coords>`
// canonical shape. We tolerate leading/trailing whitespace on the entry.
// The timestamp capture is lazy up to `...storm...`; it may contain single
// dots (e.g. fractional seconds like `20:29:00.123`).
//
// Capture groups:
//  1. ts     — timestamp prefix (up to "...storm...")
//  2. dir    — 1–3 digit bearing
//  3. kt     — 1+ digit speed
//  4. coords — space-separated lat,lon pairs
var motionRe = regexp.MustCompile(`^\s*(?P<ts>.+?)\.\.\.storm\.\.\.(?P<dir>\d{1,3})DEG\.\.\.(?P<kt>\d+)KT\.\.\.(?P<coords>.+?)\s*$`)

// pairRe matches a single "lat,lon" coordinate pair. Allows a leading sign on
// either component and an optional fractional part.
var pairRe = regexp.MustCompile(`^(-?\d+(?:\.\d+)?),(-?\d+(?:\.\d+)?)$`)

// ParseEventMotion extracts the first element of
// `parameters["eventMotionDescription"]` and converts it to a StormMotion.
//
// Return semantics:
//   - (nil, nil)  — params is nil, empty, or lacks eventMotionDescription.
//     This is expected for alerts without a motion vector (watches, flood
//     statements, etc.) and is not an error.
//   - (nil, err)  — entry was present but malformed (regex miss, bad coord
//     pair, out-of-range direction/lat/lon). Caller should log this.
//   - (motion, nil) — success. A timestamp that fails both RFC3339 parse
//     attempts does NOT fail the whole parse: ValidAt falls back to
//     time.Now().UTC() and a slog.Warn is emitted. Coordinates are the
//     load-bearing bit; a bad timestamp shouldn't drop geometry.
func ParseEventMotion(params map[string][]string) (*StormMotion, error) {
	if len(params) == 0 {
		return nil, nil
	}
	entries, ok := params["eventMotionDescription"]
	if !ok || len(entries) == 0 {
		return nil, nil
	}
	raw := entries[0]

	m := motionRe.FindStringSubmatch(raw)
	if m == nil {
		return nil, fmt.Errorf("parsing storm motion: entry did not match canonical shape: %q", raw)
	}

	tsRaw := m[1]
	dirRaw := m[2]
	ktRaw := m[3]
	coordsRaw := m[4]

	direction, err := strconv.Atoi(dirRaw)
	if err != nil || direction < 0 || direction >= 360 {
		return nil, fmt.Errorf("parsing storm motion: direction %q out of range [0,360)", dirRaw)
	}

	speed, err := strconv.Atoi(ktRaw)
	if err != nil || speed < 0 {
		return nil, fmt.Errorf("parsing storm motion: invalid speed %q", ktRaw)
	}

	points, err := parseCoordPairs(coordsRaw)
	if err != nil {
		return nil, fmt.Errorf("parsing storm motion: %w", err)
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("parsing storm motion: no coordinate pairs in %q", coordsRaw)
	}

	validAt, ok := parseMotionTimestamp(tsRaw)
	if !ok {
		slog.Warn("storm motion timestamp unparseable; falling back to now",
			"raw_timestamp", tsRaw,
		)
		validAt = time.Now().UTC()
	}

	motion := &StormMotion{
		OriginLat:    points[0][0],
		OriginLon:    points[0][1],
		DirectionDeg: direction,
		SpeedKt:      speed,
		ValidAt:      validAt,
	}
	if len(points) > 1 {
		motion.Points = points
	}
	return motion, nil
}

// parseCoordPairs splits a whitespace-separated list of "lat,lon" pairs and
// validates each against CONUS+AK/HI/PR bounds. Positive longitudes are
// defensively negated (some NWS products omit the sign) and a slog.Warn is
// emitted noting the fix-up.
func parseCoordPairs(s string) ([][2]float64, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil, fmt.Errorf("empty coordinate list")
	}
	points := make([][2]float64, 0, len(fields))
	for _, f := range fields {
		pm := pairRe.FindStringSubmatch(f)
		if pm == nil {
			return nil, fmt.Errorf("coord pair %q does not match lat,lon", f)
		}
		lat, err := strconv.ParseFloat(pm[1], 64)
		if err != nil {
			return nil, fmt.Errorf("coord pair %q: parsing lat: %w", f, err)
		}
		lon, err := strconv.ParseFloat(pm[2], 64)
		if err != nil {
			return nil, fmt.Errorf("coord pair %q: parsing lon: %w", f, err)
		}
		if lat < 10 || lat > 75 {
			return nil, fmt.Errorf("coord pair %q: latitude %v out of range [10,75]", f, lat)
		}
		if lon > 0 {
			slog.Warn("storm motion longitude missing sign; negating defensively",
				"raw_pair", f,
				"lon_before", lon,
			)
			lon = -lon
		}
		if lon < -180 || lon > -60 {
			return nil, fmt.Errorf("coord pair %q: longitude %v out of range [-180,-60]", f, lon)
		}
		points = append(points, [2]float64{lat, lon})
	}
	return points, nil
}

// parseMotionTimestamp tries RFC3339 first, then retries with `-00:00`
// normalized to `+00:00` (an observed NWS quirk). Returns (zero, false) if
// both attempts fail; the caller decides how to fall back.
func parseMotionTimestamp(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC(), true
	}
	if strings.HasSuffix(s, "-00:00") {
		alt := strings.TrimSuffix(s, "-00:00") + "+00:00"
		if t, err := time.Parse(time.RFC3339, alt); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
