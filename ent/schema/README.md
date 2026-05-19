# Ent Schema

Place Ent entity schema Go files in this directory. One file per entity, named
`<entity>.go` (lowercase), where each file defines a struct embedding
`ent.Schema` and implementing the required methods (`Fields()`, `Edges()`,
optionally `Indexes()`, `Annotations()`).

Reference: https://entgo.io/docs/schema-def

## Workflow

1. Add or edit a schema file here.
2. From the repo root, run `make generate` — this invokes
   `go generate ./ent/...`, which regenerates the Ent client into `ent/`.
3. Run `make migrate-diff` — Atlas diffs the Ent schema against the dev
   database and writes a new SQL migration under `ent/migrate/migrations/`.
4. Review the generated SQL and commit it alongside the schema change.
5. `make migrate-apply` runs at deploy time against `DATABASE_URL`.

## Prerequisites for `make migrate-diff`

Atlas spins up an ephemeral Postgres container to compute the schema diff.
You must have **Docker** running locally (see `dev = "docker://postgres/16/dev"`
in `atlas.hcl`). If Docker isn't available, either install it, swap the Atlas
`dev` URL to a persistent local Postgres, or run migrations only in CI/deploy.

Additionally set `ATLAS_LOCAL_URL` in your `.env` before running
`make migrate-diff` — see `.env.example` for the format.

## Current state

This directory is intentionally empty — no entities are defined yet, and the
Ent + Atlas workflow is not yet active at runtime. The service currently
applies its schema via an idempotent embedded DDL block (`migrateSQL` in
`internal/store/queries.go`) executed in-process at every boot. Migrating to
Atlas-managed declarative migrations is planned future work (DEF-014 in the
umbrella repo).

Running `make generate` against an empty schema directory will no-op (or emit
a benign warning) until the first entity is added.
