package nws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
)

type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
}

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
	}
}

// FetchActiveAlerts retrieves active alerts for the given area. The api.weather.gov
// `area` parameter accepts either a single state code (e.g. "WI") or a
// comma-separated list (e.g. "MN,WI,IL,IN,MI,OH,PA,NY"); the API returns a
// single merged FeatureCollection so multi-state polling is one HTTP call.
func (c *Client) FetchActiveAlerts(ctx context.Context, area string) (*AlertsResponse, error) {
	params := url.Values{
		"area":   {area},
		"status": {"actual"},
	}

	endpoint := fmt.Sprintf("%s/alerts/active?%s", c.baseURL, params.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/geo+json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching alerts: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("NWS API returned %d: %s", resp.StatusCode, string(body))
	}

	// Cap upstream body size before decoding. A runaway or malicious NWS
	// response shouldn't be able to exhaust memory or stall the poll cycle
	// — the LimitReader returns EOF once the cap is hit, which json.Decode
	// surfaces as an unexpected-EOF error the cycle can log and recover from.
	limited := io.LimitReader(resp.Body, config.NWSResponseMaxBytes)

	var alerts AlertsResponse
	if err := json.NewDecoder(limited).Decode(&alerts); err != nil {
		return nil, fmt.Errorf("decoding alerts: %w", err)
	}

	return &alerts, nil
}
