package poller

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestPollSafely_RecoversPanicAndLogs proves a panicking cycle is caught
// and logged with a degraded_path key instead of crashing the process.
// Not t.Parallel(): it swaps the package-level slog default logger, which
// would race against any other test asserting on default-logger output.
func TestPollSafely_RecoversPanicAndLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	pollSafely(context.Background(), func(_ context.Context) {
		panic("boom: simulated phase failure")
	})

	out := buf.String()
	if !strings.Contains(out, `"degraded_path":"cycle_panic_recovered"`) {
		t.Fatalf("expected degraded_path=cycle_panic_recovered in log output, got: %s", out)
	}
	if !strings.Contains(out, "boom: simulated phase failure") {
		t.Fatalf("expected panic message in log output, got: %s", out)
	}
}

// TestPollSafely_NextCycleProceedsAfterPanic proves recovery doesn't wedge
// the poller — a subsequent pollSafely call runs its fn normally.
func TestPollSafely_NextCycleProceedsAfterPanic(t *testing.T) {
	t.Parallel()
	calls := 0

	pollSafely(context.Background(), func(_ context.Context) {
		calls++
		panic("cycle 1 boom")
	})
	pollSafely(context.Background(), func(_ context.Context) {
		calls++
	})

	if calls != 2 {
		t.Fatalf("expected both cycles to run (calls=2), got calls=%d", calls)
	}
}

// TestPollSafely_NoPanicRunsNormally is the non-panic baseline.
func TestPollSafely_NoPanicRunsNormally(t *testing.T) {
	t.Parallel()
	ran := false
	pollSafely(context.Background(), func(_ context.Context) { ran = true })
	if !ran {
		t.Fatal("expected fn to run")
	}
}
