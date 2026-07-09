# Fleet deploy automation (live)

Status: **live** as of 2026-07-09. `.github/workflows/deploy-fleet.yml`
auto-deploys the 7 regional ingesters on every successful CI run on `main`,
with a post-deploy drift check scoped to those 7. The drift check compares
image content digests via `fly image show --json`; per-app deploys mint distinct
`deployment-*` tags for identical content, so tags cannot be compared.
`seestorm-ingest` (the publisher) is deliberately excluded from this workflow's
matrix — `deploy.yml` remains its sole auto-deployer, avoiding a race between
the two workflows over the same Fly app (both trigger on the same CI-completion
event). `make deploy-fleet` remains as a manual fallback for all 8 apps (hotfix
between merges, or if a workflow needs debugging) — do not run it while a
CI-triggered deploy is still in flight.

## Current state

`.github/workflows/deploy.yml` deploys **only the publisher**
(`seestorm-ingest`) — `flyctl deploy --remote-only` against the single app in
`fly.toml`, gated on CI (`workflow_run` after CI succeeds on `main`).

`.github/workflows/deploy-fleet.yml` deploys the **7 region ingesters**
(`seestorm-ingest-{dixie,gulf,midwest,mountain,northeast,pacific,plains}`) from
the same CI-completion event. Its deploy matrix has `fail-fast: false` so one
region's failure is isolated and individually visible in the checks UI. After
the matrix completes, `verify-fleet-converged` checks that all 7 regionals are
running the same image content digest.

Manual fallback remains available:

```sh
make deploy-fleet        # deploy all 8 apps from the current checkout (~3 min)
make deploy-fleet-check  # list each app's current deployment image tag
```

Do not run `make deploy-fleet` while either CI-triggered deploy workflow is in
flight. The manual script also deploys `seestorm-ingest`, so it can race the
publisher auto-deploy if run immediately after a merge to `main`.

## Why the publisher is separate

`deploy.yml` and `deploy-fleet.yml` both trigger on the same CI-completion event
on `main`. Their GitHub Actions `concurrency:` groups are different, so including
`seestorm-ingest` in the fleet matrix would allow two independent workflow runs
to deploy the same Fly app concurrently. The fix is exclusion: `deploy.yml`
remains the publisher's one and only auto-deployer, and `deploy-fleet.yml`
deploys only the 7 regionals.

Role/region come from each app's durable Fly secrets (`MODE`, `NWS_AREA`), so
deploying the shared `fly.toml` to every app cannot clobber a node's config.

## Drift check

The convergence check intentionally compares content digests, not deployment
tags. Each per-app `flyctl deploy --remote-only` can mint a distinct
`deployment-*` tag even when the built image content is identical. `fly image
show -a <app> --json` returns the deployed image metadata per machine; the
workflow extracts every `.Digest` with `jq`, prints `<app> <digest>` for the log,
and fails if the 7 regional apps have more than one unique digest.

The publisher is excluded from the drift check for the same reason it is
excluded from the deploy matrix: it is owned by `deploy.yml` and may legitimately
be mid-deploy or briefly on a different image at the instant the regional
workflow verifies convergence.

## Operational notes

The `FLY_API_TOKEN` GitHub Actions secret must be able to deploy all 7 regional
apps. If the fleet workflow fails with 401/403 across the matrix, rotate the
secret to an org-scoped Fly token with deploy access to the fleet.

Optional later optimization: build the image once and deploy by image ref instead
of letting each matrix job rebuild. Depot cache makes the rebuild cheap, so this
is not urgent.
