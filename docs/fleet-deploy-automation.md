# Fleet deploy automation (planned)

Status: **stubbed, not wired.** Stub workflow: `.github/workflows/deploy-fleet.yml`
(manual-only, deploys nothing). Deploy the fleet manually with `make deploy-fleet`
until this is built.

## Current state

`.github/workflows/deploy.yml` deploys **only the publisher** (`seestorm-ingest`) —
`flyctl deploy --remote-only` against the single app in `fly.toml`, gated on CI
(`workflow_run` after CI succeeds on `main`). The **7 region ingesters**
(`seestorm-ingest-{dixie,gulf,midwest,mountain,northeast,pacific,plains}`) have
**no CI deploy of their own**; they ship manually:

```sh
make deploy-fleet        # deploy all 8 apps from the current checkout (~3 min)
make deploy-fleet-check  # list each app's current deployment image id
```

A change that only affects the ingesters (`internal/poller`, `internal/store`,
`internal/nws`, `internal/spc`) is **not live on the regionals** until someone runs
`make deploy-fleet`. (This bit us on PR2: the publisher auto-deployed but retirement
runs on the ingesters, so the fix wasn't live until the manual fleet deploy.)

## Why it isn't automated yet

The workflow is ~15 lines. The real work is two prerequisites:

1. **Token scope.** The `FLY_API_TOKEN` GitHub secret currently deploys the
   publisher. The fleet deploy needs it to reach **all 8 apps** — i.e. an
   **org-scoped** Fly deploy token, not one scoped to `seestorm-ingest` alone.
   Confirm or mint before enabling, or the regionals fail with 401.
2. **Auth precheck.** `scripts/deploy-fleet.sh` bails on `fly auth whoami`, which
   some Fly *deploy tokens* don't support. A CI matrix that calls `fly deploy`
   directly sidesteps the precheck (so prefer the matrix below over shelling out
   to the script in CI).

## Planned approach (when prioritized)

A matrix over the 8 apps, gated on CI exactly like `deploy.yml`, with
`fail-fast: false` so one region's failure is isolated and individually visible in
the checks UI:

```yaml
on:
  workflow_run:
    workflows: ["CI"]
    types: [completed]
    branches: [main]
jobs:
  deploy:
    if: ${{ github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.head_branch == 'main' }}
    runs-on: ubuntu-latest
    strategy:
      fail-fast: false
      matrix:
        app:
          - seestorm-ingest
          - seestorm-ingest-dixie
          - seestorm-ingest-gulf
          - seestorm-ingest-midwest
          - seestorm-ingest-mountain
          - seestorm-ingest-northeast
          - seestorm-ingest-pacific
          - seestorm-ingest-plains
    steps:
      - uses: actions/checkout@v6
        with:
          ref: ${{ github.event.workflow_run.head_sha }}
      - uses: superfly/flyctl-actions/setup-flyctl@ed8efb33836e8b2096c7fd3ba1c8afe303ebbff1
      - run: flyctl deploy -a ${{ matrix.app }} --remote-only
        env:
          FLY_API_TOKEN: ${{ secrets.FLY_API_TOKEN }}
```

When this lands it supersedes the publisher-only `deploy.yml` (delete that, or keep
it only if you want the publisher to ship faster than the regionals). Role/region
come from each app's durable Fly secrets (`MODE`, `NWS_AREA`), so deploying the
shared `fly.toml` to every app can't clobber a node's config.

Optional later optimization: build the image once and deploy by image ref instead
of letting each matrix job rebuild (depot cache makes the rebuild cheap, so this is
not urgent).

## Estimated lift

~30–45 min: ~15 for the workflow, the rest to confirm the token scope and do one
validated run.
