package spc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

const testCSVReports = "Time,Speed,Location,County,State,Lat,Lon,Comments\n0001,UNK,Somewhere,Dane,WI,43.0,-89.0,ok\n"

func TestFetchTodayTornadoReports_RetriesOn5xxThenSucceeds(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(testCSVReports))
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reports, err := c.FetchTodayTornadoReports(ctx)
	if err != nil {
		t.Fatalf("FetchTodayTornadoReports: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("expected 1 parsed report, got %d", len(reports))
	}
	if reports[0].Type != "tornado" {
		t.Fatalf("expected tornado report type, got %q", reports[0].Type)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected exactly 2 requests (1 failed + 1 retry), got %d", got)
	}
}

func TestFetchTodayTornadoReports_HonorsRetryAfterHeader(t *testing.T) {
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
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(testCSVReports))
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.FetchTodayTornadoReports(ctx)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("FetchTodayTornadoReports: %v", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected retry to wait ~1s per Retry-After header, only waited %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("retry wait far exceeded the 1s Retry-After value: %v", elapsed)
	}
}

func TestFetchTodayTornadoReports_DoesNotRetry404(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.FetchTodayTornadoReports(ctx)
	if err == nil {
		t.Fatal("expected an error for a 404 response")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 request (no retry on 404), got %d", got)
	}
}

func TestFetchTodayTornadoReports_BudgetExhaustionStopsRetrying(t *testing.T) {
	t.Parallel()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := NewClient()
	c.baseURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.FetchTodayTornadoReports(ctx)
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
}
