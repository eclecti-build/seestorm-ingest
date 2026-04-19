package nws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
)

// TestFetchActiveAlerts_LimitReaderCapsOversizedBody confirms that an NWS
// response larger than NWSResponseMaxBytes is truncated before JSON decoding,
// producing a clean decode error instead of an OOM or a wedged connection.
// Failure-mode coverage for Tier 1 #2a.
func TestFetchActiveAlerts_LimitReaderCapsOversizedBody(t *testing.T) {
	t.Parallel()

	// Build a payload that starts as valid JSON ("{...features":[") and then
	// streams garbage well past the cap. We want the LimitReader to slice
	// mid-stream so the decoder sees an unexpected-EOF-style error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/geo+json")
		w.WriteHeader(http.StatusOK)
		// Open the structure so decode starts, then flood past the cap.
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[`))
		// Pad to cap + 1MB to guarantee the LimitReader cuts us off.
		filler := strings.Repeat("0", 1<<20) // 1 MiB chunks
		total := 0
		target := config.NWSResponseMaxBytes + (1 << 20)
		for total < target {
			_, _ = w.Write([]byte(filler))
			total += len(filler)
		}
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := c.FetchActiveAlerts(ctx, "WI")
	if err == nil {
		t.Fatal("expected decode error from truncated oversized body, got nil")
	}
	// The error shape is intentionally loose — we care that the cycle gets
	// a clean error back so it can continue, not about the exact message.
	if !strings.Contains(err.Error(), "decoding alerts") {
		t.Fatalf("expected decoding error, got: %v", err)
	}
}

// TestFetchActiveAlerts_SlowUpstreamRespectsContextDeadline proves that a
// stalled upstream does not hold the poller past the cycle deadline: the
// per-cycle context deadline propagates through to the HTTP client. Happy
// path for the slow-upstream scenario — the caller sees a timeout and can
// move on to the next cycle.
func TestFetchActiveAlerts_SlowUpstreamRespectsContextDeadline(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hang until the client's context is canceled, then exit.
		<-r.Context().Done()
		// Reaching here means the request was canceled — we're done.
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	// Short deadline that would fire well before the 15s HTTP client timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.FetchActiveAlerts(ctx, "WI")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	// Upper bound generous for Windows CI flakiness; the important assertion
	// is that we returned well before the 15s HTTP client timeout.
	if elapsed > 2*time.Second {
		t.Fatalf("fetch did not honor context deadline: took %v", elapsed)
	}
}
