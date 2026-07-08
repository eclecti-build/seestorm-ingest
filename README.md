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
| `NWS_AREA` | `WI` | USPS state code(s) to poll. Accepts a single value (`WI`) or comma-separated list (`MN,WI,IL,IN,MI,OH,PA,NY,IA`). One HTTP request regardless of list length. |
| `MODE` | `both` (fatal at boot if unset AND `FLY_APP_NAME` is set) | Fleet role: `both` (local dev/single-node), `ingest` (region node, no publish), or `publish` (single snapshot publisher, no polling). Required explicitly on Fly — see CLAUDE.md "Fleet & deployment". |

## Architecture

- **NWS Client** — Fetches active weather alerts via `api.weather.gov`
- **SPC Client** — Fetches today's storm reports (tornado, hail, wind) from SPC CSVs
- **PostGIS Store** — Upserts events with spatial indexes for efficient geo queries
- **Publisher** — Writes `active-events.json` snapshot after each poll cycle
- **Poller** — Orchestrates the fetch-store-publish loop on a configurable interval
