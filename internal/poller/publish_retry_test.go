package poller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/health"
	"github.com/eclecti-build/seestorm-ingest/internal/publisher"
)

func TestAttemptPublish_SucceedsFirstTry(t *testing.T) {
	t.Parallel()
	calls := 0
	err := attemptPublish(context.Background(), func(_ context.Context) error {
		calls++
		return nil
	}, 2, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("attemptPublish: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 call, got %d", calls)
	}
}

func TestAttemptPublish_RetriesOnceThenSucceeds(t *testing.T) {
	t.Parallel()
	calls := 0
	err := attemptPublish(context.Background(), func(_ context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("transient")
		}
		return nil
	}, 2, time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("attemptPublish: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly 2 calls, got %d", calls)
	}
}

func TestAttemptPublish_ExhaustsRetriesReturnsLastErr(t *testing.T) {
	t.Parallel()
	calls := 0
	boom := errors.New("boom")
	err := attemptPublish(context.Background(), func(_ context.Context) error {
		calls++
		return boom
	}, 2, time.Second, 10*time.Millisecond)
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected exactly maxAttempts=2 calls, got %d", calls)
	}
}

func TestAttemptPublish_StopsEarlyWhenParentDone(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(context.Background())
	cancel() // already done
	calls := 0
	start := time.Now()
	err := attemptPublish(parent, func(_ context.Context) error {
		calls++
		return errors.New("fails fast")
	}, 3, time.Second, 500*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed > 100*time.Millisecond {
		t.Fatalf("expected to stop immediately on a done parent ctx, took %v", elapsed)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 attempt before stopping, got %d", calls)
	}
}

// fakePublisher implements publisher.Publisher (an existing interface —
// no refactor needed) so this suite can inject a controllable per-state
// put without any live R2/network dependency.
type fakePublisher struct {
	mu            sync.Mutex
	statePutCalls map[string]int
	blockState    string
	blockUntil    <-chan struct{}
	panicState    string
}

func (f *fakePublisher) Publish(_ context.Context, _ publisher.Snapshot) error {
	return nil
}

func (f *fakePublisher) PublishState(ctx context.Context, snap publisher.StateSnapshot) error {
	f.mu.Lock()
	f.statePutCalls[snap.AreaState]++
	f.mu.Unlock()

	if snap.AreaState == f.panicState {
		panic("publish state panic")
	}

	if snap.AreaState == f.blockState {
		select {
		case <-f.blockUntil:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakePublisher) callCount(state string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statePutCalls[state]
}

func TestPublishPerStateSnapshots_RecoversStatePublisherPanic(t *testing.T) {
	t.Parallel()
	areas := []string{"WI", "IL", "MI", "OH", "IN"}
	fp := &fakePublisher{
		statePutCalls: make(map[string]int),
		panicState:    "WI",
	}

	reg := healthRegistryForTest()
	p := &Poller{cfg: Config{Areas: areas, Publisher: fp, Health: reg}}

	p.publishPerStateSnapshots(context.Background(), nil, time.Now())

	for _, state := range []string{"IL", "MI", "OH", "IN"} {
		if fp.callCount(state) != 1 {
			t.Errorf("state %s: expected exactly 1 PublishState call despite WI panic, got %d", state, fp.callCount(state))
		}
	}
	failures := reg.PublishPutFailures()
	if failures["WI"] != 1 {
		t.Fatalf("expected WI panic to be recorded as one publish-put failure, got failures=%+v", failures)
	}
}

func TestPublishPerStateSnapshots_BoundedConcurrency(t *testing.T) {
	t.Parallel()
	var maxConcurrent int32
	var current int32
	areas := []string{"WI", "IL", "MI", "OH", "IN", "MN", "IA", "PA", "NY"}

	fp := &fakePublisher{statePutCalls: make(map[string]int)}
	trackingPublisher := &trackingConcurrencyPublisher{
		inner: fp,
		before: func() {
			n := atomic.AddInt32(&current, 1)
			for {
				old := atomic.LoadInt32(&maxConcurrent)
				if n <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		},
		after: func() { atomic.AddInt32(&current, -1) },
	}

	p := &Poller{cfg: Config{Areas: areas, Publisher: trackingPublisher}}
	p.publishPerStateSnapshots(context.Background(), nil, time.Now())

	if got := atomic.LoadInt32(&maxConcurrent); got > 4 {
		t.Fatalf("max concurrent publish calls = %d, want <= config.PublishConcurrency (4)", got)
	}
	for _, state := range areas {
		if fp.callCount(state) != 1 {
			t.Fatalf("state %s: expected exactly 1 PublishState call, got %d", state, fp.callCount(state))
		}
	}
}

// trackingConcurrencyPublisher wraps fakePublisher's PublishState with
// before/after hooks to measure concurrency without changing
// fakePublisher itself.
type trackingConcurrencyPublisher struct {
	inner  *fakePublisher
	before func()
	after  func()
}

func (t *trackingConcurrencyPublisher) Publish(ctx context.Context, snapshot publisher.Snapshot) error {
	return t.inner.Publish(ctx, snapshot)
}

func (t *trackingConcurrencyPublisher) PublishState(ctx context.Context, snap publisher.StateSnapshot) error {
	t.before()
	defer t.after()
	return t.inner.PublishState(ctx, snap)
}

func TestPublishSnapshot_OneSlowStateDoesNotStarveOthers(t *testing.T) {
	t.Parallel()
	areas := []string{"WI", "IL", "MI", "OH", "IN"}
	blockUntil := make(chan struct{}) // never closed — WI hangs for the whole test
	fp := &fakePublisher{
		statePutCalls: make(map[string]int),
		blockState:    "WI",
		blockUntil:    blockUntil,
	}

	reg := healthRegistryForTest()
	p := &Poller{cfg: Config{Areas: areas, Publisher: fp, Health: reg}}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.publishPerStateSnapshots(ctx, nil, time.Now())

	for _, state := range []string{"IL", "MI", "OH", "IN"} {
		if fp.callCount(state) != 1 {
			t.Errorf("state %s: expected exactly 1 successful call despite WI hanging, got %d", state, fp.callCount(state))
		}
	}
	failures := reg.PublishPutFailures()
	if failures["WI"] < 1 {
		t.Errorf("expected WI to be recorded as a publish-put failure, got failures=%+v", failures)
	}
}

func healthRegistryForTest() *health.Registry {
	return health.NewRegistry()
}
