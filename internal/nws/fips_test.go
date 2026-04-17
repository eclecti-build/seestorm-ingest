package nws

import "testing"

func TestStateForSAMECode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		same      string
		wantState string
		wantOK    bool
	}{
		// Great Lakes basin states (the initial multi-state expansion target).
		{"minnesota_hennepin", "027053", "MN", true},
		{"wisconsin_dane", "055025", "WI", true},
		{"illinois_cook", "017031", "IL", true},
		{"indiana_marion", "018097", "IN", true},
		{"michigan_wayne", "026163", "MI", true},
		{"ohio_franklin", "039049", "OH", true},
		{"pennsylvania_allegheny", "042003", "PA", true},
		{"newyork_erie", "036029", "NY", true},

		// Spot-checks outside the Great Lakes set.
		{"california", "006037", "CA", true},
		{"texas", "048201", "TX", true},
		{"alaska", "002020", "AK", true},

		// Territory.
		{"puerto_rico", "072001", "PR", true},

		// Failure modes.
		{"unknown_prefix", "999999", "", false},
		{"too_short", "5", "", false},
		{"empty", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotState, gotOK := StateForSAMECode(tc.same)
			if gotState != tc.wantState || gotOK != tc.wantOK {
				t.Errorf("StateForSAMECode(%q) = (%q, %v), want (%q, %v)",
					tc.same, gotState, gotOK, tc.wantState, tc.wantOK)
			}
		})
	}
}
