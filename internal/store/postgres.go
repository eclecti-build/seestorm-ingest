package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/eclecti-build/seestorm-ingest/internal/nws"
	"github.com/eclecti-build/seestorm-ingest/internal/spc"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connecting to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	return &Store{pool: pool}, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, migrateSQL)
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
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

// ActiveAlertGeoJSON represents a single alert for the snapshot
type ActiveAlertGeoJSON struct {
	NWSID       string          `json:"nws_id"`
	EventType   string          `json:"event_type"`
	Severity    string          `json:"severity"`
	Headline    string          `json:"headline"`
	Description string          `json:"description"`
	AreaDesc    string          `json:"area_desc"`
	Geometry    json.RawMessage `json:"geometry"`
	EffectiveAt time.Time       `json:"effective_at"`
	ExpiresAt   time.Time       `json:"expires_at"`
}

func (s *Store) GetActiveAlerts(ctx context.Context) ([]ActiveAlertGeoJSON, error) {
	rows, err := s.pool.Query(ctx, activeAlertsSQL)
	if err != nil {
		return nil, fmt.Errorf("querying active alerts: %w", err)
	}
	defer rows.Close()

	var alerts []ActiveAlertGeoJSON
	for rows.Next() {
		var a ActiveAlertGeoJSON
		var geomStr *string
		err := rows.Scan(
			&a.NWSID, &a.EventType, &a.Severity, &a.Headline, &a.Description,
			&a.AreaDesc, &geomStr, &a.EffectiveAt, &a.ExpiresAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scanning alert row: %w", err)
		}
		if geomStr != nil {
			a.Geometry = json.RawMessage(*geomStr)
		}
		alerts = append(alerts, a)
	}

	return alerts, nil
}
