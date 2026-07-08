package nws

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchActiveAlerts_SendsIfNoneMatchAfterFirstETaggedResponse(t *testing.T) {
	t.Parallel()
	var sawIfNoneMatch string
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 1 {
			w.Header().Set("ETag", `"abc123"`)
			w.Header().Set("Content-Type", "application/geo+json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"type":"FeatureCollection","features":[]}`))
			return
		}
		sawIfNoneMatch = r.Header.Get("If-None-Match")
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := c.FetchActiveAlerts(ctx, "WI"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	_, err := c.FetchActiveAlerts(ctx, "WI")
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("second fetch: expected ErrNotModified, got %v", err)
	}
	if sawIfNoneMatch != `"abc123"` {
		t.Fatalf("expected If-None-Match %q, got %q", `"abc123"`, sawIfNoneMatch)
	}
}

func TestFetchActiveAlerts_304ReturnsNilAlertsAndErrNotModified(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	}))
	defer srv.Close()

	c := NewClient("test-agent")
	c.baseURL = srv.URL
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	alerts, err := c.FetchActiveAlerts(ctx, "WI")
	if alerts != nil {
		t.Fatalf("expected nil alerts on 304, got %+v", alerts)
	}
	if !errors.Is(err, ErrNotModified) {
		t.Fatalf("expected ErrNotModified, got %v", err)
	}
}

func TestFetchActiveAlerts_NoETagMeansNoConditionalRequestSent(t *testing.T) {
	t.Parallel()
	var sawIfNoneMatch string
	seenSecondCall := false
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		if call == 2 {
			seenSecondCall = true
			sawIfNoneMatch = r.Header.Get("If-None-Match")
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

	if _, err := c.FetchActiveAlerts(ctx, "WI"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := c.FetchActiveAlerts(ctx, "WI"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if !seenSecondCall {
		t.Fatal("expected a second request")
	}
	if sawIfNoneMatch != "" {
		t.Fatalf("expected no If-None-Match header (no prior ETag), got %q", sawIfNoneMatch)
	}
}
