package store

// migrateSQL is the authoritative schema definition for this service.
//
// At every startup, Store.Migrate executes this block inside an explicit
// transaction. All statements use CREATE ... IF NOT EXISTS so the block is
// idempotent on an already-provisioned database. This is the actual mechanism
// that creates and maintains the schema — not Ent or Atlas.
//
// Ent entity schemas (ent/schema/) and the Atlas configuration (atlas.hcl,
// ent/migrate/migrations/) are scaffolded but not yet authoritative at
// runtime; migrating to Atlas-managed declarative migrations is planned
// future work (tracked in the project issue tracker).
const migrateSQL = `
CREATE EXTENSION IF NOT EXISTS postgis;

CREATE TABLE IF NOT EXISTS weather_events (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    nws_id        TEXT UNIQUE NOT NULL,
    event_type    TEXT NOT NULL,
    severity      TEXT,
    headline      TEXT,
    description   TEXT,
    instruction   TEXT,
    area_desc     TEXT,
    sender_name   TEXT,
    geometry      GEOMETRY(Geometry, 4326),
    properties    JSONB,
    effective_at  TIMESTAMPTZ,
    expires_at    TIMESTAMPTZ,
    ingested_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at    TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_events_geometry ON weather_events USING GIST(geometry);
CREATE INDEX IF NOT EXISTS idx_events_type_time ON weather_events(event_type, effective_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_nws_id ON weather_events(nws_id);
CREATE INDEX IF NOT EXISTS idx_events_expires ON weather_events(expires_at DESC);

-- PR2: durable supersession. retired_at marks a row superseded by a later
-- message's references[] (soft-delete; nullable so ADD COLUMN is backward-
-- compatible and the rolling fleet deploy is order-safe). The partial index
-- serves the hot snapshot query (expires_at > NOW() AND retired_at IS NULL).
ALTER TABLE weather_events ADD COLUMN IF NOT EXISTS retired_at TIMESTAMPTZ;
CREATE INDEX IF NOT EXISTS idx_events_active ON weather_events (expires_at DESC) WHERE retired_at IS NULL;

CREATE TABLE IF NOT EXISTS storm_reports (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    report_type   TEXT NOT NULL,
    magnitude     TEXT,
    location      TEXT,
    county        TEXT,
    state         TEXT,
    comments      TEXT,
    geometry      GEOMETRY(Point, 4326),
    reported_at   TIMESTAMPTZ NOT NULL,
    ingested_at   TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(report_type, location, reported_at)
);

CREATE INDEX IF NOT EXISTS idx_reports_geometry ON storm_reports USING GIST(geometry);
CREATE INDEX IF NOT EXISTS idx_reports_type_time ON storm_reports(report_type, reported_at DESC);
`

const upsertAlertSQL = `
INSERT INTO weather_events (
    nws_id, event_type, severity, headline, description, instruction,
    area_desc, sender_name, geometry, properties, effective_at, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8,
    ST_GeomFromGeoJSON($9), $10, $11, $12
)
ON CONFLICT (nws_id) DO UPDATE SET
    event_type = EXCLUDED.event_type,
    severity = EXCLUDED.severity,
    headline = EXCLUDED.headline,
    description = EXCLUDED.description,
    instruction = EXCLUDED.instruction,
    area_desc = EXCLUDED.area_desc,
    geometry = EXCLUDED.geometry,
    properties = EXCLUDED.properties,
    expires_at = EXCLUDED.expires_at,
    updated_at = NOW()
    -- PR2 Decision 6: retired_at is deliberately NOT refreshed here. A stale
    -- re-insert of a retired id must keep it retired; adding retired_at to this
    -- SET list would resurrect superseded rows. Covered by the ON CONFLICT
    -- invariant test in supersession_integration_test.go.
`

const upsertStormReportSQL = `
INSERT INTO storm_reports (
    report_type, magnitude, location, county, state, comments,
    geometry, reported_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    ST_SetSRID(ST_MakePoint($7, $8), 4326), $9
)
ON CONFLICT (report_type, location, reported_at) DO NOTHING
`

const activeAlertsSQL = `
SELECT
    nws_id, event_type, severity, headline, description,
    area_desc, ST_AsGeoJSON(geometry) as geometry,
    effective_at, expires_at, properties
FROM weather_events
WHERE expires_at > NOW() AND retired_at IS NULL
ORDER BY effective_at DESC
`

// retireByReferenceSQL marks superseded rows retired. $1 is the array of
// referenced prior nws_ids; $2 is the superseding message's event_type (PR2
// Decision 3 gate). Idempotent: WHERE retired_at IS NULL means a re-run is a
// no-op, and an absent referenced id simply matches zero rows.
const retireByReferenceSQL = `
UPDATE weather_events
SET retired_at = NOW()
WHERE nws_id = ANY($1) AND retired_at IS NULL AND event_type = $2
`

// purgeExpiredSQL hard-deletes every expired row. Any row with expires_at in the
// past is invisible to the snapshot (WHERE expires_at > NOW()) and read by
// nothing else — history is built from archived R2 snapshots, not DB rows — so
// it is dead weight. Deletes naturally-expired AND superseded-then-expired rows.
const purgeExpiredSQL = `DELETE FROM weather_events WHERE expires_at < NOW()`
