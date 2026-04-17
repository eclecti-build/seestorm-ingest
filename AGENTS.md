# AGENTS.md

Agent-facing summary of SeeStorm Ingest conventions. For the full context, read `CLAUDE.md`.

## Stack (quick)
- Go 1.25, `jackc/pgx/v5`, Ent + Atlas migrations
- Neon Postgres + PostGIS, Cloudflare R2 snapshots
- Fly.io region `ord`

## Layout
- `cmd/ingest/` entry point
- `internal/{nws,spc,store,publisher,poller,auth}/`
- `ent/schema/` Ent entities (add one file per entity)
- `ent/migrate/migrations/` Atlas SQL
- `migrations/001_initial.sql` legacy baseline — do not edit

## Common tasks
- Build: `make build`
- Run: `make run`
- Test: `make test` (= `go test -race ./...`)
- Lint: `make lint` (golangci-lint)
- Format: `make fmt`
- Regenerate Ent client: `make generate`
- New migration: edit schema -> `make generate` -> `make migrate-diff` -> review SQL -> commit
- Apply migrations: `make migrate-apply`

## Rules
- No `any` in Go type assertions; wrap errors with `%w`
- No panics in library code; only during `main` startup
- No DB mocks — integration tests hit real Postgres
- Table-driven tests, always `-race`
- Conventional commit prefixes: `feat: fix: chore: docs: refactor: test:`
- Commit messages explain **why**
- No secrets in commits; check diffs before pushing
- Generated code in `ent/` is excluded from lint

## Auth
Clerk JWT verifier stub at `internal/auth/clerk.go`. Returns `ErrNotImplemented`. Do not wire into handlers without an accompanying architectural decision.

## Deploy
Fly.io via `make fly-deploy` (wraps `flyctl deploy --remote-only`). CI deploy on push to `main` uses `FLY_API_TOKEN`.
