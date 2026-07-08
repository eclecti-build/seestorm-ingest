package nws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestFetchActiveAlerts_RetriesOn429ThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/geo+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alerts, err := c.FetchActiveAlerts(ctx, "WI")
	if err != nil {
		t.Fatalf("FetchActiveAlerts: %v", err)
	}
	if alerts == nil || len(alerts.Features) != 0 {
		t.Fatalf("expected empty-but-non-nil feature collection, got %+v", alerts)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected exactly 2 requests (1 failed + 1 retry), got %d", got)
	}
}

func TestFetchActiveAlerts_HonorsRetryAfterHeader(t *testing.T) {
	t.Parallel()
	var calls int32
	start := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/geo+json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.FetchActiveAlerts(ctx, "WI")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("FetchActiveAlerts: %v", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected retry to wait ~1s per Retry-After header, only waited %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("retry wait far exceeded the 1s Retry-After value: %v", elapsed)
	}
}

func TestFetchActiveAlerts_DoesNotRetry403(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.FetchActiveAlerts(ctx, "WI")
	if err == nil {
		t.Fatal("expected an error for a 403 response")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 request (no retry on 403), got %d", got)
	}
}

func TestFetchActiveAlerts_BudgetExhaustionStopsRetryingBeforeDeadlineBlows(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.FetchActiveAlerts(ctx, "WI")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error — upstream always returns 500")
	}
	if elapsed > 700*time.Millisecond {
		t.Fatalf("retry loop ran well past the 400ms ctx deadline: %v", elapsed)
	}
	if got := atomic.LoadInt32(&calls); got > 3 {
		t.Fatalf("expected the retry loop to stop itself well before MaxAttempts given the tight budget, got %d calls", got)
	}
	_ = strconv.Itoa(int(calls)) // silence unused import if calls ends up only read via atomic
}
