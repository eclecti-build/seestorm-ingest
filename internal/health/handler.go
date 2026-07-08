package health

import (
	"encoding/json"
	"net/http"
	"time"
)

type staleFeed struct {
	Feed           string `json:"feed"`
	LastSuccess    string `json:"last_success,omitempty"`
	NeverSucceeded bool   `json:"never_succeeded,omitempty"`
}

type healthResponse struct {
	Status     string      `json:"status"`
	StaleFeeds []staleFeed `json:"stale_feeds,omitempty"`
}

// NewHandler builds the /healthz http.Handler. maxAge is typically
// StalenessMultiplier * PollInterval; feeds is the mode-scoped required
// set from RequiredFeeds. now is injectable so tests don't depend on
// wall-clock time; production callers pass time.Now.
func NewHandler(reg *Registry, feeds []Feed, maxAge time.Duration, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cutoff := now().Add(-maxAge)
		var stale []staleFeed
		for _, f := range feeds {
			t, ok := reg.LastSuccess(f)
			if !ok {
				stale = append(stale, staleFeed{Feed: string(f), NeverSucceeded: true})
				continue
			}
			if t.Before(cutoff) {
				stale = append(stale, staleFeed{Feed: string(f), LastSuccess: t.UTC().Format(time.RFC3339)})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if len(stale) == 0 {
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(healthResponse{Status: "stale", StaleFeeds: stale})
	})
}

// StalenessMultiplier is how many PollIntervals a required feed may go
// without a recorded success before /healthz reports it stale. Re-exported
// from config so callers don't need a separate import just for this one
// number.
const StalenessMultiplier = healthStalenessMultiplier

// healthStalenessMultiplier mirrors config.HealthStalenessMultiplier.
// Duplicated as a plain const (rather than importing internal/config)
// because internal/config has no reason to depend on internal/health or
// vice versa, and this package should stay leaf-level; main.go computes
// the actual maxAge duration from config.HealthStalenessMultiplier *
// pollInterval directly and passes it to NewHandler, so this constant
// here is documentation/tests-only convenience, not load-bearing.
const healthStalenessMultiplier = 3
