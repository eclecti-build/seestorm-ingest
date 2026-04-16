# SeeStorm Ingest

Go service that polls NWS/SPC APIs and archives weather events to PostGIS.

## Stack
- Go 1.22+
- PostgreSQL + PostGIS (Neon)
- Deployed to Fly.io (Chicago region, closest to WI)

## Dev
- `go run ./cmd/ingest` — run locally (needs DATABASE_URL)
- `go test ./...` — run tests
- `go vet ./...` — lint

## Architecture
- `cmd/ingest/` — entry point
- `internal/nws/` — NWS API client
- `internal/spc/` — SPC storm reports client
- `internal/store/` — PostGIS storage layer
- `internal/publisher/` — JSON snapshot publisher
- `internal/poller/` — polling orchestrator

## Data Flow
1. Poll NWS alerts API every 30s for active WI alerts
2. Poll SPC CSVs for today's storm reports
3. Deduplicate and upsert to PostGIS
4. Publish active-events.json snapshot (for CDN)

## Companion Repos
- Frontend: `eclecti-build/seestorm`
