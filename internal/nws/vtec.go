// Package nws — VTEC (Valid Time Event Code) parsing.
//
// NWS embeds a P-VTEC string in the alert's `parameters.VTEC` field, e.g.:
//
//	/O.CON.KIND.FL.W.0102.000000T0000Z-260531T1800Z/
//
// Decoded:
//
//	O      product class   (O=operational, T=test, E/X=experimental)
//	CON    action          (NEW, CON, COR, CAN, EXP, EXT, UPG, ROU, …)
//	KIND   issuing office
//	FL     phenomenon      (FL=flood, SV=severe tstorm, TO=tornado, …)
//	W      significance    (W=warning, A=watch, Y=advisory, S=statement)
//	0102   ETN             (event tracking number)
//	…      begin-end times
//
// The tuple office.phenomenon.significance.ETN (here KIND.FL.W.0102) is the
// stable identity of a *logical* warning: a continuation/extension/correction
// of the same event reuses it while the CAP message id (`nws_id`) changes on
// every message. EventID() exposes that tuple so the snapshot builder can keep
// only the latest message per event. See
// docs/superpowers/specs/2026-05-31-alert-duplicate-supersession-design.md.
//
// Return semantics mirror the other nws parsers (ParseEventMotion, etc.):
//
//	(nil, nil) — no VTEC field present, or only blank values (the norm for
//	             watches, statements, and other non-VTEC products)
//	(nil, err) — a non-blank VTEC value was present but no parseable P-VTEC
//	             was found (format drift); caller logs and ships the row
//	(v,   nil) — parsed OK
package nws

import (
	"fmt"
	"regexp"
	"strings"
)

// VTEC is the decoded operational P-VTEC of an alert.
type VTEC struct {
	ProductClass string // O, T, E, X
	Action       string // NEW, CON, COR, CAN, EXP, EXT, UPG, ROU, …
	Office       string // 4-letter issuing office, e.g. KIND
	Phenomenon   string // 2-letter phenomenon, e.g. FL
	Significance string // 1-letter significance, e.g. W
	ETN          string // 4-digit event tracking number, e.g. 0102
}

// EventID returns the stable logical-warning identity
// office.phenomenon.significance.ETN (e.g. "KIND.FL.W.0102"). Two CAP messages
// that share an EventID are the same warning re-issued.
//
// Known limitation (P-VTEC year aliasing): ETNs are unique only within a
// calendar year per office, so a warning continuously active across a New Year
// boundary could in principle share an EventID with a distinct new event that
// reuses the same ETN. We deliberately do NOT fold a year into the identity:
// the onset year is the only correct discriminator, and NWS zeroes the begin
// time on continuation messages (`.000000T0000Z-…`, verified in production), so
// it isn't recoverable from a continuation's P-VTEC — adding it would split a
// live event's own NEW/CON messages and defeat the collapse. The robust fix is
// references-based supersession (PR2), which keys on explicit message links and
// is immune to ETN recycling. The residual collision is narrow (same
// office+phenomenon+significance+ETN+event_type+area_desc, one event active a
// full year) and the collapse key also includes event_type + area_desc, which
// shrinks it further. See the design doc's PR2 section.
func (v *VTEC) EventID() string {
	return v.Office + "." + v.Phenomenon + "." + v.Significance + "." + v.ETN
}

// pvtecRe matches the leading, fixed-width portion of an operational P-VTEC
// string up to and including the ETN. The trailing begin/end time block is not
// needed for event identity and is intentionally left unmatched.
//
// Groups: 1=class 2=action 3=office 4=phenomenon 5=significance 6=ETN.
var pvtecRe = regexp.MustCompile(`/([OTEX])\.([A-Z]{3})\.([A-Z]{4})\.([A-Z]{2})\.([A-Z])\.(\d{4})\.`)

// ParseVTEC extracts the operational P-VTEC from the NWS `parameters` map.
// When the field carries multiple codes (P-VTEC plus a hydrologic H-VTEC) the
// first element that parses as a P-VTEC wins; the H-VTEC is ignored.
func ParseVTEC(params map[string][]string) (*VTEC, error) {
	if params == nil {
		return nil, nil
	}
	raw, ok := params["VTEC"]
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	sawNonBlank := false
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		sawNonBlank = true
		m := pvtecRe.FindStringSubmatch(s)
		if m == nil {
			continue
		}
		return &VTEC{
			ProductClass: m[1],
			Action:       m[2],
			Office:       m[3],
			Phenomenon:   m[4],
			Significance: m[5],
			ETN:          m[6],
		}, nil
	}

	if !sawNonBlank {
		return nil, nil
	}
	return nil, fmt.Errorf("VTEC present but no parseable P-VTEC: %q", raw)
}
