package health

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHandler_AllFeedsFresh_Returns200(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r.RecordSuccess(FeedAlerts, now.Add(-10*time.Second))
	h := NewHandler(r, []Feed{FeedAlerts}, 90*time.Second, func() time.Time { return now })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestHandler_StaleFeed_Returns503WithFeedName(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	r.RecordSuccess(FeedAlerts, now.Add(-200*time.Second)) // older than 90s maxAge
	h := NewHandler(r, []Feed{FeedAlerts}, 90*time.Second, func() time.Time { return now })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body struct {
		Status     string `json:"status"`
		StaleFeeds []struct {
			Feed string `json:"feed"`
		} `json:"stale_feeds"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding body: %v", err)
	}
	if len(body.StaleFeeds) != 1 || body.StaleFeeds[0].Feed != string(FeedAlerts) {
		t.Fatalf("expected stale_feeds=[alerts], got %+v", body.StaleFeeds)
	}
}

func TestHandler_NeverSucceededFeed_Returns503(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	now := time.Date(2026, 7, 8, 12, 0, 0, 0, time.UTC)
	h := NewHandler(r, []Feed{FeedPublish}, 90*time.Second, func() time.Time { return now })

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_NoRequiredFeeds_Returns200(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h := NewHandler(r, nil, 90*time.Second, time.Now)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 when no feeds are required", rec.Code)
	}
}
