package store

// collapseByEvent removes superseded duplicate alerts before they reach the
// snapshot. NWS issues a fresh CAP message (new nws_id) for every continuation,
// extension, or correction of a warning while reusing the VTEC event identity
// (office.phenomenon.significance.ETN, see nws.ParseVTEC). Nothing upstream
// retires the prior message, so without this pass the snapshot carries one row
// per message — N stacked polygons, N motion arrows, and N list cards for a
// single storm. See
// docs/superpowers/specs/2026-05-31-alert-duplicate-supersession-design.md.
//
// Rows are collapsed only when they share ALL THREE of: a non-empty VTEC
// eventID, the same event_type, and an identical area_desc; the surviving row
// is the latest issuance (see laterIssuance). Rows with an empty eventID —
// every non-VTEC product (watches, statements, advisories without a P-VTEC) —
// are NEVER grouped and always pass through untouched. Survivors keep their
// first-seen order, so the caller's `ORDER BY effective_at DESC` is preserved.
//
// Why area_desc is part of the key (fail-safe): two active rows that share an
// eventID but cover different county footprints cannot be told apart from the
// snapshot alone — it may be a footprint-shrinking continuation, or two
// genuinely-distinct still-active segments that happen to reuse the
// office/phenomenon/significance/ETN tuple. Collapsing them risks dropping a
// live footprint, the worst failure for a public-safety feed. Keeping both at
// worst over-shows a slightly-stale footprint until it expires (over-warning,
// the safe direction).
//
// Why event_type is part of the key: a Severe Weather Statement (SVS) follows
// up an active Tornado / Severe Thunderstorm Warning and carries the SAME VTEC
// event id (it updates the same warning) but event="Severe Weather Statement".
// Keying on event_type keeps the warning and its SVS as separate rows, so a
// later SVS can never replace — and thereby downgrade — the active warning it
// updates. (Folding an SVS into its parent warning is supersession logic, which
// belongs to PR2, not this snapshot-time dedup.)
//
// What remains collapsed is exactly the real-world duplication this fixes:
// same-event, same-type, same-footprint re-issuances under a fresh nws_id.
func collapseByEvent(alerts []ActiveAlertGeoJSON) []ActiveAlertGeoJSON {
	// winners maps the collapse key -> index into `out` of the surviving row.
	winners := make(map[string]int, len(alerts))
	out := make([]ActiveAlertGeoJSON, 0, len(alerts))

	for _, a := range alerts {
		if a.eventID == "" {
			out = append(out, a)
			continue
		}
		// NUL separates the fields so distinct (eventID, event_type, area_desc)
		// tuples can't alias each other (none of these can contain NUL).
		key := a.eventID + "\x00" + a.EventType + "\x00" + a.AreaDesc
		idx, seen := winners[key]
		if !seen {
			winners[key] = len(out)
			out = append(out, a)
			continue
		}
		if laterIssuance(a, out[idx]) {
			out[idx] = a
		}
	}

	return out
}

// laterIssuance reports whether candidate is a more recent issuance of the same
// VTEC event than incumbent: newer EffectiveAt wins; ties fall through to later
// ExpiresAt, then to the greater NWSID so the result is deterministic
// regardless of input order.
func laterIssuance(candidate, incumbent ActiveAlertGeoJSON) bool {
	if !candidate.EffectiveAt.Equal(incumbent.EffectiveAt) {
		return candidate.EffectiveAt.After(incumbent.EffectiveAt)
	}
	if !candidate.ExpiresAt.Equal(incumbent.ExpiresAt) {
		return candidate.ExpiresAt.After(incumbent.ExpiresAt)
	}
	return candidate.NWSID > incumbent.NWSID
}
