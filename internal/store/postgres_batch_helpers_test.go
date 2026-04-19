package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// TestIsFatalBatchErr locks in the Codex S4 fix: batch-level ctx errors
// must short-circuit the per-row fallback instead of fanning out into
// O(n) doomed retries. Pure unit test — no DB needed.
func TestIsFatalBatchErr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"context.Canceled", context.Canceled, true},
		{"context.DeadlineExceeded", context.DeadlineExceeded, true},
		{"wrapped context.Canceled", fmt.Errorf("begin tx: %w", context.Canceled), true},
		{"wrapped context.DeadlineExceeded", fmt.Errorf("commit tx: %w", context.DeadlineExceeded), true},
		{"unrelated error", errors.New("generic DB error"), false},
		{"nil error", nil, false},
		{"wrapped unrelated", fmt.Errorf("wrap: %w", errors.New("inner")), false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := isFatalBatchErr(tc.err); got != tc.want {
				t.Errorf("isFatalBatchErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
