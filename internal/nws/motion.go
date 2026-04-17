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
// ParseStormMotion is a pure parser: failure cases (no match, malformed
// numerics, out-of-range lat/lon/direction) return nil. There is no error
// channel — a nil return means "no motion data available for this alert".
package nws

import (
	"regexp"
	"strconv"
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

// motionRe matches the TIME...MOT...LOC header block. Capture groups:
//  1. HHMM
//  2. direction in degrees (up to 3 digits)
//  3. speed in knots (1+ digits)
//  4. lat (4 digits)
//  5. lon (4 or 5 digits)
var motionRe = regexp.MustCompile(`TIME\.\.\.MOT\.\.\.LOC\s+(\d{4})Z\s+(\d{3})DEG\s+(\d+)KT\s+(\d{4})\s+(\d{4,5})`)

// ParseStormMotion extracts the first TIME...MOT...LOC block from an NWS alert
// description. Returns nil when the block is missing or malformed.
//
// issuedAt is used only to resolve the calendar date of the HHMM stamp. If the
// naively-combined date-plus-time lands more than an hour in the future
// relative to issuedAt, the result is rolled back by 24h to handle the case
// where the alert was issued just after UTC midnight but the motion snapshot
// was taken just before.
func ParseStormMotion(description string, issuedAt time.Time) *StormMotion {
	m := motionRe.FindStringSubmatch(description)
	if m == nil {
		return nil
	}

	hhmm := m[1]
	hours, err := strconv.Atoi(hhmm[:2])
	if err != nil || hours < 0 || hours > 23 {
		return nil
	}
	minutes, err := strconv.Atoi(hhmm[2:])
	if err != nil || minutes < 0 || minutes > 59 {
		return nil
	}

	direction, err := strconv.Atoi(m[2])
	if err != nil || direction < 0 || direction >= 360 {
		return nil
	}

	speed, err := strconv.Atoi(m[3])
	if err != nil || speed < 0 {
		return nil
	}

	lat, ok := parseLat(m[4])
	if !ok {
		return nil
	}

	lon, ok := parseLon(m[5])
	if !ok {
		return nil
	}

	issuedUTC := issuedAt.UTC()
	validAt := time.Date(
		issuedUTC.Year(), issuedUTC.Month(), issuedUTC.Day(),
		hours, minutes, 0, 0, time.UTC,
	)
	// Day-rollover: if naive combine is more than 1h in the future, the HHMM
	// was captured the previous UTC day (alert issued just after midnight Z).
	if validAt.Sub(issuedUTC) > time.Hour {
		validAt = validAt.Add(-24 * time.Hour)
	}

	return &StormMotion{
		OriginLat:    lat,
		OriginLon:    lon,
		DirectionDeg: direction,
		SpeedKt:      speed,
		ValidAt:      validAt,
	}
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
