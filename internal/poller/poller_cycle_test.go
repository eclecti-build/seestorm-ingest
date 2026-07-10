package poller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclecti-build/seestorm-ingest/internal/healthcheck"
	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
	"github.com/eclecti-build/seestorm-ingest/internal/store"
)

type alertFetchResult struct {
	resp *nws.AlertsResponse
	err  error
}

type fakeAlertFetcher struct {
	resp   *nws.AlertsResponse
	err    error
	got    []string
	script []alertFetchResult
}

func (f *fakeAlertFetcher) FetchActiveAlerts(_ context.Context, area string) (*nws.AlertsResponse, error) {
	f.got = append(f.got, area)
	if len(f.script) > 0 {
		result := f.script[0]
		f.script = f.script[1:]
		return result.resp, result.err
	}
	return f.resp, f.err
}

type fakeReportFetcher struct {
	torn, hail, wind          []spc.StormReport
	tornErr, hailErr, windErr error
}

func (f *fakeReportFetcher) FetchTodayTornadoReports(_ context.Context) ([]spc.StormReport, error) {
	return f.torn, f.tornErr
}

func (f *fakeReportFetcher) FetchTodayHailReports(_ context.Context) ([]spc.StormReport, error) {
	return f.hail, f.hailErr
}

func (f *fakeReportFetcher) FetchTodayWindReports(_ context.Context) ([]spc.StormReport, error) {
	return f.wind, f.windErr
}

type fakeStore struct {
	upsertAlertsScript []error
	gotAlerts          [][]nws.Alert
	gotReports         [][]spc.StormReport
}

func (s *fakeStore) UpsertAlertsBatch(_ context.Context, alerts []nws.Alert) (int, int, bool, error) {
	s.gotAlerts = append(s.gotAlerts, alerts)
	if len(s.upsertAlertsScript) > 0 {
		err := s.upsertAlertsScript[0]
		s.upsertAlertsScript = s.upsertAlertsScript[1:]
		return len(alerts), 0, false, err
	}
	return len(alerts), 0, false, nil
}

func (s *fakeStore) UpsertStormReportsBatch(_ context.Context, reports []spc.StormReport) (int, bool, error) {
	s.gotReports = append(s.gotReports, reports)
	return len(reports), false, nil
}

func (*fakeStore) GetActiveAlerts(context.Context) ([]store.ActiveAlertGeoJSON, error) {
	return nil, nil
}

func (*fakeStore) PurgeExpired(context.Context) (int64, error) {
	return 0, nil
}

func pollCycleConfig(nwsFetcher AlertFetcher, spcFetcher ReportFetcher, alertStore AlertStore, pinger *healthcheck.Pinger) Config {
	return Config{
		Mode:         ModeIngest,
		NWS:          nwsFetcher,
		SPC:          spcFetcher,
		Store:        alertStore,
		PollInterval: time.Second,
		Areas:        []string{"WI"},
		HealthPing:   pinger,
	}
}

func newPingServer(t *testing.T) (url string, pinged chan struct{}, hits *atomic.Int32) {
	t.Helper()

	pinged = make(chan struct{}, 1)
	hits = &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
		pinged <- struct{}{}
	}))
	t.Cleanup(srv.Close)

	return srv.URL, pinged, hits
}

func TestPollCycle_IngestSuccess_UpsertsFetchedAlertsAndPings(t *testing.T) {
	pingURL, pinged, hits := newPingServer(t)

	features := []nws.Alert{{ID: "one"}, {ID: "two"}}
	alerts := &fakeAlertFetcher{resp: &nws.AlertsResponse{Features: features}}
	alertStore := &fakeStore{}
	p := New(pollCycleConfig(alerts, &fakeReportFetcher{}, alertStore, healthcheck.New(pingURL)))

	p.poll(context.Background())

	if !reflect.DeepEqual(alertStore.gotAlerts, [][]nws.Alert{features}) {
		t.Fatalf("upserted alerts = %#v, want %#v", alertStore.gotAlerts, [][]nws.Alert{features})
	}
	if !reflect.DeepEqual(alerts.got, []string{"WI"}) {
		t.Fatalf("fetched areas = %#v, want []string{\"WI\"}", alerts.got)
	}
	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("expected poll cycle heartbeat within 2s")
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("heartbeat hits = %d, want 1", got)
	}
}

func TestPollCycle_AlertsFetchError_WithholdsHeartbeat(t *testing.T) {
	pingURL, pinged, hits := newPingServer(t)

	alerts := &fakeAlertFetcher{err: errors.New("NWS unavailable")}
	alertStore := &fakeStore{}
	p := New(pollCycleConfig(alerts, &fakeReportFetcher{}, alertStore, healthcheck.New(pingURL)))

	p.poll(context.Background())

	select {
	case <-pinged:
		t.Fatal("expected no heartbeat after alerts fetch failure")
	case <-time.After(200 * time.Millisecond):
		// This is a deterministic safety margin, not a race window: poll() evaluates
		// shouldHeartbeat synchronously before it can call PingAsync, so no withheld heartbeat arrives late.
	}
	if len(alertStore.gotAlerts) != 0 {
		t.Fatalf("alerts upsert calls = %d, want 0", len(alertStore.gotAlerts))
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("heartbeat hits = %d, want 0", got)
	}
}

func TestPollCycle_304AfterFailedUpsert_WithholdsHeartbeatUntilRecovery(t *testing.T) {
	pingURL, pinged, hits := newPingServer(t)

	features := []nws.Alert{{ID: "recovering"}}
	alerts := &fakeAlertFetcher{script: []alertFetchResult{
		{resp: &nws.AlertsResponse{Features: features}},
		{err: nws.ErrNotModified},
		{resp: &nws.AlertsResponse{Features: features}},
	}}
	alertStore := &fakeStore{upsertAlertsScript: []error{errors.New("Postgres unavailable"), nil}}
	p := New(pollCycleConfig(alerts, &fakeReportFetcher{}, alertStore, healthcheck.New(pingURL)))

	// Drive three sequential poll() calls on one Poller: etagVouchesForStore reads
	// cross-cycle lastAlertsUpsertFailed state, not three independent cycles.
	p.poll(context.Background())
	select {
	case <-pinged:
		t.Fatal("expected no heartbeat after failed alerts upsert")
	case <-time.After(200 * time.Millisecond):
		// This is a deterministic safety margin, not a race window: poll() evaluates
		// shouldHeartbeat synchronously before it can call PingAsync, so no withheld heartbeat arrives late.
	}

	p.poll(context.Background())
	select {
	case <-pinged:
		t.Fatal("expected no heartbeat for 304 following failed alerts upsert")
	case <-time.After(200 * time.Millisecond):
		// This is a deterministic safety margin, not a race window: poll() evaluates
		// shouldHeartbeat synchronously before it can call PingAsync, so no withheld heartbeat arrives late.
	}

	p.poll(context.Background())
	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("expected heartbeat after successful alerts recovery")
	}
	if len(alertStore.gotAlerts) != 2 {
		t.Fatalf("alerts upsert calls = %d, want 2", len(alertStore.gotAlerts))
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("heartbeat hits = %d, want 1", got)
	}
}

func TestPollCycle_SPCSingleFeedFailure_OthersStillUpserted(t *testing.T) {
	pingURL, pinged, _ := newPingServer(t)

	hail := spc.StormReport{Type: "hail", Location: "Hail town"}
	wind := spc.StormReport{Type: "wind", Location: "Wind town"}
	alerts := &fakeAlertFetcher{resp: &nws.AlertsResponse{}}
	reports := &fakeReportFetcher{
		tornErr: errors.New("tornado feed unavailable"),
		hail:    []spc.StormReport{hail},
		wind:    []spc.StormReport{wind},
	}
	alertStore := &fakeStore{}
	p := New(pollCycleConfig(alerts, reports, alertStore, healthcheck.New(pingURL)))

	p.poll(context.Background())

	wantReports := []spc.StormReport{hail, wind}
	if !reflect.DeepEqual(alertStore.gotReports, [][]spc.StormReport{wantReports}) {
		t.Fatalf("upserted reports = %#v, want %#v", alertStore.gotReports, [][]spc.StormReport{wantReports})
	}
	select {
	case <-pinged:
	case <-time.After(2 * time.Second):
		t.Fatal("expected heartbeat despite one SPC feed failure")
	}
}
