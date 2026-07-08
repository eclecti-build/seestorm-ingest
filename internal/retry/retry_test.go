package retry

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestIsRetryableStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code int
		want bool
	}{
		{http.StatusOK, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
		{599, true},
		{600, false},
	}
	for _, tc := range cases {
		if got := IsRetryableStatus(tc.code); got != tc.want {
			t.Errorf("IsRetryableStatus(%d) = %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestBackoffDelay_BoundedByExponentialCeiling(t *testing.T) {
	t.Parallel()
	for attempt := 0; attempt < 6; attempt++ {
		for i := 0; i < 50; i++ {
			d := BackoffDelay(attempt)
			if d < 0 || d > MaxDelay {
				t.Fatalf("BackoffDelay(%d) = %v, out of bounds [0, %v]", attempt, d, MaxDelay)
			}
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		header    string
		wantOK    bool
		wantValue time.Duration
	}{
		{"", false, 0},
		{"2", true, 2 * time.Second},
		{"0", true, 0},
		{"-1", false, 0},
		{"not-a-number", false, 0},
	}
	for _, tc := range cases {
		got, ok := ParseRetryAfter(tc.header)
		if ok != tc.wantOK {
			t.Errorf("ParseRetryAfter(%q) ok = %v, want %v", tc.header, ok, tc.wantOK)
			continue
		}
		if ok && got != tc.wantValue {
			t.Errorf("ParseRetryAfter(%q) = %v, want %v", tc.header, got, tc.wantValue)
		}
	}
}

func TestNextDelay_CapsAgainstRemainingBudget(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	delay, ok := NextDelay(ctx, 0, 2*time.Second, true) // Retry-After: 2s, but only ~300ms left
	if !ok {
		t.Fatal("expected ok=true (some budget remains)")
	}
	if delay >= 2*time.Second {
		t.Fatalf("expected delay capped below the 2s Retry-After value, got %v", delay)
	}
}

func TestNextDelay_NoRemainingBudgetStopsRetrying(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	time.Sleep(15 * time.Millisecond) // ctx deadline has now passed

	_, ok := NextDelay(ctx, 0, 0, false)
	if ok {
		t.Fatal("expected ok=false once remaining budget is exhausted")
	}
}

func TestSleep_ReturnsCtxErrOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(ctx, time.Second); err == nil {
		t.Fatal("expected error from an already-canceled ctx")
	}
}
