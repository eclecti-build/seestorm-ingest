package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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
// `geocode.SAME` for state derivation. Decoding the full AlertProperties
// here is wasteful — we only need these two slices.
type alertPropsParams struct {
	Parameters map[string][]string `json:"parameters"`
	Geocode    struct {
		SAME []string `json:"SAME"`
		UGC  []string `json:"UGC"`
	} `json:"geocode"`
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
					if _, ok := fipsToStateAbbrevSet[upper]; ok {
						seen[upper] = struct{}{}
					}
				}
			}
		}
	}

	if len(seen) == 0 {
		return nil, usedFallback
	}

	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out, usedFallback
}

// fipsToStateAbbrevSet is the set of valid USPS state abbreviations used to
// validate area_desc fallback parsing. Built once from the nws FIPS table.
var fipsToStateAbbrevSet = func() map[string]struct{} {
	// Hardcoded list mirrors the values in nws.fipsStateCodes. Kept here as
	// a private set rather than exporting from nws to avoid a circular
	// dependency or a public API that would have to be kept in sync.
	abbrevs := []string{
		"AL", "AK", "AZ", "AR", "CA", "CO", "CT", "DE", "DC", "FL",
		"GA", "HI", "ID", "IL", "IN", "IA", "KS", "KY", "LA", "ME",
		"MD", "MA", "MI", "MN", "MS", "MO", "MT", "NE", "NV", "NH",
		"NJ", "NM", "NY", "NC", "ND", "OH", "OK", "OR", "PA", "RI",
		"SC", "SD", "TN", "TX", "UT", "VT", "VA", "WA", "WV", "WI",
		"WY", "AS", "GU", "MP", "PR", "VI",
	}
	set := make(map[string]struct{}, len(abbrevs))
	for _, a := range abbrevs {
		set[a] = struct{}{}
	}
	return set
}()
