package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
)

// isFatalBatchErr reports whether a batch-level error should short-circuit
// the per-row fallback path. Context errors (cancel/deadline) mean the whole
// cycle is already giving up — iterating per-row would issue O(n) doomed
// statements against the same unhealthy ctx and end with a silent
// degraded-path "success" that drops the tail. Treat those as fatal.
func isFatalBatchErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := buildPoolConfig(databaseURL)
	if err != nil {
		return nil, err
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Store{pool: pool}, nil
}

// buildPoolConfig parses the database URL and applies the audit-settled pool
// shape (see docs/SWARM_AUDIT_2026-04-18.md "Constants — paste-ready"). Split
// out so unit tests can assert the values without a live database.
//
// Sizing rationale:
//   - MaxConns 16 stays under Neon Launch's default ceiling while leaving
//     headroom for Atlas migrations running alongside the poller.
//   - MinConns 2 keeps a warm pool so the first poll after idle-suspend
//     doesn't pay a cold-connect penalty.
//   - MaxConnIdleTime 4m is deliberately shorter than Neon's ~5m idle-suspend
//     so pgx recycles before the server drops us.
//   - statement_timeout 15s guards against a single query holding a connection
//     through the whole PollCycleTimeoutSec window. Raised from 5s after the
//     2026-04-21 IA outbreak where GetActiveAlerts cursor iteration under
//     heavy alert load tripped the 5s ceiling. Migrate() overrides this with
//     SET LOCAL statement_timeout = 0 inside its transaction so boot-time
//     DDL (CREATE EXTENSION postgis on cold Neon, future CREATE INDEX on a
//     populated table) isn't aborted by the hot-path ceiling.
func buildPoolConfig(databaseURL string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}

	cfg.MaxConns = 16
	cfg.MinConns = 2
	cfg.MaxConnIdleTime = 4 * time.Minute
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000" // ms

	return cfg, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

// Migrate runs schema DDL inside an explicit transaction and disables
// statement_timeout for that transaction only. The shared pool bakes in
// statement_timeout=15000 (see buildPoolConfig) to bound hot-path queries,
// but DDL during boot — CREATE EXTENSION postgis on a cold Neon branch,
// CREATE INDEX on a populated table, future PostGIS additions — can easily
// run past 5s and would otherwise fail the process on startup. SET LOCAL
// scopes the override to this transaction so non-migrate pool checkouts
// still inherit the 5s ceiling.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin migrate tx: %w", err)
	}
	// Safe to call after Commit — pgx makes Rollback a no-op once committed.
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = 0"); err != nil {
		return fmt.Errorf("disabling statement_timeout for migrate: %w", err)
	}

	if _, err := tx.Exec(ctx, migrateSQL); err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrate tx: %w", err)
	}
	return nil
}

func (s *Store) UpsertAlert(ctx context.Context, alert nws.Alert) error {
	props := alert.Properties

	propsJSON, err := json.Marshal(props)
	if err != nil {
		return fmt.Errorf("marshaling properties: %w", err)
	}

	var geomStr *string
	if len(alert.Geometry) > 0 && string(alert.Geometry) != "null" {
		s := string(alert.Geometry)
		geomStr = &s
	}

	effectiveAt, _ := time.Parse(time.RFC3339, props.Effective)
	expiresAt, _ := time.Parse(time.RFC3339, props.Expires)

	_, err = s.pool.Exec(ctx, upsertAlertSQL,
		props.ID,
		props.Event,
		props.Severity,
		props.Headline,
		props.Description,
		props.Instruction,
		props.AreaDesc,
		props.SenderName,
		geomStr,
		propsJSON,
		effectiveAt,
		expiresAt,
	)
	if err != nil {
		return fmt.Errorf("upserting alert %s: %w", props.ID, err)
	}

	return nil
}

// UpsertAlertsBatch writes every alert in a single transaction using pgx.Batch,
// and falls back to per-alert inserts if the batch transaction fails so one
// malformed alert doesn't cost us 19 good ones in the same cycle. See audit
// Open Decisions #8 (whole-batch transaction first; degraded path logged).
//
// Returns (inserted, degraded, err). `inserted` counts successful upserts;
// `degraded` is true iff we took the per-alert fallback path. `err` is only
// non-nil for ctx cancellation — individual row failures are logged and skipped.
func (s *Store) UpsertAlertsBatch(ctx context.Context, alerts []nws.Alert) (int, bool, error) {
	if len(alerts) == 0 {
		return 0, false, nil
	}

	// Happy path: single transaction, all-or-nothing for the commit itself.
	inserted, err := s.upsertAlertsBatchTx(ctx, alerts)
	if err == nil {
		return inserted, false, nil
	}
	// Distinguish ctx/infra failure from data-shape failure. Context errors
	// mean the cycle deadline fired or shutdown is in progress — retrying
	// per-row against the same dead ctx just burns cycles and silently drops
	// rows. Surface the ctx error instead of entering the fallback loop.
	if isFatalBatchErr(err) {
		return 0, false, err
	}
	if ctx.Err() != nil {
		return 0, false, ctx.Err()
	}

	// Degraded path: the batch/tx failed (likely one malformed row poisoned
	// the commit). Fall back to per-alert inserts so the good alerts still
	// land. Logged explicitly so ops can tell the two paths apart.
	slog.WarnContext(ctx, "batch upsert failed, falling back to per-alert",
		"degraded_path", "batch_upsert_fallback",
		"alert_count", len(alerts),
		"error", err,
	)

	count := 0
	for _, alert := range alerts {
		if err := s.UpsertAlert(ctx, alert); err != nil {
			// If the ctx died mid-fallback (cycle deadline fired,
			// shutdown signal), stop iterating and surface the error.
			// Continuing would log O(remaining) identical failures and
			// still end with degraded=true, which is worse than bailing.
			if isFatalBatchErr(err) {
				return count, true, err
			}
			slog.ErrorContext(ctx, "failed to upsert alert (fallback)",
				"nws_id", alert.Properties.ID,
				"error", err,
			)
			continue
		}
		count++
	}
	return count, true, nil
}

// upsertAlertsBatchTx runs the whole batch inside a single transaction. A
// failure anywhere in Exec() results rolls the entire batch back — the caller
// is expected to fall back to per-row inserts on error.
func (s *Store) upsertAlertsBatchTx(ctx context.Context, alerts []nws.Alert) (int, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	// Safe to call after a successful Commit — pgx makes Rollback a no-op
	// once the tx is committed, which keeps the defer simple.
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, alert := range alerts {
		args, err := alertUpsertArgs(alert)
		if err != nil {
			// A marshal failure pre-queue is a data-shape bug, not a DB
			// issue. Abort the batch so the caller can drop into the
			// per-row fallback path where the bad row's error is isolated.
			return 0, fmt.Errorf("marshaling alert %s: %w", alert.Properties.ID, err)
		}
		batch.Queue(upsertAlertSQL, args...)
	}

	br := tx.SendBatch(ctx, batch)
	var batchErr error
	for range alerts {
		if _, err := br.Exec(); err != nil && batchErr == nil {
			batchErr = err
		}
	}
	if closeErr := br.Close(); closeErr != nil && batchErr == nil {
		batchErr = closeErr
	}
	if batchErr != nil {
		return 0, fmt.Errorf("batch exec: %w", batchErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return len(alerts), nil
}

// alertUpsertArgs extracts the parameter list for a single alert upsert.
// Shared by the per-row path and the batch path so the argument order stays
// in lockstep with upsertAlertSQL.
func alertUpsertArgs(alert nws.Alert) ([]any, error) {
	props := alert.Properties

	propsJSON, err := json.Marshal(props)
	if err != nil {
		return nil, fmt.Errorf("marshaling properties: %w", err)
	}

	var geomStr *string
	if len(alert.Geometry) > 0 && string(alert.Geometry) != "null" {
		s := string(alert.Geometry)
		geomStr = &s
	}

	effectiveAt, _ := time.Parse(time.RFC3339, props.Effective)
	expiresAt, _ := time.Parse(time.RFC3339, props.Expires)

	return []any{
		props.ID,
		props.Event,
		props.Severity,
		props.Headline,
		props.Description,
		props.Instruction,
		props.AreaDesc,
		props.SenderName,
		geomStr,
		propsJSON,
		effectiveAt,
		expiresAt,
	}, nil
}

// UpsertStormReportsBatch writes every storm report in a single transaction
// via pgx.Batch, falling back to per-report inserts on failure. Same shape as
// UpsertAlertsBatch — see that doc comment for semantics.
func (s *Store) UpsertStormReportsBatch(ctx context.Context, reports []spc.StormReport) (int, bool, error) {
	if len(reports) == 0 {
		return 0, false, nil
	}

	inserted, err := s.upsertStormReportsBatchTx(ctx, reports)
	if err == nil {
		return inserted, false, nil
	}
	// See UpsertAlertsBatch for the rationale — ctx errors should short-circuit
	// the fallback, not fan out into O(n) doomed per-row retries.
	if isFatalBatchErr(err) {
		return 0, false, err
	}
	if ctx.Err() != nil {
		return 0, false, ctx.Err()
	}

	slog.WarnContext(ctx, "batch upsert failed, falling back to per-report",
		"degraded_path", "batch_upsert_fallback",
		"report_count", len(reports),
		"error", err,
	)

	count := 0
	for _, report := range reports {
		if err := s.UpsertStormReport(ctx, report); err != nil {
			if isFatalBatchErr(err) {
				return count, true, err
			}
			slog.ErrorContext(ctx, "failed to upsert storm report (fallback)", "error", err)
			continue
		}
		count++
	}
	return count, true, nil
}

func (s *Store) upsertStormReportsBatchTx(ctx context.Context, reports []spc.StormReport) (int, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	batch := &pgx.Batch{}
	for _, r := range reports {
		batch.Queue(upsertStormReportSQL,
			r.Type, r.Magnitude, r.Location, r.County, r.State, r.Comments,
			r.Lon, r.Lat, r.Time,
		)
	}

	br := tx.SendBatch(ctx, batch)
	var batchErr error
	for range reports {
		if _, err := br.Exec(); err != nil && batchErr == nil {
			batchErr = err
		}
	}
	if closeErr := br.Close(); closeErr != nil && batchErr == nil {
		batchErr = closeErr
	}
	if batchErr != nil {
		return 0, fmt.Errorf("batch exec: %w", batchErr)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit tx: %w", err)
	}
	return len(reports), nil
}

func (s *Store) UpsertStormReport(ctx context.Context, report spc.StormReport) error {
	_, err := s.pool.Exec(ctx, upsertStormReportSQL,
		report.Type,
		report.Magnitude,
		report.Location,
		report.County,
		report.State,
		report.Comments,
		report.Lon,
		report.Lat,
		report.Time,
	)
	if err != nil {
		return fmt.Errorf("upserting storm report: %w", err)
	}

	return nil
}

// ActiveAlertGeoJSON represents a single alert for the snapshot.
//
// `States` is the set of US state abbreviations the alert covers, derived from
// the NWS `properties.geocode.SAME` codes (preferred) or parsed from
// `area_desc` as a fallback. Plural because cross-border alerts (Mississippi
// River flooding, multi-state derechos) are real and the client filters on
// this field when scoping by user location.
type ActiveAlertGeoJSON struct {
	NWSID       string           `json:"nws_id"`
	EventType   string           `json:"event_type"`
	Severity    string           `json:"severity"`
	Headline    string           `json:"headline"`
	Description string           `json:"description"`
	AreaDesc    string           `json:"area_desc"`
	States      []string         `json:"states"`
	Geometry    json.RawMessage  `json:"geometry"`
	EffectiveAt time.Time        `json:"effective_at"`
	ExpiresAt   time.Time        `json:"expires_at"`
	StormMotion *nws.StormMotion `json:"storm_motion,omitempty"`
	WarningTags *nws.WarningTags `json:"warning_tags,omitempty"`
}

// alertPropsParams is the minimal shape we unmarshal out of the JSONB
// `properties` column. We surface `parameters` for motion parsing and
// `geocode` for state derivation. Decoding the full AlertProperties
// here is wasteful — we only need these two fields. We share `nws.AlertGeocode`
// with the upstream marshaler so the wire shape can't silently drift.
type alertPropsParams struct {
	Parameters map[string][]string `json:"parameters"`
	Geocode    nws.AlertGeocode    `json:"geocode"`
}

func (s *Store) GetActiveAlerts(ctx context.Context) ([]ActiveAlertGeoJSON, error) {
	rows, err := s.pool.Query(ctx, activeAlertsSQL)
	if err != nil {
		return nil, fmt.Errorf("querying active alerts: %w", err)
	}
	defer rows.Close()

	var (
		alerts         []ActiveAlertGeoJSON
		motionParsed   int
		motionAbsent   int
		motionFailed   int
		statesFromSAME int
		statesFallback int
	)
	for rows.Next() {
		var a ActiveAlertGeoJSON
		var geomStr *string
		var propsJSON []byte
		err := rows.Scan(
			&a.NWSID, &a.EventType, &a.Severity, &a.Headline, &a.Description,
			&a.AreaDesc, &geomStr, &a.EffectiveAt, &a.ExpiresAt, &propsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning alert row: %w", err)
		}
		if geomStr != nil {
			a.Geometry = json.RawMessage(*geomStr)
		}

		// Pull only `parameters` out of the JSONB — that's all the
		// motion parser needs. A malformed JSONB row shouldn't drop
		// the alert; log and proceed with a zero map.
		var props alertPropsParams
		if len(propsJSON) > 0 {
			if err := json.Unmarshal(propsJSON, &props); err != nil {
				slog.WarnContext(ctx, "alert properties JSONB decode failed",
					"nws_id", a.NWSID,
					"error", err,
				)
			}
		}

		motion, err := nws.ParseEventMotion(props.Parameters)
		switch {
		case err != nil:
			motionFailed++
			slog.WarnContext(ctx, "storm motion parse failed",
				"nws_id", a.NWSID,
				"error", err,
			)
		case motion == nil:
			motionAbsent++
		default:
			motionParsed++
		}
		// Motion is optional — the alert stays in the snapshot whether
		// or not we got a vector.
		a.StormMotion = motion

		// Derive states from SAME codes (preferred — structured, unambiguous).
		// Fall back to area_desc parsing only if no SAME codes are present
		// or none resolved to a known state. The fallback counter exists so
		// we can spot upstream changes (e.g. NWS dropping geocode.SAME) in
		// log aggregation rather than discovering it during an event.
		states, usedFallback := deriveStates(props.Geocode.SAME, a.AreaDesc)
		if usedFallback {
			statesFallback++
		} else {
			statesFromSAME++
		}
		a.States = states

		// Parse IBW warning tags for Tornado / Severe Thunderstorm /
		// Flash Flood warnings. Watches + statements lack the `&&`
		// block, so ParseWarningTags returns (nil, nil) for them and
		// the omitempty JSON tag keeps the snapshot small.
		tags, err := nws.ParseWarningTags(a.Description)
		if err != nil {
			slog.WarnContext(ctx, "warning tag parse failed",
				"nws_id", a.NWSID,
				"error", err,
			)
		}
		a.WarningTags = tags
		alerts = append(alerts, a)
	}
	// pgx's rows.Next() returns false on both clean iteration end AND a
	// mid-cursor error (network drop, server abort, etc.). Without this
	// check a partial failure would silently publish a truncated alert
	// snapshot — unacceptable for a public-safety feed where dropping a
	// tornado warning could put people at risk.
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("reading active alerts: %w", err)
	}

	slog.InfoContext(ctx, "snapshot motion stats",
		"parsed", motionParsed,
		"absent", motionAbsent,
		"failed", motionFailed,
	)

	slog.InfoContext(ctx, "snapshot state derivation stats",
		"from_same", statesFromSAME,
		"from_area_desc_fallback", statesFallback,
	)

	return alerts, nil
}

// deriveStates resolves the set of US state abbreviations covered by an
// alert. Preferred source is the NWS `geocode.SAME` codes (structured,
// unambiguous — first 2 digits map to the FIPS state). Fallback is naive
// parsing of `area_desc` for trailing `, XX` tokens (NWS uses the format
// "County Name, XX" separated by `;`).
//
// Returns (states, usedFallback). `usedFallback` is true when SAME yielded
// no recognized states and area_desc parsing was used. Callers should
// instrument this in log aggregation to surface upstream changes.
func deriveStates(sameCodes []string, areaDesc string) ([]string, bool) {
	seen := map[string]struct{}{}
	for _, code := range sameCodes {
		if state, ok := nws.StateForSAMECode(code); ok {
			seen[state] = struct{}{}
		}
	}

	usedFallback := false
	if len(seen) == 0 && areaDesc != "" {
		usedFallback = true
		for _, segment := range strings.Split(areaDesc, ";") {
			segment = strings.TrimSpace(segment)
			if comma := strings.LastIndex(segment, ","); comma >= 0 {
				candidate := strings.TrimSpace(segment[comma+1:])
				if len(candidate) == 2 {
					upper := strings.ToUpper(candidate)
					// Only accept if the abbreviation is a known state code.
					// Guards against "NEAR THE LAKE, ETC" garbage.
					if nws.IsValidStateCode(upper) {
						seen[upper] = struct{}{}
					}
				}
			}
		}
	}

	// Always return a non-nil slice so the wire shape is uniform.
	// `States []string` has no `omitempty` and Go marshals nil slices as
	// `null` — that breaks the v2 contract (`states[]` is documented as an
	// array). Empty alerts therefore must serialize to `[]`, not `null`.
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, usedFallback
}
