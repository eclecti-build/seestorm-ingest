package store

import (
	"testing"
	"time"
)

// aa builds an ActiveAlertGeoJSON for collapse tests. effMin/expMin are minute
// offsets from a fixed base so issuance ordering is explicit and deterministic.
func aa(nwsID, eventID, eventType, area string, effMin, expMin int) ActiveAlertGeoJSON {
	base := time.Date(2026, 5, 30, 20, 0, 0, 0, time.UTC)
	return ActiveAlertGeoJSON{
		NWSID:       nwsID,
		EventType:   eventType,
		AreaDesc:    area,
		EffectiveAt: base.Add(time.Duration(effMin) * time.Minute),
		ExpiresAt:   base.Add(time.Duration(expMin) * time.Minute),
		eventID:     eventID,
	}
}

func ids(alerts []ActiveAlertGeoJSON) []string {
	out := make([]string, len(alerts))
	for i, a := range alerts {
		out[i] = a.NWSID
	}
	return out
}

func TestCollapseByEvent(t *testing.T) {
	t.Parallel()

	t.Run("collapses re-issued messages of one event to the latest", func(t *testing.T) {
		t.Parallel()
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("older", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 0, 30),
			aa("newest", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 50, 80),
			aa("middle", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 25, 55),
		})
		if len(out) != 1 {
			t.Fatalf("want 1 row, got %d (%v)", len(out), ids(out))
		}
		if out[0].NWSID != "newest" {
			t.Fatalf("want newest survivor, got %q", out[0].NWSID)
		}
	})

	t.Run("distinct event ids both survive", func(t *testing.T) {
		t.Parallel()
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("a", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 0, 30),
			aa("b", "KIND.FL.W.0087", "Flood Warning", "Gibson, IN", 0, 30),
		})
		if len(out) != 2 {
			t.Fatalf("want 2 rows, got %d (%v)", len(out), ids(out))
		}
	})

	t.Run("non-VTEC rows are never collapsed even when event+area match", func(t *testing.T) {
		t.Parallel()
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("x", "", "Special Weather Statement", "Cooper, MO", 0, 30),
			aa("y", "", "Special Weather Statement", "Cooper, MO", 10, 40),
		})
		if len(out) != 2 {
			t.Fatalf("want 2 rows (no collapse without a VTEC event id), got %d (%v)", len(out), ids(out))
		}
	})

	t.Run("deterministic tie-break: greater nws_id wins, order-independent", func(t *testing.T) {
		t.Parallel()
		mk := func(order ...string) []ActiveAlertGeoJSON {
			in := make([]ActiveAlertGeoJSON, 0, len(order))
			for _, id := range order {
				in = append(in, aa(id, "KIND.FL.W.0102", "Flood Warning", "Vernon, MO", 28, 30))
			}
			return in
		}
		a := collapseByEvent(mk("aaa", "zzz"))
		b := collapseByEvent(mk("zzz", "aaa"))
		if len(a) != 1 || len(b) != 1 {
			t.Fatalf("want 1 row each, got %d and %d", len(a), len(b))
		}
		if a[0].NWSID != "zzz" || b[0].NWSID != "zzz" {
			t.Fatalf("tie-break not deterministic: got %q and %q (want zzz)", a[0].NWSID, b[0].NWSID)
		}
	})

	t.Run("same event id but DIFFERENT footprint is never collapsed (fail-safe: never hide a live footprint)", func(t *testing.T) {
		t.Parallel()
		// Two active rows that share a VTEC event id but cover different
		// county footprints might be a footprint-shrinking continuation OR two
		// genuinely-distinct still-active segments — we cannot tell them apart
		// from the snapshot alone. On a public-safety feed the safe direction
		// is to keep both (at worst over-show a stale footprint) rather than
		// risk dropping a live one. So the collapse key includes area_desc.
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("a", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN; Lawrence, IN", 0, 30),
			aa("b", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 50, 80),
		})
		if len(out) != 2 {
			t.Fatalf("want 2 rows (differing footprint must not collapse), got %d (%v)", len(out), ids(out))
		}
	})

	t.Run("a later Severe Weather Statement must NOT replace the warning it updates", func(t *testing.T) {
		t.Parallel()
		// An SVS continues a Tornado/Severe Thunderstorm Warning and carries
		// the SAME P-VTEC event id, but event="Severe Weather Statement". If
		// we collapsed on (eventID, area) alone the newer SVS would win and the
		// active Tornado Warning would be published as a mere statement —
		// under-classifying a live warning. event_type is part of the key, so
		// the two never collapse and the warning is preserved intact.
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("warn", "KMKX.TO.W.0042", "Tornado Warning", "Dane, WI", 0, 30),
			aa("svs", "KMKX.TO.W.0042", "Severe Weather Statement", "Dane, WI", 10, 30),
		})
		if len(out) != 2 {
			t.Fatalf("want 2 rows (warning must not be replaced by its SVS), got %d (%v)", len(out), ids(out))
		}
		var sawWarning bool
		for _, a := range out {
			if a.EventType == "Tornado Warning" {
				sawWarning = true
			}
		}
		if !sawWarning {
			t.Fatalf("active Tornado Warning was dropped/downgraded; out=%v", ids(out))
		}
	})

	t.Run("same event id AND same footprint collapses to latest", func(t *testing.T) {
		t.Parallel()
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("old", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 0, 30),
			aa("new", "KIND.FL.W.0102", "Flood Warning", "Jackson, IN", 50, 80),
		})
		if len(out) != 1 {
			t.Fatalf("want 1 row, got %d (%v)", len(out), ids(out))
		}
		if out[0].NWSID != "new" {
			t.Fatalf("want new survivor, got %q", out[0].NWSID)
		}
	})

	t.Run("preserves first-seen order of survivors", func(t *testing.T) {
		t.Parallel()
		out := collapseByEvent([]ActiveAlertGeoJSON{
			aa("p", "KIND.FL.W.0001", "Flood Warning", "A", 0, 30),
			aa("q", "", "Special Weather Statement", "B", 0, 30),
			aa("r", "KIND.FL.W.0002", "Flood Warning", "C", 0, 30),
		})
		if got := ids(out); len(got) != 3 || got[0] != "p" || got[1] != "q" || got[2] != "r" {
			t.Fatalf("order not preserved: %v", got)
		}
	})
}
