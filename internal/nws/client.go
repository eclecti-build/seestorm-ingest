package nws

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type Client struct {
	httpClient *http.Client
	userAgent  string
	baseURL    string
}

func NewClient(userAgent string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
		userAgent: userAgent,
		baseURL:   "https://api.weather.gov",
	}
}

// FetchActiveAlerts retrieves active alerts for the given area (e.g., "WI")
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

	var alerts AlertsResponse
	if err := json.NewDecoder(resp.Body).Decode(&alerts); err != nil {
		return nil, fmt.Errorf("decoding alerts: %w", err)
	}

	return &alerts, nil
}
