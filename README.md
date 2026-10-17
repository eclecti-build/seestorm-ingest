# seestorm-ingest

Go service that polls NWS and SPC APIs for weather data, deduplicates events, stores them in PostGIS, and publishes CDN-cacheable JSON snapshots.

## Quick Start

```bash
# 1. Point at a Postgres database that has (or can create) the PostGIS extension
export DATABASE_URL="postgresql://user:password@host/seestorm?sslmode=require"

# 2. Run — schema migrations are applied automatically at boot
go run ./cmd/ingest
```

With no `R2_*` variables set, snapshots are written to `SNAPSHOT_DIR` on local disk and
nothing is uploaded, so a database is the only external dependency for local development.

## Requirements

- Go 1.25+
- PostgreSQL 16+ with the [PostGIS](https://postgis.net/) extension available. The first
  migration runs `CREATE EXTENSION IF NOT EXISTS postgis`, so the connecting role must be
  allowed to create extensions (or PostGIS must already be installed in the database).

### Database: Neon (recommended)

SeeStorm's production database is [Neon](https://neon.com) serverless Postgres, and we
recommend it for self-hosters and contributors too. It ships PostGIS, has a free tier that
comfortably covers a single-state ingest, and its
[branching](https://neon.com/docs/introduction/branching) gives you throwaway copies of the
schema for tests and experiments.

1. Create a project at [console.neon.tech](https://console.neon.tech) (any region; we use
   AWS `us-east-2`, close to our Fly region).
2. Copy the connection string from the project dashboard and export it as
   `DATABASE_URL`. Keep `sslmode=require`.
3. Run `go run ./cmd/ingest`. PostGIS is enabled and the schema is created on first boot.

See [Neon's PostGIS guide](https://neon.com/docs/extensions/postgis) for extension details.

### Database: local Docker (alternative)

If you prefer a fully local setup, the official PostGIS image works:

```bash
docker run -d --name seestorm-pg -p 5432:5432 \
  -e POSTGRES_USER=seestorm -e POSTGRES_PASSWORD=seestorm -e POSTGRES_DB=seestorm \
  postgis/postgis:16-3.4
export DATABASE_URL="postgresql://seestorm:seestorm@localhost:5432/seestorm?sslmode=disable"
```

Plain `postgres` images will **not** work; the PostGIS extension must be installable.

## Configuration

| Variable | Default | Description |
|---|---|---|
| `DATABASE_URL` | (required) | PostgreSQL connection string. Must point at a database where PostGIS is installed or installable. We recommend [Neon](https://neon.com) (see Quick Start). |
| `SNAPSHOT_DIR` | `./snapshots` | Directory for JSON snapshot output |
| `POLL_INTERVAL` | `30s` | Polling interval (Go duration format) |
| `NWS_AREA` | `WI` | USPS state code(s) to poll. Accepts a single value (`WI`) or comma-separated list (`MN,WI,IL,IN,MI,OH,PA,NY,IA`). One HTTP request regardless of list length. |
| `MODE` | `both` (fatal at boot if unset AND `FLY_APP_NAME` is set) | Fleet role: `both` (local dev/single-node), `ingest` (region node, no publish), or `publish` (single snapshot publisher, no polling). Required explicitly on Fly — see CLAUDE.md "Fleet & deployment". |
| `R2_ACCOUNT_ID`, `R2_ACCESS_KEY_ID`, `R2_SECRET_ACCESS_KEY`, `R2_BUCKET_NAME` | (unset) | Optional. When all four are set, snapshots are also uploaded to the Cloudflare R2 bucket the client reads from. When unset, snapshots go to `SNAPSHOT_DIR` only. |
| `NWS_USER_AGENT` | (built-in) | `User-Agent` sent to `api.weather.gov`. NWS asks for a contact address; set your own when self-hosting. |

## Architecture

- **NWS Client** — Fetches active weather alerts via `api.weather.gov`
- **SPC Client** — Fetches today's storm reports (tornado, hail, wind) from SPC CSVs
- **PostGIS Store** — Upserts events with spatial indexes for efficient geo queries
- **Publisher** — Writes `active-events.json` snapshot after each poll cycle
- **Poller** — Orchestrates the fetch-store-publish loop on a configurable interval

## Self-hosting

The service is a single static Go binary (see `Dockerfile`) that needs one Postgres
database and outbound HTTPS. The only required secret is `DATABASE_URL`. Run it with
`MODE=both` for a single-node deployment; the multi-app fleet split (`ingest` / `publish`)
is only needed when polling many states from several regions.

### Hosting: Fly.io (recommended)

Production runs on [Fly.io](https://fly.io) and the repo ships a ready-to-use `fly.toml`
(health check on `/healthz`, a persistent volume for `SNAPSHOT_DIR`). To stand up your own
instance:

```bash
# Install flyctl: https://fly.io/docs/flyctl/install/
fly launch --no-deploy            # creates the app from fly.toml; pick your own app name/region
fly volumes create seestorm_data --size 1
fly secrets set DATABASE_URL="postgresql://...neon.tech/seestorm?sslmode=require" MODE=both
fly deploy
```

Pair it with a Neon database in the nearest Neon region to your Fly region to keep
round-trip latency low (we run Fly `ord` against Neon `aws-us-east-2`). Fly docs:
[deploying an app](https://fly.io/docs/launch/deploy/),
[secrets](https://fly.io/docs/apps/secrets/),
[volumes](https://fly.io/docs/volumes/).

### Hosting: anywhere else

Any host that can run the Docker image (`make docker-build`) with `DATABASE_URL` set
works: a VPS, Kubernetes, Cloud Run, etc. Expose port `8080` if you want the `/healthz`
check. Migrations run at boot and are serialized with a Postgres advisory lock, so
running several replicas against one database is safe.
