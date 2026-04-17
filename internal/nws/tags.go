// Package nws also provides a parser for NWS Impact-Based Warning (IBW)
// threat tags embedded in Tornado / Severe Thunderstorm / Flash Flood warning
// descriptions.
//
// The NWS wraps the structured tag block with a `&&` delimiter — prose
// appears above, tag key/value pairs appear below, and the block is
// terminated by a second `&&` or end-of-text. Typical shape:
//
//	...prose...
//
//	&&
//
//	TORNADO...OBSERVED
//	TORNADO DAMAGE THREAT...CONSIDERABLE
//	HAIL...1.75IN
//	MAX HAIL SIZE...1.75 IN
//
//	&&
//
// Not every warning issues every tag. The struct keeps missing values as the
// empty string (omitempty on the JSON tags keeps the snapshot small).
//
// This parser is deliberately permissive about exact whitespace and casing
// in keys — NWS forecasters are humans and the format drifts — but it does
// not normalize *values*. Downstream consumers see the raw NWS string so the
// client can build stable lookup tables against the official IBW spec.
package nws

import (
	"regexp"
	"strings"
)

// WarningTags captures the structured threat tags from an IBW-compliant
// warning description. Every field is a string so "missing" and "empty"
// collapse to the same state — callers gate display on `value != ""`.
//
// The canonical source for the tag key list is the NWS Impact-Based Warning
// specification. The subset here is what actually appears in WI warnings
// today (Tornado / Severe Thunderstorm / Flash Flood). Adding a new tag is
// a one-line addition to tagKeys below.
type WarningTags struct {
	// Tornado-warning tags
	Tornado             string `json:"tornado,omitempty"`               // OBSERVED | POSSIBLE | RADAR INDICATED
	TornadoDamageThreat string `json:"tornado_damage_threat,omitempty"` // CONSIDERABLE | DESTRUCTIVE | CATASTROPHIC

	// Severe-thunderstorm-warning tags
	Hail                     string `json:"hail,omitempty"`                       // e.g. "1.75IN" or "<.75IN"
	MaxHailSize              string `json:"max_hail_size,omitempty"`              // e.g. "1.75 IN"
	Wind                     string `json:"wind,omitempty"`                       // e.g. "60MPH"
	MaxWindGust              string `json:"max_wind_gust,omitempty"`              // e.g. "60 MPH"
	ThunderstormDamageThreat string `json:"thunderstorm_damage_threat,omitempty"` // CONSIDERABLE | DESTRUCTIVE

	// Flash-flood-warning tags
	FlashFlood             string `json:"flash_flood,omitempty"`               // RADAR INDICATED | OBSERVED
	FlashFloodDamageThreat string `json:"flash_flood_damage_threat,omitempty"` // CONSIDERABLE | CATASTROPHIC
	ExpectedRainfallRate   string `json:"expected_rainfall_rate,omitempty"`    // e.g. "2-3 INCHES IN 1 HOUR"

	// Shared / advisory tags
	SpotterActivation string `json:"spotter_activation,omitempty"` // REQUESTED | NOT REQUESTED
	Source            string `json:"source,omitempty"`             // e.g. "RADAR INDICATED", "TRAINED WEATHER SPOTTERS"
}

// IsEmpty reports whether every tag field is the empty string. Used by
// callers that want to distinguish "warning parsed but had no tags" from
// "warning had tags." Useful for dev-time drift detection.
func (t *WarningTags) IsEmpty() bool {
	return t == nil || (t.Tornado == "" &&
		t.TornadoDamageThreat == "" &&
		t.Hail == "" &&
		t.MaxHailSize == "" &&
		t.Wind == "" &&
		t.MaxWindGust == "" &&
		t.ThunderstormDamageThreat == "" &&
		t.FlashFlood == "" &&
		t.FlashFloodDamageThreat == "" &&
		t.ExpectedRainfallRate == "" &&
		t.SpotterActivation == "" &&
		t.Source == "")
}

// tagKey maps the raw NWS tag key (normalized: upper-case, single-spaced)
// to a pointer assignment on the WarningTags struct. Adding a new tag is
// one entry here plus one struct field above.
type tagKey struct {
	key    string
	assign func(t *WarningTags, v string)
}

var tagKeys = []tagKey{
	{"TORNADO", func(t *WarningTags, v string) { t.Tornado = v }},
	{"TORNADO DAMAGE THREAT", func(t *WarningTags, v string) { t.TornadoDamageThreat = v }},
	{"HAIL", func(t *WarningTags, v string) { t.Hail = v }},
	{"MAX HAIL SIZE", func(t *WarningTags, v string) { t.MaxHailSize = v }},
	{"WIND", func(t *WarningTags, v string) { t.Wind = v }},
	{"MAX WIND GUST", func(t *WarningTags, v string) { t.MaxWindGust = v }},
	{"THUNDERSTORM DAMAGE THREAT", func(t *WarningTags, v string) { t.ThunderstormDamageThreat = v }},
	{"FLASH FLOOD", func(t *WarningTags, v string) { t.FlashFlood = v }},
	{"FLASH FLOOD DAMAGE THREAT", func(t *WarningTags, v string) { t.FlashFloodDamageThreat = v }},
	{"EXPECTED RAINFALL RATE", func(t *WarningTags, v string) { t.ExpectedRainfallRate = v }},
	{"SPOTTER ACTIVATION", func(t *WarningTags, v string) { t.SpotterActivation = v }},
	{"SOURCE", func(t *WarningTags, v string) { t.Source = v }},
}

// tagLineRe captures one KEY...VALUE pair on a line. The key side matches
// letters, spaces, and slashes (e.g. "MAX HAIL SIZE"). The separator is
// three or more dots — NWS historically uses exactly three but we accept
// `...+` defensively for format drift. The value runs to end-of-line.
//
// We apply this per-line rather than over the whole block so that line
// ordering is preserved and stray tokens on the prose side of `&&` are
// rejected by the line-anchored regex.
var tagLineRe = regexp.MustCompile(`^([A-Z][A-Z /]*[A-Z])\.{3,}(.+)$`)

// delimiter is the NWS-canonical section separator for the tag block. It
// appears on its own line between prose and tags, and again after the tag
// block. We locate the FIRST `&&` and parse everything after it; a second
// `&&` (if present) terminates the block.
const delimiter = "&&"

// ParseWarningTags extracts IBW threat tags from an NWS warning description.
//
// Return semantics:
//   - (tags, nil)  — at least one recognized tag was parsed. tags may have
//     empty fields for any tag the warning did not issue.
//   - (nil, nil)   — no `&&` delimiter present, OR the delimiter was present
//     but no recognized tag lines appeared after it. This is the expected
//     case for non-warning products (Watches, Statements) and for warnings
//     issued without IBW tagging. Not an error.
//   - (nil, err)   — the description had a tag block but parsing of a
//     specific line failed in a way the caller should know about. In
//     practice the parser is forgiving enough that this is rare; reserved
//     for future use if we tighten validation.
//
// Unrecognized tag keys are silently skipped so NWS can introduce new tags
// without breaking the snapshot — they will simply not appear in the JSON
// until we add a field above.
func ParseWarningTags(description string) (*WarningTags, error) {
	// Fast absence check — the overwhelming majority of non-warning alerts
	// have no `&&` block, so skip regex work entirely.
	_, block, ok := strings.Cut(description, delimiter)
	if !ok {
		return nil, nil
	}

	// The tag block runs to the NEXT `&&` (if present) or to EOF.
	if before, _, ok := strings.Cut(block, delimiter); ok {
		block = before
	}

	tags := &WarningTags{}
	for line := range strings.SplitSeq(block, "\n") {
		// NWS wraps tag lines — a single tag can span multiple physical
		// lines when the value is long (e.g. SOURCE...). We take only
		// the header line for now; continuation handling is a future
		// refinement if we see wrapped values in practice.
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		m := tagLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}

		key := normalizeKey(m[1])
		value := strings.TrimSpace(m[2])
		if value == "" {
			continue
		}

		for _, tk := range tagKeys {
			if tk.key == key {
				tk.assign(tags, value)
				break
			}
		}
	}

	if tags.IsEmpty() {
		return nil, nil
	}
	return tags, nil
}

// normalizeKey collapses runs of whitespace to a single space and upper-cases
// the result. NWS tag keys are canonical upper-case with single spaces, but
// format drift (tabs, double spaces) should not cause silent misses.
func normalizeKey(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}
