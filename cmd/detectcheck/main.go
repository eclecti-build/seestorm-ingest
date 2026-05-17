// Command detectcheck is a LOCAL, UNCOMMITTED validation harness.
//
// It shows the difference the new tornado-detection derivation makes:
// for every tornado-related alert it prints the RAW NWS inputs
// (parameters.tornadoDetection / tornadoDamageThreat + the relevant
// SOURCE.../HAZARD... narrative line) next to the DERIVED, normalized
// `tornado` object produced by nws.DetectTornado.
//
// No database, no snapshot, no R2 — it talks only to the public
// api.weather.gov alerts endpoint (same client the poller uses), so it
// runs anywhere with network and zero config.
//
// Usage:
//
//	go run ./cmd/detectcheck                 # live: WI + Great Lakes neighbors
//	go run ./cmd/detectcheck -area WI         # live: a specific area list
//	go run ./cmd/detectcheck -file alerts.json# offline: a saved api.weather.gov AlertsResponse
//	go run ./cmd/detectcheck -samples         # built-in synthetic samples only
//
// This file is intentionally not wired into the build and is meant to
// stay uncommitted — it is a developer validation aid, not a service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
)

func main() {
	area := flag.String("area", "WI,MN,IL,IA,IN,MI", "comma-separated NWS area list (live mode)")
	file := flag.String("file", "", "path to a saved api.weather.gov AlertsResponse JSON (offline mode)")
	samplesOnly := flag.Bool("samples", false, "only run the built-in synthetic samples")
	flag.Parse()

	fmt.Println("=== detectcheck — tornado detection derivation, before/after ===")

	// Built-in synthetic samples guarantee a visible demonstration of how
	// the data is interpreted even when no tornado is active anywhere.
	runSamples()
	if *samplesOnly {
		return
	}

	var resp *nws.AlertsResponse
	var err error
	if *file != "" {
		resp, err = loadFile(*file)
		fmt.Printf("\n--- offline: %s ---\n", *file)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client := nws.NewClient("(seestorm.org detectcheck, contact@seestorm.org)")
		fmt.Printf("\n--- live: api.weather.gov/alerts/active?area=%s ---\n", *area)
		resp, err = client.FetchActiveAlerts(ctx, *area)
	}
	if err != nil {
		fmt.Printf("fetch/load failed: %v\n", err)
		os.Exit(1)
	}

	total, tornadoish := 0, 0
	for _, a := range resp.Features {
		total++
		p := a.Properties
		if !isTornadoish(p) {
			continue
		}
		tornadoish++
		report(p.Event, p.AreaDesc, p.Parameters, p.Description)
	}
	fmt.Printf("\nsummary: %d active alerts, %d tornado-related\n", total, tornadoish)
	if tornadoish == 0 {
		fmt.Println("(no tornado-related alerts active right now — the synthetic samples above still demonstrate the derivation)")
	}
}

// isTornadoish keeps the report focused on alerts where detection is
// meaningful: any Tornado product, or any alert carrying the structured
// tornado parameters / the embedded-tornado tag (SVR with TORNADO...).
func isTornadoish(p nws.AlertProperties) bool {
	if strings.Contains(strings.ToLower(p.Event), "tornado") {
		return true
	}
	if _, ok := p.Parameters["tornadoDetection"]; ok {
		return true
	}
	if _, ok := p.Parameters["tornadoDamageThreat"]; ok {
		return true
	}
	return strings.Contains(strings.ToUpper(p.Description), "TORNADO...")
}

func report(event, area string, params map[string][]string, desc string) {
	fmt.Printf("\n• %s — %s\n", event, area)
	fmt.Printf("  RAW   tornadoDetection=%v tornadoDamageThreat=%v\n",
		params["tornadoDetection"], params["tornadoDamageThreat"])
	if line := firstNarrativeLine(desc); line != "" {
		fmt.Printf("  RAW   %s\n", line)
	}

	det, err := nws.DetectTornado(params, desc)
	switch {
	case err != nil:
		fmt.Printf("  DERIVED  (drift) error: %v\n", err)
	case det == nil:
		fmt.Printf("  DERIVED  (nil) — not a tornado-warning detection\n")
	default:
		b, _ := json.Marshal(det)
		fmt.Printf("  DERIVED  %s\n", b)
		fmt.Printf("           => label \"%s\"\n", label(det))
	}
}

// label mirrors the anti-drift display vocabulary the client will use, so
// the harness output reads the way the UI will read.
func label(d *nws.TornadoDetection) string {
	if !d.Confirmed {
		return "Tornado Warning — Radar Indicated"
	}
	switch d.DamageThreat {
	case nws.DamageThreatCatastrophic:
		return "Tornado Emergency"
	case nws.DamageThreatConsiderable:
		return "Particularly Dangerous Tornado Warning"
	default:
		return "Tornado Warning — Confirmed"
	}
}

func firstNarrativeLine(desc string) string {
	for _, ln := range strings.Split(desc, "\n") {
		ln = strings.TrimSpace(ln)
		u := strings.ToUpper(ln)
		if strings.HasPrefix(u, "SOURCE...") || strings.HasPrefix(u, "HAZARD...") {
			return ln
		}
	}
	return ""
}

func loadFile(path string) (*nws.AlertsResponse, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // local dev tool, operator-supplied path
	if err != nil {
		return nil, err
	}
	var resp nws.AlertsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// runSamples feeds NWS-shaped fixtures through DetectTornado so the
// derivation is always demonstrable, including the exact SVS-update shape
// that motivated the parser.
func runSamples() {
	type sample struct {
		name   string
		params map[string][]string
		desc   string
	}
	samples := []sample{
		{
			name:   "SVS update, structured RADAR INDICATED (the motivating case)",
			params: map[string][]string{"tornadoDetection": {"RADAR INDICATED"}},
			desc:   "HAZARD...Tornado.\n\nSOURCE...Radar indicated rotation.\n\nIMPACT...Flying debris will be dangerous.",
		},
		{
			name:   "Tornado Warning, structured OBSERVED",
			params: map[string][]string{"tornadoDetection": {"OBSERVED"}},
			desc:   "HAZARD...Tornado.\n\nSOURCE...Weather spotters confirmed tornado.",
		},
		{
			name: "PDS — OBSERVED + CONSIDERABLE",
			params: map[string][]string{
				"tornadoDetection":    {"OBSERVED"},
				"tornadoDamageThreat": {"CONSIDERABLE"},
			},
			desc: "HAZARD...Damaging tornado.\n\nSOURCE...Law enforcement confirmed tornado.",
		},
		{
			name: "Tornado Emergency — OBSERVED + CATASTROPHIC",
			params: map[string][]string{
				"tornadoDetection":    {"OBSERVED"},
				"tornadoDamageThreat": {"CATASTROPHIC"},
			},
			desc: "HAZARD...Deadly tornado.\n\nSOURCE...Confirmed large and destructive tornado.",
		},
		{
			name:   "SVR embedded tornado — structured POSSIBLE (must NOT overclaim)",
			params: map[string][]string{"tornadoDetection": {"POSSIBLE"}},
			desc:   "HAZARD...60 mph wind gusts and a tornado possible.",
		},
	}
	fmt.Println("\n--- built-in synthetic samples ---")
	for _, s := range samples {
		fmt.Printf("\n• %s\n", s.name)
		report("(sample)", s.name, s.params, s.desc)
	}
}
