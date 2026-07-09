package poller

import (
	"errors"
	"fmt"
	"testing"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

func TestClassifyAlertFetchErr_NotModified(t *testing.T) {
	t.Parallel()
	unchanged, realErr := classifyAlertFetchErr(nws.ErrNotModified)
	if !unchanged {
		t.Fatal("expected unchanged=true for nws.ErrNotModified")
	}
	if realErr != nil {
		t.Fatalf("expected real=nil, got %v", realErr)
	}
}

func TestClassifyAlertFetchErr_WrappedNotModified(t *testing.T) {
	t.Parallel()
	wrapped := fmt.Errorf("wrapper: %w", nws.ErrNotModified)
	unchanged, _ := classifyAlertFetchErr(wrapped)
	if !unchanged {
		t.Fatal("expected unchanged=true for a wrapped nws.ErrNotModified")
	}
}

func TestClassifyAlertFetchErr_RealFailure(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	unchanged, realErr := classifyAlertFetchErr(boom)
	if unchanged {
		t.Fatal("expected unchanged=false for an unrelated error")
	}
	if !errors.Is(realErr, boom) {
		t.Fatalf("expected real to be boom, got %v", realErr)
	}
}

func TestClassifyAlertFetchErr_Nil(t *testing.T) {
	t.Parallel()
	unchanged, realErr := classifyAlertFetchErr(nil)
	if unchanged {
		t.Fatal("expected unchanged=false for nil err")
	}
	if realErr != nil {
		t.Fatalf("expected real=nil, got %v", realErr)
	}
}

func TestETagVouchesForStore(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name             string
		lastUpsertFailed bool
		want             bool
	}{
		{"previous 200 payload landed", false, true},
		{"previous 200 payload failed to land", true, false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := etagVouchesForStore(tc.lastUpsertFailed); got != tc.want {
				t.Fatalf("etagVouchesForStore(%v) = %v, want %v", tc.lastUpsertFailed, got, tc.want)
			}
		})
	}
}
