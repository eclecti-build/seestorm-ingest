package poller

import (
	"context"
	"testing"
	"time"
)

func TestStartupJitter_BoundedByInterval(t *testing.T) {
	t.Parallel()
	interval := 30 * time.Second
	for i := 0; i < 200; i++ {
		d := startupJitter(interval)
		if d < 0 || d >= interval {
			t.Fatalf("startupJitter(%v) = %v, out of bounds [0, %v)", interval, d, interval)
		}
	}
}

func TestStartupJitter_ZeroIntervalReturnsZero(t *testing.T) {
	t.Parallel()
	if d := startupJitter(0); d != 0 {
		t.Fatalf("startupJitter(0) = %v, want 0", d)
	}
}

// TestRun_AppliesStartupJitterBeforeFirstPoll proves Run waits out the
// injected jitter before its first poll. Mode is ModeIngest —
// Mode.ShouldIngest() must be true for jitter to apply at all (review
// amendment: publisher-only nodes are exempt, see
// TestPollerStartupJitter_ZeroForPublishOnlyMode below) — so, unlike the
// pre-amendment version of this test, poll() is NOT a safe no-op here:
// NWS/SPC/Store are all nil on this bare Config, so the immediate
// post-jitter pollAlerts/pollStormReports call WILL nil-pointer-panic.
// That's fine and intentional — Task 1's pollSafely wraps every call to
// p.poll (including this first one), recovering and logging the panic
// rather than propagating it, so this test still needs no live
// dependencies and still only asserts the jitter-wait timing.
func TestRun_AppliesStartupJitterBeforeFirstPoll(t *testing.T) {
	t.Parallel()
	cfg := Config{
		PollInterval: 200 * time.Millisecond,
		Mode:         ModeIngest,
		jitterFunc:   func(time.Duration) time.Duration { return 50 * time.Millisecond },
	}
	p := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("Run returned before the injected jitter elapsed: %v", elapsed)
	}
}

// TestRun_JitterWaitRespectsCtxCancellation proves a long jitter doesn't
// block shutdown — ctx cancellation during the jitter wait returns
// promptly. Mode is ModeIngest so the jitter-wait path is actually
// exercised (review amendment: publish-only nodes skip it entirely — see
// TestPollerStartupJitter_ZeroForPublishOnlyMode). ctx is canceled WHILE
// still inside the jitter wait, so Run returns before ever reaching
// pollSafely(ctx, p.poll) — no nil-dependency panic risk here at all,
// unlike TestRun_AppliesStartupJitterBeforeFirstPoll above.
func TestRun_JitterWaitRespectsCtxCancellation(t *testing.T) {
	t.Parallel()
	cfg := Config{
		PollInterval: time.Second,
		Mode:         ModeIngest,
		jitterFunc:   func(time.Duration) time.Duration { return 10 * time.Second },
	}
	p := New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	if err := p.Run(ctx); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("Run did not return promptly on ctx cancellation during jitter wait: %v", elapsed)
	}
}

// TestPollerStartupJitter_ZeroForPublishOnlyMode proves the exemption
// (review amendment): a publish-only node (ModePublish) never polls
// NWS/SPC — Mode.ShouldIngest() is false for it — so the fleet-lockstep
// rationale startupJitter's package-level doc comment describes (de-sync
// 8 nodes' upstream polling after a fleet-wide redeploy) doesn't apply.
// Applying jitter to it anyway would only add up to PollInterval of pure
// post-deploy staleness on the merged/history R2 snapshot for zero
// benefit. The injected jitterFunc deliberately returns a large nonzero
// value so this test would fail loudly if the exemption weren't wired.
func TestPollerStartupJitter_ZeroForPublishOnlyMode(t *testing.T) {
	t.Parallel()
	cfg := Config{
		PollInterval: 30 * time.Second,
		Mode:         ModePublish,
		jitterFunc:   func(time.Duration) time.Duration { return 25 * time.Second },
	}
	p := New(cfg)
	if got := p.startupJitter(); got != 0 {
		t.Fatalf("expected zero jitter for ModePublish, got %v", got)
	}
}

// TestPollerStartupJitter_NonzeroForIngestAndBothModes proves the
// exemption is scoped correctly — ModeIngest and ModeBoth both still get
// jitter (both poll upstreams; ShouldIngest() is true for both).
func TestPollerStartupJitter_NonzeroForIngestAndBothModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeIngest, ModeBoth} {
		cfg := Config{
			PollInterval: 30 * time.Second,
			Mode:         mode,
			jitterFunc:   func(time.Duration) time.Duration { return 25 * time.Second },
		}
		p := New(cfg)
		if got := p.startupJitter(); got != 25*time.Second {
			t.Fatalf("mode %v: expected the injected jitterFunc value, got %v", mode, got)
		}
	}
}
