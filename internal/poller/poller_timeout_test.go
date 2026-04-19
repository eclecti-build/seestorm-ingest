package poller

import (
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
)

// TestDeriveCycleTimeout_RespectsIntervalInvariant locks in the
// "cycle timeout < PollInterval" invariant for any operator-set interval.
// Previously the poller used a hard-coded PollCycleTimeoutSec constant,
// which meant setting PollInterval below 25s would silently generate
// self-inflicted missed cycles. See Codex review S6.
func TestDeriveCycleTimeout_RespectsIntervalInvariant(t *testing.T) {
	t.Parallel()

	ceiling := time.Duration(config.PollCycleTimeoutSec) * time.Second

	cases := []struct {
		name     string
		interval time.Duration
		want     time.Duration
	}{
		{
			name:     "default 30s interval clamps to audit ceiling",
			interval: 30 * time.Second,
			want:     ceiling, // 25s
		},
		{
			name:     "60s interval still clamps to ceiling, not interval-slack",
			interval: 60 * time.Second,
			want:     ceiling,
		},
		{
			name:     "20s interval derives below ceiling (20 - 5 = 15s)",
			interval: 20 * time.Second,
			want:     15 * time.Second,
		},
		{
			name:     "10s interval derives to 5s",
			interval: 10 * time.Second,
			want:     5 * time.Second,
		},
		{
			name:     "pathological tiny interval clamps to floor",
			interval: 2 * time.Second,
			want:     cycleFloor,
		},
		{
			name:     "zero interval clamps to floor (no negative timeouts)",
			interval: 0,
			want:     cycleFloor,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deriveCycleTimeout(tc.interval)
			if got != tc.want {
				t.Errorf("deriveCycleTimeout(%v) = %v, want %v", tc.interval, got, tc.want)
			}
			// The core invariant: for any non-trivial interval, derived
			// timeout must leave at least cycleSlack headroom inside the
			// interval — otherwise the next tick is starved.
			if tc.interval > ceiling+cycleSlack {
				// Only the clamped ceiling case is relevant here.
				if got != ceiling {
					t.Errorf("interval > ceiling+slack: expected ceiling %v, got %v", ceiling, got)
				}
			} else if tc.interval > cycleSlack+cycleFloor {
				if got > tc.interval-cycleSlack {
					t.Errorf("derived timeout %v exceeds interval-slack %v (interval %v)", got, tc.interval-cycleSlack, tc.interval)
				}
			}
		})
	}
}
