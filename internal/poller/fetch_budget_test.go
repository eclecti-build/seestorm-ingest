package poller

import (
	"context"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
)

func TestWithBudgetFraction_CapsAgainstRemainingDeadline(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	child, childCancel := withBudgetFraction(parent, 50)
	defer childCancel()

	dl, ok := child.Deadline()
	if !ok {
		t.Fatal("expected child to have a deadline")
	}
	// ~5s expected (50% of 10s); generous slack for scheduling jitter, the
	// point is "roughly half", not exact.
	remaining := time.Until(dl)
	if remaining <= 0 || remaining > 6*time.Second {
		t.Fatalf("expected ~5s remaining (50%% of 10s), got %v", remaining)
	}
}

func TestWithBudgetFraction_NoDeadlineReturnsUsableContext(t *testing.T) {
	t.Parallel()
	child, cancel := withBudgetFraction(context.Background(), 50)
	defer cancel()
	if _, ok := child.Deadline(); ok {
		t.Fatal("expected no deadline to be introduced when the parent has none")
	}
	if err := child.Err(); err != nil {
		t.Fatalf("expected a live context, got err=%v", err)
	}
}

func TestWithBudgetFraction_AlreadyExpiredParentYieldsExpiredChild(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)

	child, childCancel := withBudgetFraction(parent, 50)
	defer childCancel()
	select {
	case <-child.Done():
	default:
		t.Fatal("expected an already-expired parent to yield an already-done child")
	}
}

// TestSequentialBudgetCapping_LeavesFloorForStore proves the worst case
// AlertsFetchBudgetPercent/SPCFetchBudgetPercent are designed to bound:
// if the alerts fetch and each of the three SPC CSV fetches consume their
// FULL allotted cap (simulating a completely wedged upstream that hangs
// until its own sub-context expires, on every one of the four calls —
// worse than merely slow, to prove the cap actually bites rather than
// merely being generous), the store-upsert step that runs after them
// — under the ORIGINAL fetchStoreCtx, not a sub-context — still has a
// strictly positive floor of budget left, rather than being handed an
// already-expired context (the starvation bug this amendment fixes; see
// pollAlerts/pollStormReports and this task's "retry budget fairness"
// design decision).
//
// This targets the composition seam (withBudgetFraction applied in the
// same sequence pollAlerts/pollStormReports use) rather than the real
// pollAlerts/pollStormReports methods directly: NWS/SPC/Store are
// concrete types with no interface seam (see this plan's "Corrections"
// section), so a live end-to-end version would need a real Postgres
// connection AND a way to override *nws.Client/*spc.Client's unexported
// baseURL from outside their own packages, neither of which this
// already-large plan introduces. The pure-composition test below proves
// the SAME arithmetic pollAlerts/pollStormReports execute.
func TestSequentialBudgetCapping_LeavesFloorForStore(t *testing.T) {
	t.Parallel()
	const fetchStoreBudget = 2 * time.Second
	fetchStoreCtx, cancel := context.WithTimeout(context.Background(), fetchStoreBudget)
	defer cancel()

	consumeFullCap := func(ctx context.Context, pct int) {
		child, childCancel := withBudgetFraction(ctx, pct)
		defer childCancel()
		<-child.Done() // simulates a fetch that hangs until its own cap expires
	}

	start := time.Now()
	consumeFullCap(fetchStoreCtx, config.AlertsFetchBudgetPercent) // alerts: worst case 50%
	consumeFullCap(fetchStoreCtx, config.SPCFetchBudgetPercent)    // tornado: worst case 20% of what's left
	consumeFullCap(fetchStoreCtx, config.SPCFetchBudgetPercent)    // hail
	consumeFullCap(fetchStoreCtx, config.SPCFetchBudgetPercent)    // wind
	elapsed := time.Since(start)

	dl, ok := fetchStoreCtx.Deadline()
	if !ok {
		t.Fatal("expected fetchStoreCtx to have a deadline")
	}
	remaining := time.Until(dl)
	if remaining <= 0 {
		t.Fatalf("store step would receive an already-expired context after %v elapsed (budget=%v) — starved", elapsed, fetchStoreBudget)
	}
	// Worst-case analytic floor: 100% -> 50% -> 40% -> 32% -> 25.6% of the
	// original budget, i.e. ~512ms of this test's 2s budget. Assert well
	// above zero rather than pinning the exact percentage, to avoid a
	// flaky test tied to goroutine scheduling.
	if remaining < 300*time.Millisecond {
		t.Fatalf("expected a meaningful floor (~500ms) left for the store step, got %v (elapsed=%v)", remaining, elapsed)
	}
}
