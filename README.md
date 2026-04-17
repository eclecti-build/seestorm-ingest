# seestorm-ingest

Go service that polls NWS and SPC APIs for weather data, deduplicates events, stores them in PostGIS, and publishes CDN-cacheable JSON snapshots.

## Quick Start

```bash
# Set required env vars
export DATABASE_URL="postgresql://user:password@localhost:5432/seestorm?sslmode=require"

# Run
go run ./cmd/ingest
```

## Requirements

- Go 1.25+

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | (required) | PostgreSQL connection string |
| `SNAPSHOT_DIR` | `./snapshots` | Directory for JSON snapshot output |
| `POLL_INTERVAL` | `30s` | Polling interval (Go duration format) |

## Architecture

- **NWS Client** — Fetches active weather alerts via `api.weather.gov`
- **SPC Client** — Fetches today's storm reports (tornado, hail, wind) from SPC CSVs
- **PostGIS Store** — Upserts events with spatial indexes for efficient geo queries
- **Publisher** — Writes `active-events.json` snapshot after each poll cycle
- **Poller** — Orchestrates the fetch-store-publish loop on a configurable interval
