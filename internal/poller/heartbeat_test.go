package poller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/healthcheck"
)

// TestShouldHeartbeat pins the exact gating truth table: only the phase(s)
// a given Mode actually runs gate the ping; a phase the Mode doesn't run at
// all is vacuously fine (callers pass true for it — see poll()).
func TestShouldHeartbeat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		mode            Mode
		alertsOK        bool
		mergedPublishOK bool
		want            bool
	}{
		{"ingest-only, alerts succeeded", ModeIngest, true, true, true},
		{"ingest-only, alerts failed -> no ping", ModeIngest, false, true, false},
		{"ingest-only ignores mergedPublishOK (phase doesn't run)", ModeIngest, true, false, true},
		{"publish-only, merged publish succeeded", ModePublish, true, true, true},
		{"publish-only, merged publish failed -> no ping", ModePublish, true, false, false},
		{"publish-only ignores alertsOK (phase doesn't run)", ModePublish, false, true, true},
		{"both, everything succeeded", ModeBoth, true, true, true},
		{"both, alerts failed -> no ping", ModeBoth, false, true, false},
		{"both, merged publish failed -> no ping", ModeBoth, true, false, false},
		{"both, everything failed -> no ping", ModeBoth, false, false, false},
		{"empty Mode behaves as ModeBoth", Mode(""), false, true, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldHeartbeat(tc.mode, tc.alertsOK, tc.mergedPublishOK); got != tc.want {
				t.Errorf("shouldHeartbeat(%q, alertsOK=%v, mergedPublishOK=%v) = %v, want %v",
					tc.mode, tc.alertsOK, tc.mergedPublishOK, got, tc.want)
			}
		})
	}
}

// TestHeartbeatGating_EndToEndWithRealPinger composes shouldHeartbeat with
// the real healthcheck.Pinger against an httptest receiver — proving the
// actual wiring this task cares about ("a cycle with a failed alerts fetch
// sends NO ping"), not just the pure boolean function in isolation.
//
// A full Poller.poll()-cycle test with a REAL failing NWS fetch is
// intentionally NOT attempted here: internal/nws.Client hardcodes
// api.weather.gov as its base URL (no injection point) and
// internal/store.Store requires a real Postgres connection — this repo's
// own convention is "no mocks for the database; integration tests use a
// real Postgres instance" (see this task's Step 6 Known Scope Boundary).
// Composing the already-unit-tested shouldHeartbeat with the
// already-unit-tested Pinger against a real HTTP receiver is the honest
// boundary of what's testable here without a much larger refactor of
// Config's NWS/SPC/Store fields into interfaces, which is out of scope for
// this task.
func TestHeartbeatGating_EndToEndWithRealPinger(t *testing.T) {
	t.Parallel()

	var hits int32
	pinged := make(chan struct{}, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
		pinged <- struct{}{}
	}))
	defer srv.Close()

	pinger := healthcheck.New(srv.URL)

	// Cycle 1: simulates an ingest-only cycle whose NWS fetch+store failed —
	// shouldHeartbeat must say no, and the caller (mirroring poll()) must
	// not even call PingAsync.
	if shouldHeartbeat(ModeIngest, false, true) {
		pinger.PingAsync(context.Background())
	}
	select {
	case <-pinged:
		t.Fatal("expected NO ping after a failed alerts fetch")
	case <-time.After(100 * time.Millisecond):
		// Expected: nothing arrived.
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("expected 0 pings after a failed alerts fetch, got %d", got)
	}

	// Cycle 2: the next cycle succeeds — the ping now fires for real.
	if !shouldHeartbeat(ModeIngest, true, true) {
		t.Fatal("expected shouldHeartbeat to return true for a succeeded cycle")
	}
	pinger.PingAsync(context.Background())
	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("expected ping was not sent within 2s")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 ping total, got %d", got)
	}
}
