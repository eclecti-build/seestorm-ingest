package spc

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/config"
	"github.com/eclecti-build/seestorm-ingest/internal/retry"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: config.HTTPClientTimeoutSec * time.Second,
		},
		baseURL: "https://www.spc.noaa.gov/climo/reports",
	}
}

// FetchTodayTornadoReports fetches today's tornado reports from SPC
func (c *Client) FetchTodayTornadoReports(ctx context.Context) ([]StormReport, error) {
	return c.fetchReports(ctx, "today_torn.csv", "tornado")
}

// FetchTodayHailReports fetches today's hail reports from SPC
func (c *Client) FetchTodayHailReports(ctx context.Context) ([]StormReport, error) {
	return c.fetchReports(ctx, "today_hail.csv", "hail")
}

// FetchTodayWindReports fetches today's wind reports from SPC
func (c *Client) FetchTodayWindReports(ctx context.Context) ([]StormReport, error) {
	return c.fetchReports(ctx, "today_wind.csv", "wind")
}

func (c *Client) fetchReports(ctx context.Context, file string, reportType string) ([]StormReport, error) {
	endpoint := fmt.Sprintf("%s/%s", c.baseURL, file)

	var lastErr error
	for attempt := 0; attempt < retry.MaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("creating request: %w", err)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetching %s: %w", file, err)
			if !c.retryAfterErr(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			lastErr = fmt.Errorf("SPC returned %d for %s", resp.StatusCode, file)
			if !retry.IsRetryableStatus(resp.StatusCode) {
				return nil, lastErr
			}
			retryAfter, hasRA := retry.ParseRetryAfter(resp.Header.Get("Retry-After"))
			if !c.retryAfterResp(ctx, attempt, retryAfter, hasRA) {
				return nil, lastErr
			}
			continue
		}

		defer resp.Body.Close()

		// Cap upstream body size before parsing. Prevents a runaway CSV from
		// exhausting memory; csv.Reader sees io.EOF at the cap and the caller
		// handles the partial-read error without wedging the pool.
		return parseCSVReports(io.LimitReader(resp.Body, config.SPCResponseMaxBytes), reportType)
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

func parseCSVReports(r io.Reader, reportType string) ([]StormReport, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // Variable fields
	reader.TrimLeadingSpace = true

	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("parsing CSV: %w", err)
	}

	var reports []StormReport

	for i, record := range records {
		// Skip header row
		if i == 0 {
			continue
		}
		if len(record) < 8 {
			continue
		}

		lat, err := strconv.ParseFloat(strings.TrimSpace(record[5]), 64)
		if err != nil {
			continue
		}
		lon, err := strconv.ParseFloat(strings.TrimSpace(record[6]), 64)
		if err != nil {
			continue
		}

		report := StormReport{
			Magnitude: strings.TrimSpace(record[1]),
			Location:  strings.TrimSpace(record[2]),
			County:    strings.TrimSpace(record[3]),
			State:     strings.TrimSpace(record[4]),
			Lat:       lat,
			Lon:       lon,
			Comments:  strings.TrimSpace(record[7]),
			Type:      reportType,
		}

		// Parse time (HHMM format in SPC CSVs)
		timeStr := strings.TrimSpace(record[0])
		if len(timeStr) == 4 {
			hour, _ := strconv.Atoi(timeStr[:2])
			min, _ := strconv.Atoi(timeStr[2:])
			now := time.Now().UTC()
			report.Time = time.Date(now.Year(), now.Month(), now.Day(), hour, min, 0, 0, time.UTC)
		}

		reports = append(reports, report)
	}

	return reports, nil
}
