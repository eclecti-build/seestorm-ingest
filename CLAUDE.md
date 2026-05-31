# SeeStorm Ingest

Go service that polls NWS/SPC APIs and archives weather events to PostGIS.

## Stack
- **Language:** Go 1.25 (`go.mod` pins 1.25.5)
- **Database driver:** `github.com/jackc/pgx/v5` — schema is applied via an idempotent embedded DDL block (`migrateSQL` in `internal/store/queries.go`) executed in-process at every boot
- **ORM + migrations (scaffolded, not yet authoritative):** [Ent](https://entgo.io) with [Atlas](https://atlasgo.io) — entity schemas in `ent/schema/` and Atlas config (`atlas.hcl`) are present but have no effect at runtime; migrating to Atlas-managed migrations is planned future work (tracked in the project issue tracker)
- **Database:** Neon Postgres + PostGIS
- **Snapshot storage:** Cloudflare R2 (`active-events.json`)
- **Deploy target:** Fly.io, primary region `ord` (Chicago — closest to Wisconsin)

## Dev
- `make run` — run locally (needs `DATABASE_URL`)
- `make test` — `go test -race ./...`
- `make lint` — `golangci-lint run`
- `make fmt` — `gofmt -w . && goimports -w .`
- `make build` — produces `bin/ingest`

See `Makefile` for the full target list.

## Architecture
- `cmd/ingest/` — entry point
- `internal/nws/` — NWS API client
- `internal/spc/` — SPC storm reports client
- `internal/store/` — PostGIS storage layer; schema owned at runtime by `migrateSQL` in `queries.go` (boot-DDL; Ent-generated client not yet in use)
- `internal/publisher/` — JSON snapshot publisher (local + R2)
- `internal/poller/` — polling orchestrator
- `ent/schema/` — Ent entity schema (empty — add `.go` files as entities are introduced)
- `ent/migrate/migrations/` — Atlas-managed migration SQL

## Data Flow
1. Poll NWS alerts API every 30s for active alerts in the configured `NWS_AREA`(s) — accepts a single state code or a comma-separated list (e.g. `WI` or `MN,WI,IL,IN,MI,OH,PA,NY,IA`).
2. Poll SPC CSVs for today's storm reports
3. Deduplicate and upsert to PostGIS
4. Publish `active-events.json` snapshot to local disk and Cloudflare R2

The published snapshot carries a `schema_version` field; the client refuses
to render an unrecognized version. Bump `publisher.SnapshotSchemaVersion`
when the wire shape changes and coordinate the client PR before deploying.

## Fleet & deployment

Ingest runs as **8 Fly apps** sharing one Neon DB and one R2 bucket
(`seestorm-data`), all built from THIS repo (there are no per-region repos):

- `seestorm-ingest` — the **publisher** (`MODE=publish`): reads the shared DB and
  writes the merged + history snapshot to R2 every 30s. Does not poll upstreams.
- `seestorm-ingest-{dixie,gulf,midwest,mountain,northeast,pacific,plains}` —
  region **ingesters** (`MODE=ingest`): each polls NWS for its `NWS_AREA` states
  and upserts the shared DB. They do not publish.

`MODE` (role) and `NWS_AREA` (region) live as **durable Fly secrets** per app —
never in `fly.toml` — so a deploy can't clobber them. **Exactly one app may have
`MODE=publish`**; running more than one publisher multiplies R2 history writes
and collapses the client's history window (the 2026-05 incident).

`.github/workflows/deploy.yml` auto-deploys **only `seestorm-ingest`** (the
publisher) — gated on CI: it runs after the CI workflow succeeds on `main`
(`workflow_run`). Ship the rest with `make deploy-fleet` (pushes the current image
to all 8; role/region come from each app's secrets). `make deploy-fleet-check`
lists the roster and each app's current image.

> ⚠️ **If your change touches the ingesters** — anything under `internal/poller`,
> `internal/store`, `internal/nws`, or `internal/spc` (polling, upsert,
> retire/purge, parsing) — the CI auto-deploy ships it to the **publisher only**.
> It is **NOT live on the 7 region ingesters** until you run `make deploy-fleet`.
> Skipping this leaves the fleet on mixed image versions. Fleet-deploy automation
> is stubbed but not yet wired — see `docs/fleet-deploy-automation.md`.

## Auth
**None.** The ingest service exposes no authenticated endpoints today — its output (snapshot JSON on Cloudflare R2) is public by design. Public safety data stays frictionless.

Future work may require auth for narrow use cases (user-submitted spotter reports, admin-only data corrections, rate-limiting abusive scrapers). Evaluate at the edge (Cloudflare WAF) before adding application-level auth.

## Testing
- Standard Go `*_test.go` files, table-driven
- Always run with the race detector: `go test -race ./...`
- **No mocks for the database.** Integration tests use a real Postgres instance (local Docker, or a throwaway Neon branch). Mock pain on prior projects made the cost/value tradeoff clear — fidelity beats isolation for this service.

## Lint
- `gofmt`, `goimports`, `golangci-lint run`
- Config at `.golangci.yml`
- Generated code (`ent/`) is excluded
- Enabled linters: errcheck, govet, staticcheck, unused, ineffassign, gofmt, goimports, gosimple, revive, gocritic, misspell

## Commits
Conventional Commits with these prefixes:
- `feat:` new user-facing feature
- `fix:` bug fix
- `chore:` tooling, deps, non-code chores
- `docs:` documentation only
- `refactor:` behavior-preserving code change
- `test:` test-only change

**No git hooks, no Husky, no commitlint.** Claude performs commit review. Checklist:
- Conventional prefix present
- Message describes the **why**, not just the what
- No secrets in the diff (API keys, tokens, `.env`)
- Errors are wrapped with context (`fmt.Errorf("...: %w", err)`)
- No `panic()` in library code (panics only in `main` during init failure)

## Schema management — current state

**How the schema is applied today:** `Store.Migrate` in `internal/store/postgres.go` executes the `migrateSQL` constant (`internal/store/queries.go`) inside an explicit transaction at every boot. All DDL uses `IF NOT EXISTS`, making it idempotent. This is the sole mechanism that creates and owns the schema at runtime.

`migrations/001_initial.sql` is a historical record of the hand-written initial schema — it is not executed at runtime and is kept for reference only.

**Ent + Atlas (scaffolded, not yet active):** `ent/schema/` and `atlas.hcl` are present and wired up (see `Makefile` targets `generate`, `migrate-diff`, `migrate-apply`) but no entity schemas are defined yet, and Atlas has not generated any migrations. These artifacts have no effect on a running database. Migrating to Atlas-managed declarative migrations is planned future work (tracked in the project issue tracker).

**To add a schema change today:** edit `migrateSQL` in `internal/store/queries.go`. Use `IF NOT EXISTS` guards so the DDL is idempotent on existing databases.

**When Ent + Atlas adoption lands:**

1. Add an entity schema in `ent/schema/<name>.go` (must implement `ent.Schema`).
2. `make generate` — regenerates the Ent client.
3. `make migrate-diff` — Atlas diffs the Ent schema and writes SQL under `ent/migrate/migrations/`.
4. Review the generated SQL and commit it alongside the schema change.
5. `make migrate-apply` — Atlas applies the migration at deploy time.

Required env for Atlas (future):
- `ATLAS_LOCAL_URL` — local dev Postgres URL (for `migrate-diff`)
- `DATABASE_URL` — prod Postgres URL (for `migrate-apply`)

## Companion Repos
- Umbrella: `eclecti-build/seestorm`
- Frontend: `eclecti-build/seestorm-client`
