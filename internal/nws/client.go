package nws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
	"github.com/eclecti-build/seestorm-ingest/internal/retry"
)

type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string

	etagMu sync.Mutex
	etags  map[string]string // endpoint -> last-seen ETag
}

// ErrNotModified is returned by FetchActiveAlerts when the upstream
// responds 304 Not Modified to a conditional (If-None-Match) request.
// Callers must treat this as "fetch succeeded, nothing changed" — skip
// decode + upsert, but still record fetch success (see
// internal/health.Registry and internal/poller's classifyAlertFetchErr).
var ErrNotModified = errors.New("nws: not modified (304)")

func NewClient(userAgent string) *Client {
	return &Client{
		httpClient: &http.Client{
			// config.HTTPClientTimeoutSec is 30s, sized to accommodate outbreak-day
			// payloads when polling many states in a single request (e.g. an
			// 8-state Great Lakes pull during a multi-state derecho can return
			// several MB of GeoJSON). See config/constants.go for the rationale
			// behind the deviation from the audit's single-state default.
			Timeout: config.HTTPClientTimeoutSec * time.Second,
		},
		userAgent: userAgent,
		baseURL:   "https://api.weather.gov",
		etags:     make(map[string]string),
	}
}

// FetchActiveAlerts retrieves active alerts for the given area. The api.weather.gov
// `area` parameter accepts either a single state code (e.g. "WI") or a
// comma-separated list (e.g. "MN,WI,IL,IN,MI,OH,PA,NY"); the API returns a
// single merged FeatureCollection so multi-state polling is one HTTP call.
//
// Retries up to retry.MaxAttempts total attempts on transient failures
// (transport errors, 429, 5xx) with full-jitter exponential backoff,
// honoring Retry-After when present and always capping the wait against
// ctx's remaining deadline (see internal/retry) — retries never extend
// the caller's cycle budget. Non-transient failures (4xx other than 429)
// return immediately on the first attempt.
func (c *Client) FetchActiveAlerts(ctx context.Context, area string) (*AlertsResponse, error) {
	params := url.Values{
		"area":   {area},
		"status": {"actual"},
	}
	endpoint := fmt.Sprintf("%s/alerts/active?%s", c.baseURL, params.Encode())

	c.etagMu.Lock()
	lastETag := c.etags[endpoint]
	c.etagMu.Unlock()

	var lastErr error
	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}
		req.Header.Set("User-Agent", c.userAgent)
		req.Header.Set("Accept", "application/geo+json")
		if lastETag != "" {
			req.Header.Set("If-None-Match", lastETag)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetching alerts: %w", err)
			if !c.retryAfterErr(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode == http.StatusNotModified {
			_ = resp.Body.Close()
			return nil, ErrNotModified
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("NWS API returned %d: %s", resp.StatusCode, string(body))
			if !retry.IsRetryableStatus(resp.StatusCode) {
				return nil, lastErr
			}
			retryAfter, hasRA := retry.ParseRetryAfter(resp.Header.Get("Retry-After"))
			if !c.retryAfterResp(ctx, attempt, retryAfter, hasRA) {
				return nil, lastErr
			}
			continue
		}

		// 200 OK: capture the new ETag (if present) BEFORE decoding, so a
		// decode failure still leaves us positioned to send a conditional
		// request next cycle — a transient decode issue is not a reason to
		// lose the freshness signal.
		if newETag := resp.Header.Get("ETag"); newETag != "" {
			c.etagMu.Lock()
			c.etags[endpoint] = newETag
			c.etagMu.Unlock()
		}

		// Cap upstream body size before decoding. A runaway or malicious NWS
		// response shouldn't be able to exhaust memory or stall the poll
		// cycle — the LimitReader returns EOF once the cap is hit, which
		// json.Decode surfaces as an unexpected-EOF error the cycle can log
		// and recover from.
		limited := io.LimitReader(resp.Body, config.NWSResponseMaxBytes)
		var alerts AlertsResponse
		decodeErr := json.NewDecoder(limited).Decode(&alerts)
		_ = resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding alerts: %w", decodeErr)
		}
		return &alerts, nil
	}
	return nil, lastErr
}

// retryAfterErr and retryAfterResp both return false when the caller
// should stop retrying (attempts exhausted or ctx budget too tight) and
// true after successfully sleeping through the computed backoff.
func (c *Client) retryAfterErr(ctx context.Context, attempt int) bool {
	if attempt == retry.MaxAttempts-1 {
		return false
	}
	delay, ok := retry.NextDelay(ctx, attempt, 0, false)
	if !ok {
		return false
	}
	return retry.Sleep(ctx, delay) == nil
}

func (c *Client) retryAfterResp(ctx context.Context, attempt int, retryAfter time.Duration, hasRetryAfter bool) bool {
	if attempt == retry.MaxAttempts-1 {
		return false
	}
	delay, ok := retry.NextDelay(ctx, attempt, retryAfter, hasRetryAfter)
	if !ok {
		return false
	}
	return retry.Sleep(ctx, delay) == nil
}
