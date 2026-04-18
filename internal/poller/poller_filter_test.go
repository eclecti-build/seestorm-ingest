package poller

import (
	"reflect"
	"testing"

	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

func TestFilterAlertsByState(t *testing.T) {
	t.Parallel()

	a := func(id string, states ...string) store.ActiveAlertGeoJSON {
		return store.ActiveAlertGeoJSON{NWSID: id, States: states}
	}

	all := []store.ActiveAlertGeoJSON{
		a("WI-only", "WI"),
		a("IL-only", "IL"),
		a("WI+IL cross", "WI", "IL"),
		a("MI-only", "MI"),
		a("no-state"), // empty States — should never appear in any per-state file
	}

	cases := []struct {
		state string
		want  []string // NWSIDs in expected order
	}{
		{"WI", []string{"WI-only", "WI+IL cross"}},
		{"IL", []string{"IL-only", "WI+IL cross"}},
		{"MI", []string{"MI-only"}},
		// Bordering state with no matching alerts — empty slice, not nil.
		// Empty-vs-nil matters because the publisher marshals this directly
		// to the wire and `alerts: null` would break the v2 contract.
		{"OH", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			t.Parallel()
			got := filterAlertsByState(all, tc.state)

			// Wire-format invariant: filterAlertsByState MUST return a non-nil
			// slice even when no alerts match. The per-state snapshot has no
			// `omitempty` on Alerts, so a nil here would marshal to
			// `"alerts":null` — silently breaks the v2 contract that promises
			// an array. Empty must be `[]`.
			if got == nil {
				t.Fatalf("filterAlertsByState(%q) returned nil — must return non-nil empty slice for v2 wire-format contract", tc.state)
			}

			gotIDs := make([]string, len(got))
			for i, x := range got {
				gotIDs[i] = x.NWSID
			}
			if len(gotIDs) == 0 && len(tc.want) == 0 {
				return
			}
			if !reflect.DeepEqual(gotIDs, tc.want) {
				t.Errorf("filterAlertsByState(%q) = %v, want %v", tc.state, gotIDs, tc.want)
			}
		})
	}

	// Cross-border invariant: an alert with N states appears in exactly N
	// per-state files. Catches future regressions where someone "optimizes"
	// the filter and accidentally first-match-wins.
	t.Run("cross-border alert appears in every matching state", func(t *testing.T) {
		t.Parallel()
		hits := 0
		for _, st := range []string{"WI", "IL", "MI", "OH"} {
			for _, x := range filterAlertsByState(all, st) {
				if x.NWSID == "WI+IL cross" {
					hits++
				}
			}
		}
		if hits != 2 {
			t.Errorf("WI+IL cross alert hit count: got %d, want 2 (one in WI, one in IL)", hits)
		}
	})
}
