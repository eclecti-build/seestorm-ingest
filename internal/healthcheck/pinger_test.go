package healthcheck

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPingAsync_SendsGETToConfiguredURL proves the happy path: a configured
// Pinger sends exactly one GET to the ping URL per PingAsync call.
func TestPingAsync_SendsGETToConfiguredURL(t *testing.T) {
	t.Parallel()

	var hits int32
	var gotMethod string
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
		close(done)
	}))
	defer srv.Close()

	p := New(srv.URL)
	p.PingAsync(context.Background())

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ping was not sent within 2s")
	}

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("expected exactly 1 ping, got %d", got)
	}
	if gotMethod != http.MethodGet {
		t.Fatalf("expected GET, got %s", gotMethod)
	}
}

// TestPingAsync_NoopWhenURLEmpty proves the local-dev no-op contract: an
// empty URL (HEALTHCHECK_PING_URL unset) must never attempt any network
// call, and PingAsync must return immediately.
func TestPingAsync_NoopWhenURLEmpty(t *testing.T) {
	t.Parallel()

	p := New("")
	start := time.Now()
	p.PingAsync(context.Background())
	elapsed := time.Since(start)

	if elapsed > 5*time.Millisecond {
		t.Fatalf("no-op PingAsync should return near-instantly, took %v", elapsed)
	}
}

// TestPingAsync_DeadURLNeverBlocksCaller is the failure-path proof required
// by the audit: a ping endpoint that never responds must not delay the poll
// cycle that called PingAsync. We point the Pinger's Client at a transport
// that blocks forever and assert PingAsync itself returns near-instantly —
// the actual HTTP attempt and its timeout happen in the background
// goroutine, invisible to the caller.
func TestPingAsync_DeadURLNeverBlocksCaller(t *testing.T) {
	t.Parallel()

	blockForever := make(chan struct{}) // never closed
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-blockForever // simulate a hung endpoint
	}))
	defer srv.Close()

	p := New(srv.URL)
	start := time.Now()
	p.PingAsync(context.Background())
	elapsed := time.Since(start)

	if elapsed > 5*time.Millisecond {
		t.Fatalf("PingAsync must return before the ping completes; took %v", elapsed)
	}
	// Cleanup: unblock the handler so the httptest server can close without
	// leaking a goroutine past the test.
	close(blockForever)
}

// TestPingAsync_NetworkErrorDoesNotPanic proves a fully unreachable URL
// (connection refused) is handled without panicking — the ping is
// advisory, not load-bearing for process stability.
func TestPingAsync_NetworkErrorDoesNotPanic(t *testing.T) {
	t.Parallel()

	// Port 1 is a reserved/unreachable port in virtually every environment;
	// the connection attempt fails fast with "connection refused".
	p := New("http://127.0.0.1:1/ping")

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("PingAsync must not panic on network error, got: %v", r)
		}
	}()
	p.PingAsync(context.Background())
	time.Sleep(50 * time.Millisecond) // let the background goroutine run
}

func TestPing_DrainsResponseBodyForKeepAliveReuse(t *testing.T) {
	t.Parallel()

	var (
		newConns int32
		mu       sync.Mutex
		hits     []string
	)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.RemoteAddr)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			atomic.AddInt32(&newConns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	p := New(srv.URL)
	client := srv.Client()
	p.Client = client
	defer client.CloseIdleConnections()

	p.ping(context.Background())
	p.ping(context.Background())

	if got := atomic.LoadInt32(&newConns); got != 1 {
		t.Fatalf("expected keep-alive connection reuse after draining body; got %d new connections", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 2 {
		t.Fatalf("expected 2 pings, got %d", len(hits))
	}
	if hits[0] != hits[1] {
		t.Fatalf("expected both pings on the same remote address, got %q then %q", hits[0], hits[1])
	}
}
