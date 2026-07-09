// Package healthcheck implements a dead-man's-switch heartbeat to an
// external monitoring service (healthchecks.io). Ingestion/publishing can
// fail silently for hours — the only existing signal is slog to stdout,
// which no one watches in real time (2026-04-18 swarm audit, keystone
// finding). Pinger closes that gap with the minimal credible mechanism: an
// HTTP GET to a per-app "ping URL" at the end of every successfully
// completed poll cycle. The external service pages a human by email after
// N consecutive missed pings — SeeStorm doesn't host or build any alerting
// logic itself.
//
// Design constraints (Tier 3 infra-maturity plan, Task 1):
//   - non-blocking: PingAsync fires in its own goroutine with a short,
//     independent timeout, so a slow or fully dead ping endpoint can never
//     delay the poll cycle that triggered it.
//   - no-op when unconfigured: an empty URL makes every method a no-op, so
//     local dev (no HEALTHCHECK_PING_URL) needs zero special-casing.
//   - zero new dependencies: net/http only.
package healthcheck

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// pingTimeout bounds a single ping attempt. Generous relative to a typical
// healthchecks.io response (sub-200ms) but short enough that even a fully
// hung endpoint can't accumulate goroutines faster than one per poll cycle
// (30s default) unless something is catastrophically wrong upstream.
const pingTimeout = 5 * time.Second

// Pinger sends a fire-and-forget heartbeat to an external dead-man's-switch
// URL. The zero value (empty URL) is a valid no-op Pinger — safe for local
// dev and any app that hasn't set HEALTHCHECK_PING_URL.
type Pinger struct {
	// URL is the per-app ping endpoint, e.g. https://hc-ping.com/<uuid>.
	// Empty disables pinging entirely.
	URL string
	// Client performs the HTTP GET. Defaults to a client with pingTimeout
	// when nil. Overridable in tests to point at an httptest.Server or to
	// inject a transport that never responds.
	Client *http.Client
}

// New constructs a Pinger from HEALTHCHECK_PING_URL's value. An empty
// string yields a no-op Pinger — callers never need to nil-check.
func New(url string) *Pinger {
	return &Pinger{URL: url}
}

func (p *Pinger) httpClient() *http.Client {
	if p.Client != nil {
		return p.Client
	}
	return &http.Client{Timeout: pingTimeout}
}

// PingAsync fires a success heartbeat in a new goroutine and returns
// immediately. It NEVER blocks the caller and never returns an error — by
// design, a dead or slow ping endpoint must not affect the poll cycle that
// triggered it. Failures are logged at WARN so operators can spot a
// persistently broken ping URL in slog output, but the external service
// (healthchecks.io) is the actual alerting mechanism: N consecutive missed
// pings there is what pages a human.
//
// parent is used only to derive a request-scoped logger context; the
// ping's own timeout is deliberately independent of parent's lifecycle —
// see ping() for why.
func (p *Pinger) PingAsync(parent context.Context) {
	if p.URL == "" {
		return // no-op: HEALTHCHECK_PING_URL unset (local dev)
	}
	go p.ping(parent)
}

func (p *Pinger) ping(parent context.Context) {
	// Deliberately context.Background(), not parent: PingAsync is called at
	// the very end of poll(), whose caller cancels/lets pollCtx expire right
	// after. Deriving from parent would let cycle teardown race-cancel the
	// in-flight ping before pingTimeout elapses. The independent pingTimeout
	// is the only bound we want on this background call.
	//
	// Accepted edge case, noted explicitly: on process shutdown (SIGTERM, or
	// the top-level context being cancelled — not just one cycle's pollCtx),
	// this final goroutine is abandoned mid-flight along with the rest of the
	// process; nothing drains or awaits it. This is accepted, not fixed: it
	// costs at most one missed ping, and healthchecks.io's Grace window
	// already exists to absorb exactly that — a normal rolling
	// restart/redeploy looks identical to "one ping didn't arrive" from the
	// monitoring side, which does not itself trigger an alert.
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		slog.WarnContext(parent, "healthcheck ping: build request failed", "error", err)
		return
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		slog.WarnContext(parent, "healthcheck ping failed", "error", err)
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 300 {
		slog.WarnContext(parent, "healthcheck ping non-2xx response", "status", resp.StatusCode)
	}
}
