package poller

import "testing"

// TestParseMode locks in MODE resolution: empty defaults to "both" (so
// existing single-node deployments and local runs are unaffected), values are
// case-insensitive and trimmed, and an unrecognized value is a hard error
// rather than a silent fallback to "both" — defaulting a typo'd publisher back
// to "both" would reintroduce the history-amplification bug this flag prevents.
func TestParseMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModeBoth, false},
		{"both", ModeBoth, false},
		{"ingest", ModeIngest, false},
		{"publish", ModePublish, false},
		{"PUBLISH", ModePublish, false},
		{"  ingest  ", ModeIngest, false},
		{"garbage", "", true},
		{"publsh", "", true},
	}

	for _, c := range cases {
		got, err := ParseMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMode(%q): expected error, got nil (mode %q)", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("ParseMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestModePredicates pins which phases each mode enables. The empty Mode (zero
// value) must behave as ModeBoth so a Config constructed without an explicit
// Mode keeps the original poll+publish behavior.
func TestModePredicates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode          Mode
		shouldIngest  bool
		shouldPublish bool
	}{
		{ModeBoth, true, true},
		{ModeIngest, true, false},
		{ModePublish, false, true},
		{Mode(""), true, true},
	}

	for _, c := range cases {
		if got := c.mode.ShouldIngest(); got != c.shouldIngest {
			t.Errorf("Mode(%q).ShouldIngest() = %v, want %v", c.mode, got, c.shouldIngest)
		}
		if got := c.mode.ShouldPublish(); got != c.shouldPublish {
			t.Errorf("Mode(%q).ShouldPublish() = %v, want %v", c.mode, got, c.shouldPublish)
		}
	}
}
