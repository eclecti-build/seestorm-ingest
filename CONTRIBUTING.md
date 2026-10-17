# Contributing

Thanks for helping a non-profit severe-weather project.

- Conventional commits: `feat: fix: chore: docs: refactor: test:`
- Branch names match the type: `feat/… fix/… chore/… docs/…`
- Run `go build ./... && go test ./...` before opening a PR.
- Integration tests need a real PostGIS database (we don't mock the store). They
  read `TEST_DATABASE_URL` and skip themselves when it is unset, so `go test ./...`
  passes without one. CI does not currently set it either, so please run them
  locally for any change under `internal/store`.
  See "Integration test database" below.
- One focused change per PR. Explain *why* in the body.
- MIT-licensed; by contributing you agree your work is MIT.

## Integration test database

Set `TEST_DATABASE_URL` to a Postgres database where the PostGIS extension can be
created. Tests create and drop their own tables; don't point this at a database you care
about.

**Recommended: a Neon branch.** Production runs on [Neon](https://neon.com), and a
[branch](https://neon.com/docs/introduction/branching) is the quickest disposable
PostGIS database you can get:

```bash
# one-time: npm i -g neonctl && neonctl auth
neonctl branches create --name test-$(whoami) --project-id <your-project-id>
export TEST_DATABASE_URL="$(neonctl connection-string test-$(whoami) --project-id <your-project-id>)"
go test -race ./internal/store/...
neonctl branches delete test-$(whoami) --project-id <your-project-id>   # when done
```

Any Neon project works, including a free-tier one of your own; you do not need access to
the SeeStorm project.

**Alternative: local Docker.** Use the `postgis/postgis` image (plain `postgres` images
lack the extension):

```bash
docker run -d --name seestorm-test-pg -p 5433:5432 \
  -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=seestorm_test \
  postgis/postgis:16-3.4
export TEST_DATABASE_URL="postgresql://test:test@localhost:5433/seestorm_test?sslmode=disable"
```

## Deploying

Merges to `main` deploy to Fly.io automatically via GitHub Actions; contributors never
deploy by hand. If you want to run your own copy, follow the **Self-hosting** section of
the README (Fly.io + Neon is the supported, documented path).
