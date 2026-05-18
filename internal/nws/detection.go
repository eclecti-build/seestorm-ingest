// Package nws also provides a parser that resolves the tornado DETECTION
// state of a warning — "radar indicated" vs "confirmed on the ground" —
// from the structured NWS payload.
//
// NWS encodes two ORTHOGONAL axes for a Tornado Warning. Collapsing them
// into one label is exactly the semantic drift this parser exists to
// prevent, so they are kept as separate fields:
//
//  1. Detection — IS there a tornado?
//     RADAR_INDICATED — rotation seen on radar; the tornado is NOT
//     confirmed.
//     OBSERVED        — a tornado is confirmed ongoing. Confirmation may
//     be a dual-pol radar debris signature (a "debris
//     ball" / TDS) OR a reliable ground report.
//     OBSERVED does NOT imply a human saw it — a radar
//     debris ball is also OBSERVED.
//  2. DamageThreat — how catastrophic if it hits?
//     BASE         — default Tornado Warning.
//     CONSIDERABLE — a "Particularly Dangerous Situation" (PDS).
//     CATASTROPHIC — a "Tornado Emergency".
//
// Source priority (most authoritative first):
//
//  1. properties.parameters.tornadoDetection / tornadoDamageThreat — the
//     machine-readable fields api.weather.gov reliably emits on BOTH the
//     original Tornado Warning and its Severe Weather Statement (SVS)
//     updates. This is the canonical source. It matters because an SVS
//     update — the message that typically escalates RADAR INDICATED →
//     OBSERVED, or to PDS/Emergency — carries the SVS narrative format and
//     NO `&&` tag block, so the legacy tag path below cannot see it.
//  2. The `&&` IBW tag block (TORNADO...X / TORNADO DAMAGE THREAT...Y),
//     reusing ParseWarningTags. Legacy / belt-and-suspenders for products
//     that still ship the raw tag block but (rarely) lack the parameter.
//  3. A DELIBERATELY CONSERVATIVE scan of the SVS narrative. This tier is
//     gated on a `HAZARD...Tornado` line and only ever yields
//     RADAR_INDICATED — it will NEVER infer OBSERVED from free text.
//     Rationale: a false "confirmed tornado" is the dangerous direction
//     (cry-wolf, eroded trust); under-claiming is safe. OBSERVED must
//     always come from a structured source.
//
// Anti-drift contract for downstream consumers:
//   - NEVER render RADAR_INDICATED as "on the ground" / "touchdown".
//   - NEVER render OBSERVED as "a spotter saw it" unless SourceText
//     explicitly attributes a human source.
//   - "Tornado Emergency" (CATASTROPHIC) is its own name, not a louder
//     Tornado Warning.
//
// POSSIBLE — whether as the structured `tornadoDetection` parameter or the
// `TORNADO...POSSIBLE` tag — is intentionally NOT a detection level: it is
// the embedded-tornado signal of a *Severe Thunderstorm* Warning, not a
// Tornado Warning. It is a KNOWN value that resolves to "no detection"
// rather than overclaim, and is NOT treated as format drift.
package nws

import (
	"fmt"
	"regexp"
	"strings"
)

// TornadoDetectionState is the normalized, closed enumeration of the
// tornado detection axis. Downstream consumers build stable lookup tables
// against THESE values, never against raw NWS strings.
type TornadoDetectionState string

const (
	// DetectionRadarIndicated — rotation on radar, tornado NOT confirmed.
	DetectionRadarIndicated TornadoDetectionState = "RADAR_INDICATED"
	// DetectionObserved — tornado confirmed (radar debris signature OR
	// reliable report). Confirmed != "a human saw it".
	DetectionObserved TornadoDetectionState = "OBSERVED"
)

// TornadoDamageThreatLevel is the normalized, closed enumeration of the
// damage-threat axis. Per the NWS Impact-Based Warning spec the tornado
// damage threat tag is only ever CONSIDERABLE or CATASTROPHIC; its absence
// is the BASE (default) Tornado Warning. DESTRUCTIVE is a *thunderstorm*
// threat and is deliberately not a member here.
type TornadoDamageThreatLevel string

const (
	DamageThreatBase         TornadoDamageThreatLevel = "BASE"
	DamageThreatConsiderable TornadoDamageThreatLevel = "CONSIDERABLE"
	DamageThreatCatastrophic TornadoDamageThreatLevel = "CATASTROPHIC"
)

// TornadoDetection is the derived, normalized detection record attached to
// the snapshot. It is additive and optional — a nil pointer (omitempty)
// means "not a tornado warning, or no detection information present", which
// is the expected state for the overwhelming majority of alerts.
type TornadoDetection struct {
	// Detection is the certainty axis. Always one of the closed enum
	// values when this struct is non-nil.
	Detection TornadoDetectionState `json:"detection"`
	// Confirmed is a convenience mirror of (Detection == OBSERVED). It is
	// surfaced explicitly because "is the tornado confirmed?" is the
	// single question the UI most needs to answer unambiguously.
	Confirmed bool `json:"confirmed"`
	// DamageThreat is the impact axis. Defaults to BASE.
	DamageThreat TornadoDamageThreatLevel `json:"damage_threat"`
	// SourceText is the raw, human-readable NWS source phrase (e.g.
	// "Radar indicated rotation", "TRAINED WEATHER SPOTTERS") for display
	// only. It is the ONLY field a consumer may use to attribute a human
	// observer; the Detection enum must not be narrativized.
	SourceText string `json:"source_text,omitempty"`
}

// hazardLineRe extracts the value of the SVS narrative HAZARD... line.
var hazardLineRe = regexp.MustCompile(`(?i)\bHAZARD\.{2,}\s*([^\n]+)`)

// svrHazardExclusionRe rejects a HAZARD value where the tornado is only a
// *possibility* or is secondary to wind/hail — i.e. a Severe Thunderstorm
// Warning's embedded-tornado wording ("...60 mph wind gusts and a tornado
// possible"). Without this, the bare "mentions tornado" check let tier-3
// narrate an SVR into a RADAR_INDICATED tornado — the exact overclaim the
// fallback exists to prevent.
var svrHazardExclusionRe = regexp.MustCompile(`(?i)\b(possible|wind|hail|mph|gust)\b`)

// isTornadoHazard reports whether the SVS narrative HAZARD line scopes the
// statement to an actual ongoing tornado (e.g. "HAZARD...Tornado.",
// "HAZARD...Damaging tornado.") rather than merely mentioning a tornado as
// possible or secondary in a Severe Thunderstorm Warning. Deliberately
// strict: tier-3 is a last resort and under-claiming is the safe failure.
func isTornadoHazard(description string) bool {
	m := hazardLineRe.FindStringSubmatch(description)
	if m == nil {
		return false
	}
	haz := m[1]
	if !strings.Contains(strings.ToLower(haz), "tornado") {
		return false
	}
	return !svrHazardExclusionRe.MatchString(haz)
}

// sourceLineRe captures the value of the SVS narrative "SOURCE..." line up
// to end-of-line. NWS canonically uses three dots; we accept two-or-more
// defensively, matching the tolerance in tags.go.
var sourceLineRe = regexp.MustCompile(`(?i)\bSOURCE\.{2,}\s*([^\n]+)`)

// DetectTornado resolves the tornado detection/damage-threat state of an
// alert from its structured parameters and description.
//
// Return semantics (mirrors ParseEventMotion / ParseWarningTags):
//
//   - (nil, nil)  — not a tornado warning, or no detection information in
//     any source. Expected for watches, statements, and every
//     non-tornado product. Not an error.
//   - (nil, err)  — a tornado detection token WAS present but was not a
//     recognized value (NWS introduced a new one / format drift). The
//     caller should log this; the alert still belongs in the snapshot.
//   - (det, nil)  — resolved. det.Detection is always a valid enum value.
//
// params is the NWS `properties.parameters` map; description is the alert
// `properties.description`. Either may be empty.
func DetectTornado(params map[string][]string, description string) (*TornadoDetection, error) {
	det := &TornadoDetection{DamageThreat: DamageThreatBase}
	resolved := false

	// --- Tier 1: structured parameters (authoritative) ---------------
	if raw, ok := firstParam(params, "tornadoDetection"); ok {
		switch normalizeToken(raw) {
		case "RADAR INDICATED":
			det.Detection = DetectionRadarIndicated
			resolved = true
		case "OBSERVED":
			det.Detection = DetectionObserved
			resolved = true
		case "POSSIBLE":
			// A KNOWN value, but not a Tornado Warning detection level:
			// `tornadoDetection...POSSIBLE` is the embedded-tornado
			// parameter of a *Severe Thunderstorm* Warning. Resolve to
			// "no tornado detection" (leave unresolved → (nil, nil))
			// rather than overclaim OR mistreat a known value as drift.
			// Mirrors the Tier-2 tag path, which also drops POSSIBLE. SVR
			// warnings with embedded tornado potential are common during
			// an outbreak; erroring here would bury the detFailed drift
			// signal under expected noise.
		default:
			// A present-but-unrecognized value is genuine drift worth
			// surfacing.
			return nil, fmt.Errorf("parsing tornado detection: unrecognized tornadoDetection %q", raw)
		}
	}
	if raw, ok := firstParam(params, "tornadoDamageThreat"); ok {
		// Damage threat is secondary; an unrecognized value degrades to
		// BASE rather than failing the whole detection.
		switch normalizeToken(raw) {
		case "CONSIDERABLE":
			det.DamageThreat = DamageThreatConsiderable
		case "CATASTROPHIC":
			det.DamageThreat = DamageThreatCatastrophic
		}
	}

	// --- Tier 2: the `&&` IBW tag block ------------------------------
	// Reuse the battle-tested tag parser. POSSIBLE is intentionally not a
	// detection level (it is the Severe-Thunderstorm embedded-tornado
	// tag), so it neither resolves nor overclaims.
	tags, _ := ParseWarningTags(description)
	if tags != nil {
		if !resolved {
			switch normalizeToken(tags.Tornado) {
			case "":
				// No TORNADO... tag on this warning (e.g. an SVR carrying
				// only hail/wind tags). Not a detection, not drift.
			case "RADAR INDICATED":
				det.Detection = DetectionRadarIndicated
				resolved = true
			case "OBSERVED":
				det.Detection = DetectionObserved
				resolved = true
			case "POSSIBLE":
				// SVR embedded-tornado tag — a KNOWN non-detection,
				// mirroring the structured Tier-1 POSSIBLE handling.
			default:
				// Symmetric with Tier-1: a non-empty, unrecognized tag
				// value is genuine NWS format drift. Surface it so
				// detFailed counts it instead of the detection silently
				// vanishing (the documented return contract).
				return nil, fmt.Errorf("parsing tornado detection: unrecognized TORNADO tag %q", tags.Tornado)
			}
		}
		if det.DamageThreat == DamageThreatBase {
			switch normalizeToken(tags.TornadoDamageThreat) {
			case "CONSIDERABLE":
				det.DamageThreat = DamageThreatConsiderable
			case "CATASTROPHIC":
				det.DamageThreat = DamageThreatCatastrophic
			}
		}
		if det.SourceText == "" && strings.TrimSpace(tags.Source) != "" {
			det.SourceText = strings.TrimSpace(tags.Source)
		}
	}

	// Enrich SourceText from the narrative SOURCE... line for display,
	// regardless of which tier resolved the detection. This is the only
	// field permitted to attribute a human observer.
	narrativeSource := ""
	if m := sourceLineRe.FindStringSubmatch(description); m != nil {
		narrativeSource = strings.TrimRight(strings.TrimSpace(m[1]), ".")
	}
	if det.SourceText == "" && narrativeSource != "" {
		det.SourceText = narrativeSource
	}

	// --- Tier 3: conservative narrative inference --------------------
	// Only fires when nothing structured resolved, only inside a tornado
	// HAZARD context, and ONLY in the under-claiming direction. A false
	// "confirmed" is the dangerous failure mode; a missed confirmation
	// degrades gracefully to "radar indicated".
	if !resolved && isTornadoHazard(description) {
		if strings.Contains(strings.ToUpper(narrativeSource), "RADAR INDICATED") {
			det.Detection = DetectionRadarIndicated
			resolved = true
		}
	}

	if !resolved {
		return nil, nil
	}
	det.Confirmed = det.Detection == DetectionObserved
	return det, nil
}

// firstParam returns the first non-blank entry for key in the NWS
// parameters map. The second return is false when the key is absent or
// every entry is blank — treated identically to "not present", matching
// ParseEventMotion's handling of empty values.
func firstParam(params map[string][]string, key string) (string, bool) {
	if len(params) == 0 {
		return "", false
	}
	entries, ok := params[key]
	if !ok {
		return "", false
	}
	for _, e := range entries {
		if strings.TrimSpace(e) != "" {
			return e, true
		}
	}
	return "", false
}

// normalizeToken upper-cases and collapses internal whitespace so
// "radar  indicated" / "Radar Indicated" / "RADAR INDICATED" all compare
// equal. NWS values are canonically upper single-spaced, but forecasters
// are human and the format drifts (see tags.go's normalizeKey).
func normalizeToken(s string) string {
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}
