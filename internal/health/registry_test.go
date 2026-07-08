package health

import (
	"sync"
	"testing"
	"time"
)

func TestRegistry_RecordAndLastSuccess(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	if _, ok := r.LastSuccess(FeedAlerts); ok {
		t.Fatal("expected no recorded success on a fresh registry")
	}
	now := time.Now()
	r.RecordSuccess(FeedAlerts, now)
	got, ok := r.LastSuccess(FeedAlerts)
	if !ok {
		t.Fatal("expected LastSuccess ok=true after RecordSuccess")
	}
	if !got.Equal(now) {
		t.Fatalf("LastSuccess = %v, want %v", got, now)
	}
}

func TestRegistry_NilReceiverIsSafe(t *testing.T) {
	t.Parallel()
	var r *Registry
	r.RecordSuccess(FeedAlerts, time.Now()) // must not panic
	if _, ok := r.LastSuccess(FeedAlerts); ok {
		t.Fatal("nil registry must report no recorded success")
	}
}

func TestRegistry_ConcurrentAccessIsRaceFree(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	var wg sync.WaitGroup
	feeds := []Feed{FeedAlerts, FeedSPCTorn, FeedSPCHail, FeedSPCWind, FeedPublish}
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			r.RecordSuccess(feeds[i%len(feeds)], time.Now())
		}()
		go func() {
			defer wg.Done()
			r.LastSuccess(feeds[i%len(feeds)])
		}()
	}
	wg.Wait()
}

func TestRequiredFeeds(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                    string
		shouldIngest, shouldPub bool
		want                    []Feed
	}{
		{"both", true, true, []Feed{FeedAlerts, FeedSPCTorn, FeedSPCHail, FeedSPCWind, FeedPublish}},
		{"ingest-only", true, false, []Feed{FeedAlerts, FeedSPCTorn, FeedSPCHail, FeedSPCWind}},
		{"publish-only", false, true, []Feed{FeedPublish}},
		{"neither (misconfig)", false, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RequiredFeeds(tc.shouldIngest, tc.shouldPub)
			if len(got) != len(tc.want) {
				t.Fatalf("RequiredFeeds(%v,%v) = %v, want %v", tc.shouldIngest, tc.shouldPub, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("RequiredFeeds(%v,%v)[%d] = %v, want %v", tc.shouldIngest, tc.shouldPub, i, got[i], tc.want[i])
				}
			}
		})
	}
}
