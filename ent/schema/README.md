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

## Current state

This directory is intentionally empty — no entities are defined yet. Running
`make generate` against an empty schema directory will no-op (or emit a
benign error) until the first entity is added.
