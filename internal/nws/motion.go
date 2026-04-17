// Package nws also provides a parser for the NWS TIME...MOT...LOC block that
// accompanies tornado, severe thunderstorm, and flash flood warnings.
//
// Format (single line, whitespace-separated):
//
//	TIME...MOT...LOC <HHMM>Z <DDD>DEG <SS>KT <LAT4> <LON4or5> [extra pairs...]
//
// Where:
//   - HHMM is the UTC time the motion snapshot was taken.
//   - DDD is the direction the storm is moving FROM, in degrees [0,360).
//   - SS is the speed in knots.
//   - LAT4 is 4 digits: whole degrees + hundredths (e.g. 4258 -> 42.58).
//   - LON4or5 is 4 or 5 digits, CONUS West (always negated): last two digits
//     are hundredths, everything before is whole degrees (e.g. 8947 -> -89.47,
//     10345 -> -103.45).
//
// Additional lat/lon pairs beyond the first are ignored — the first pair is
// treated as the canonical storm origin for forward-projection rendering.
//
// ParseStormMotion distinguishes three outcomes via its (motion, error)
// signature:
//
//   - (nil, nil)  — no TIME...MOT...LOC block present in the description.
//   - (nil, err)  — block present but malformed; err wraps the specific reason.
//     Callers should log this so NWS format drift surfaces.
//   - (motion, nil) — parsed successfully.
package nws

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// StormMotion captures the parsed TIME...MOT...LOC fields. DirectionDeg is the
// direction the storm comes FROM; forward travel bearing is (direction+180)%360.
type StormMotion struct {
	OriginLat    float64   `json:"origin_lat"`
	OriginLon    float64   `json:"origin_lon"`
	DirectionDeg int       `json:"direction_deg"` // FROM which storm comes
	SpeedKt      int       `json:"speed_kt"`
	ValidAt      time.Time `json:"valid_at"`
}

// motionHeader is a cheap loose-detection string; absence means "no block
// present" (a legitimate non-error case).
const motionHeader = "TIME...MOT...LOC"

// motionRe matches the TIME...MOT...LOC header block. The trailing \b ensures
// a 6-digit longitude like 103456 fails the match rather than being silently
// truncated to 10345. Multi-vertex descriptions (e.g. "8895 4272 8880") still
// parse because whitespace after the first longitude satisfies \b.
//
// Capture groups:
//  1. HHMM
//  2. direction in degrees (up to 3 digits)
//  3. speed in knots (1+ digits)
//  4. lat (4 digits)
//  5. lon (4 or 5 digits)
var motionRe = regexp.MustCompile(`TIME\.\.\.MOT\.\.\.LOC\s+(\d{4})Z\s+(\d{3})DEG\s+(\d+)KT\s+(\d{4})\s+(\d{4,5})\b`)

// ParseStormMotion extracts the first TIME...MOT...LOC block from an NWS alert
// description.
//
// Return semantics:
//   - (nil, nil)  — the description contains no TIME...MOT...LOC header at all.
//     This is expected for alerts without a motion vector (flood watches, etc.)
//     and is not an error.
//   - (nil, err)  — a header was found but parsing failed (regex mismatch,
//     out-of-range direction/lat/lon, invalid HHMM). The caller should log
//     this to catch NWS format drift.
//   - (motion, nil) — success.
//
// issuedAt is the anchor timestamp used to resolve the calendar date of the
// HHMM stamp. NWS encodes only HHMM, no date. Day rollover is bidirectional:
// the result is flipped by ±24h to whichever direction places validAt closest
// to issuedAt within ±12h. This handles both "alert issued just after UTC
// midnight, motion captured just before" and the symmetric forward case, and
// stays correct if the caller passes an anchor (sent_at, effective_at,
// ingested_at) that drifts by up to several hours from the true issuance time.
// Storm-motion captures are always within about an hour of issuance in
// practice.
func ParseStormMotion(description string, issuedAt time.Time) (*StormMotion, error) {
	if !strings.Contains(description, motionHeader) {
		return nil, nil
	}

	m := motionRe.FindStringSubmatch(description)
	if m == nil {
		return nil, fmt.Errorf("parsing storm motion: header present but regex did not match")
	}

	hhmm := m[1]
	hours, err := strconv.Atoi(hhmm[:2])
	if err != nil || hours < 0 || hours > 23 {
		return nil, fmt.Errorf("parsing storm motion: invalid HHMM hour %q", hhmm)
	}
	minutes, err := strconv.Atoi(hhmm[2:])
	if err != nil || minutes < 0 || minutes > 59 {
		return nil, fmt.Errorf("parsing storm motion: invalid HHMM minute %q", hhmm)
	}

	direction, err := strconv.Atoi(m[2])
	if err != nil || direction < 0 || direction >= 360 {
		return nil, fmt.Errorf("parsing storm motion: direction %q out of range [0,360)", m[2])
	}

	speed, err := strconv.Atoi(m[3])
	if err != nil || speed < 0 {
		return nil, fmt.Errorf("parsing storm motion: invalid speed %q", m[3])
	}

	lat, ok := parseLat(m[4])
	if !ok {
		return nil, fmt.Errorf("parsing storm motion: latitude %q out of range", m[4])
	}

	lon, ok := parseLon(m[5])
	if !ok {
		return nil, fmt.Errorf("parsing storm motion: longitude %q out of range", m[5])
	}

	issuedUTC := issuedAt.UTC()
	validAt := time.Date(
		issuedUTC.Year(), issuedUTC.Month(), issuedUTC.Day(),
		hours, minutes, 0, 0, time.UTC,
	)
	// Day rollover: NWS encodes only HHMM, no date. The issuance timestamp
	// anchors which UTC date the TIME belongs to, but it may be off by a few
	// hours (e.g. when callers pass effective_at instead of sent_at). Flip
	// the inferred date by 24h whichever direction places validAt closest to
	// issuedAt within ±12h. Valid storm-motion TIMEs are always within an
	// hour or so of issuance in practice.
	diff := validAt.Sub(issuedUTC)
	switch {
	case diff > 12*time.Hour:
		validAt = validAt.Add(-24 * time.Hour)
	case diff < -12*time.Hour:
		validAt = validAt.Add(24 * time.Hour)
	}

	return &StormMotion{
		OriginLat:    lat,
		OriginLon:    lon,
		DirectionDeg: direction,
		SpeedKt:      speed,
		ValidAt:      validAt,
	}, nil
}

// parseLat converts a 4-digit NWS lat token (DDdd) to decimal degrees. Returns
// false if the value is outside the [0, 90] valid latitude range.
func parseLat(s string) (float64, bool) {
	whole, err := strconv.Atoi(s[:2])
	if err != nil {
		return 0, false
	}
	hundredths, err := strconv.Atoi(s[2:])
	if err != nil {
		return 0, false
	}
	v := float64(whole) + float64(hundredths)/100.0
	if v < 0 || v > 90 {
		return 0, false
	}
	return v, true
}

// parseLon converts a 4- or 5-digit NWS lon token to decimal degrees. CONUS
// West is implicit — the result is always negated. Returns false if the
// absolute value is outside the [0, 180] valid longitude range.
func parseLon(s string) (float64, bool) {
	cut := len(s) - 2
	whole, err := strconv.Atoi(s[:cut])
	if err != nil {
		return 0, false
	}
	hundredths, err := strconv.Atoi(s[cut:])
	if err != nil {
		return 0, false
	}
	v := float64(whole) + float64(hundredths)/100.0
	if v < 0 || v > 180 {
		return 0, false
	}
	return -v, true
}
