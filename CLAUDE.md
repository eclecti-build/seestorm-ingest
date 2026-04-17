# SeeStorm Ingest

Go service that polls NWS/SPC APIs and archives weather events to PostGIS.

## Stack
- **Language:** Go 1.25 (`go.mod` pins 1.25.5)
- **Database driver:** `github.com/jackc/pgx/v5`
- **ORM + migrations:** [Ent](https://entgo.io) with [Atlas](https://atlasgo.io) for declarative migrations
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
- `internal/store/` — PostGIS storage layer (pgx/v5 today; Ent-generated client landing alongside)
- `internal/publisher/` — JSON snapshot publisher (local + R2)
- `internal/poller/` — polling orchestrator
- `internal/auth/` — Clerk JWT verification (stub, not yet wired)
- `ent/schema/` — Ent entity schema (empty — add `.go` files as entities are introduced)
- `ent/migrate/migrations/` — Atlas-managed migration SQL

## Data Flow
1. Poll NWS alerts API every 30s for active WI alerts
2. Poll SPC CSVs for today's storm reports
3. Deduplicate and upsert to PostGIS
4. Publish `active-events.json` snapshot to local disk and Cloudflare R2

## Auth
Clerk JWT verification via `github.com/clerk/clerk-sdk-go/v2`.
Stub lives at `internal/auth/clerk.go` and returns `ErrNotImplemented`. It is **not** wired into any handler yet — enable once `/api` routes require auth. Set `CLERK_SECRET_KEY` in the environment when wiring.

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

## Migration workflow

1. Edit or add an entity schema in `ent/schema/<name>.go` (must implement `ent.Schema`).
2. `make generate` — runs `go generate ./ent/...`, regenerating the Ent client.
3. `make migrate-diff` — Atlas diffs the Ent schema against the dev database and writes new SQL under `ent/migrate/migrations/`.
4. **Review the generated SQL** and commit it alongside the schema change.
5. `make migrate-apply` — Atlas applies the migration at deploy time (run against `DATABASE_URL` / prod env).

The legacy `migrations/001_initial.sql` stays as a historical record of the hand-written initial schema. All future changes flow through Ent -> Atlas.

Required env for Atlas:
- `ATLAS_LOCAL_URL` — local dev Postgres URL (for `migrate-diff`)
- `DATABASE_URL` — prod Postgres URL (for `migrate-apply`)

## Companion Repos
- Frontend: `eclecti-build/seestorm`
