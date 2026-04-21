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

// TestSplitCycleBudget_GuaranteesPublishBudget locks in the core invariant
// behind Codex C3: the publish phase always gets PublishPhaseBudgetSec (15s)
// of its own budget, no matter how tight the overall cycle timeout is or how
// slow the fetch+store phase was. fetchStoreBudget is the remainder, floored
// at 1s for safety when cycleTimeout is pathologically small.
//
// The invariant we really care about:
//   - publishBudget is ALWAYS exactly PublishPhaseBudgetSec seconds.
//   - fetchStoreBudget is always >= 1s.
//   - Under normal (non-pathological) cycleTimeout values, the two sum to
//     cycleTimeout; under tiny values the floor takes over and total may
//     exceed cycleTimeout — that's fine because the outer pollCtx timeout
//     and sequential phase execution still bound total wall-clock.
func TestSplitCycleBudget_GuaranteesPublishBudget(t *testing.T) {
	t.Parallel()

	publish := time.Duration(config.PublishPhaseBudgetSec) * time.Second

	cases := []struct {
		name             string
		cycleTimeout     time.Duration
		wantFetchStore   time.Duration
		wantPublish      time.Duration
		wantSumEqTimeout bool
	}{
		{
			name:             "default 25s cycle splits to 10s fetch+store / 15s publish",
			cycleTimeout:     25 * time.Second,
			wantFetchStore:   10 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: true,
		},
		{
			name:             "30s cycle splits to 15s / 15s",
			cycleTimeout:     30 * time.Second,
			wantFetchStore:   15 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: true,
		},
		{
			name:             "16s cycle splits to 1s / 15s",
			cycleTimeout:     16 * time.Second,
			wantFetchStore:   1 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: true,
		},
		{
			name:             "15s cycle hits fetch+store floor — publish still gets full budget",
			cycleTimeout:     15 * time.Second,
			wantFetchStore:   1 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: false, // floor overrides subtraction
		},
		{
			name:             "1s cycleFloor hits fetch+store floor — publish still gets full budget",
			cycleTimeout:     1 * time.Second,
			wantFetchStore:   1 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: false,
		},
		{
			name:             "zero cycleTimeout hits fetch+store floor — publish still gets full budget",
			cycleTimeout:     0,
			wantFetchStore:   1 * time.Second,
			wantPublish:      publish,
			wantSumEqTimeout: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotFetchStore, gotPublish := splitCycleBudget(tc.cycleTimeout)
			if gotFetchStore != tc.wantFetchStore {
				t.Errorf("fetchStoreBudget = %v, want %v", gotFetchStore, tc.wantFetchStore)
			}
			if gotPublish != tc.wantPublish {
				t.Errorf("publishBudget = %v, want %v", gotPublish, tc.wantPublish)
			}
			// Core invariant — publish ALWAYS gets the full configured budget,
			// and fetch+store is never less than 1s. This is the fix for C3.
			if gotPublish != publish {
				t.Errorf("publishBudget must ALWAYS equal PublishPhaseBudgetSec (%v), got %v", publish, gotPublish)
			}
			if gotFetchStore < time.Second {
				t.Errorf("fetchStoreBudget must be >= 1s floor, got %v", gotFetchStore)
			}
			if tc.wantSumEqTimeout {
				if sum := gotFetchStore + gotPublish; sum != tc.cycleTimeout {
					t.Errorf("fetchStore+publish = %v, want %v (cycleTimeout)", sum, tc.cycleTimeout)
				}
			}
		})
	}
}

// TestSplitCycleBudget_DefaultCycleMatchesAuditNumbers pins the concrete
// budget numbers under the default operator config (30s PollInterval → 25s
// cycleTimeout → 10s fetch+store / 15s publish). These are the numbers
// referenced in incident playbooks and the Codex C3 review; if someone
// changes them they should do it deliberately, not incidentally.
//
// Widened from the original 20/5 split on 2026-04-21 after an IA outbreak
// where the 5s publish budget starved sequential R2 puts. See the
// PublishPhaseBudgetSec comment in internal/config/constants.go.
func TestSplitCycleBudget_DefaultCycleMatchesAuditNumbers(t *testing.T) {
	t.Parallel()

	cycleTimeout := deriveCycleTimeout(time.Duration(config.PollIntervalSec) * time.Second)
	if cycleTimeout != 25*time.Second {
		t.Fatalf("derived cycle timeout for 30s interval = %v, want 25s (audit-settled ceiling)", cycleTimeout)
	}

	fetchStore, publish := splitCycleBudget(cycleTimeout)
	if fetchStore != 10*time.Second {
		t.Errorf("fetchStoreBudget for default cycle = %v, want 10s", fetchStore)
	}
	if publish != 15*time.Second {
		t.Errorf("publishBudget for default cycle = %v, want 15s", publish)
	}
}
