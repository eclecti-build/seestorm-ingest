// Package retry implements the small in-cycle retry policy shared by the
// NWS and SPC HTTP clients (Tier 2 resilience hardening, 2026-07-08).
// Retries are bounded by the caller's remaining ctx budget (deadline) so
// they can never extend a poll cycle past its PollCycleTimeoutSec ceiling
// — see internal/poller/poller.go and internal/config/constants.go for
// the two-phase cycle budget this must fit inside.
package retry

import (
	"context"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
)

// MaxAttempts is the total number of attempts (1 initial + up to
// MaxAttempts-1 retries).
const MaxAttempts = config.HTTPRetryMaxAttempts

// BaseDelay / MaxDelay bound the exponential backoff before jitter.
const (
	BaseDelay = time.Duration(config.HTTPRetryBaseDelayMs) * time.Millisecond
	MaxDelay  = time.Duration(config.HTTPRetryMaxDelayMs) * time.Millisecond
)

// nextAttemptFloor is the minimum remaining ctx budget required before
// starting another attempt.
const nextAttemptFloor = time.Duration(config.HTTPRetryNextAttemptFloorMs) * time.Millisecond

// IsRetryableStatus reports whether an HTTP status code represents a
// transient upstream failure worth retrying inside the cycle budget: 429
// (rate limited) and any 5xx. Explicitly excludes 4xx-other-than-429
// (e.g. 403 from a bad User-Agent, 404) — those will never succeed on
// retry, and retrying them only burns budget the next phase needs.
func IsRetryableStatus(code int) bool {
	if code == 429 {
		return true
	}
	return code >= 500 && code <= 599
}

// BackoffDelay returns the exponential delay before retry attempt N
// (0-indexed) with full jitter: a uniform random value in
// [0, min(BaseDelay<<N, MaxDelay)]. Full jitter (vs. equal/decorrelated
// jitter) is the simplest policy that still avoids a synchronized retry
// storm across the 8-node fleet hitting the same upstream at the same
// instant after a shared outage.
func BackoffDelay(attempt int) time.Duration {
	exp := BaseDelay
	for i := 0; i < attempt; i++ {
		exp *= 2
		if exp > MaxDelay {
			exp = MaxDelay
			break
		}
	}
	return time.Duration(rand.Int64N(int64(exp) + 1))
}

// ParseRetryAfter parses the Retry-After header (seconds form only — NWS
// and SPC are not known to send the HTTP-date form, and adding a date
// parser for a case never observed is unjustified complexity). Returns
// (0, false) when absent, unparseable, or negative.
func ParseRetryAfter(header string) (time.Duration, bool) {
	if header == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

// NextDelay computes the wait before the next attempt, applying the
// Retry-After override when present, then capping the result against the
// remaining ctx budget. ok is false when there is not enough remaining
// budget to justify starting another attempt at all, in which case the
// caller must stop retrying immediately.
func NextDelay(ctx context.Context, attempt int, retryAfter time.Duration, hasRetryAfter bool) (time.Duration, bool) {
	delay := BackoffDelay(attempt)
	if hasRetryAfter {
		delay = retryAfter
	}
	if dl, hasDeadline := ctx.Deadline(); hasDeadline {
		remaining := time.Until(dl)
		if remaining < nextAttemptFloor {
			return 0, false
		}
		if cappedDelay := remaining - nextAttemptFloor; delay > cappedDelay {
			delay = cappedDelay
		}
	}
	return delay, true
}

// Sleep waits for d or until ctx is done, whichever comes first. Returns
// ctx.Err() if ctx finished first.
func Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
