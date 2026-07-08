package main

import "testing"

// TestRequireExplicitModeOnFly locks in the 2026-07 fix for the incident
// class documented in poller/mode.go:8-14: a regional Fly app missing its
// MODE secret must fail fast at boot, not silently become a second
// publisher via ParseMode's empty->"both" default. Local dev (no
// FLY_APP_NAME) is unaffected.
func TestRequireExplicitModeOnFly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		flyAppName string
		rawMode    string
		wantErr    bool
	}{
		{"local dev, no FLY_APP_NAME, empty MODE: ok", "", "", false},
		{"local dev, no FLY_APP_NAME, explicit MODE: ok", "", "ingest", false},
		{"on Fly, empty MODE: fatal", "seestorm-ingest-plains", "", true},
		{"on Fly, whitespace-only MODE: fatal", "seestorm-ingest-plains", "   ", true},
		{"on Fly, MODE=ingest: ok", "seestorm-ingest-plains", "ingest", false},
		{"on Fly, MODE=publish: ok", "seestorm-ingest", "publish", false},
		{"on Fly, MODE=both (explicit, allowed): ok", "seestorm-ingest", "both", false},
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := requireExplicitModeOnFly(c.flyAppName, c.rawMode)
			if c.wantErr && err == nil {
				t.Errorf("requireExplicitModeOnFly(%q, %q): expected error, got nil", c.flyAppName, c.rawMode)
			}
			if !c.wantErr && err != nil {
				t.Errorf("requireExplicitModeOnFly(%q, %q): unexpected error %v", c.flyAppName, c.rawMode, err)
			}
		})
	}
}
