#!/usr/bin/env bash
#
# Deploy the SeeStorm ingest fleet — the publisher plus every region ingester —
# from the current checkout, so the apps never drift onto different image
# versions.
#
# Why this exists: .github/workflows/deploy.yml only deploys `seestorm-ingest`
# (the single app named in fly.toml). The region apps are the SAME codebase
# deployed to separate Fly app names, with no CI of their own. Without this
# script a code change ships to the publisher but leaves the regionals on the
# old image — the exact version drift behind the 2026-05 history-amplification
# incident (all nodes publishing -> ~8x history writes -> collapsed client
# history window).
#
# Per-app ROLE and REGION are NOT set here. They live as durable Fly secrets
# (MODE, NWS_AREA) on each app, so this script only pushes the image. Because
# secrets persist across deploys regardless of flags, it is safe to deploy the
# shared fly.toml to every app — a deploy can never clobber a node's region.
#
# Usage:
#   ./scripts/deploy-fleet.sh           Deploy the current image to all apps.
#   ./scripts/deploy-fleet.sh --check   List the roster + each app's current
#                                       image. No deploy.
#
# Requires: flyctl on PATH, authenticated (`fly auth whoami`) with access to
# every app in FLEET.

set -euo pipefail

# The fleet roster. `seestorm-ingest` is the publisher (MODE=publish secret);
# the rest are region ingesters (MODE=ingest + NWS_AREA secrets). When adding or
# removing a region node, update this list AND create/destroy the app with its
# own MODE/NWS_AREA secrets.
FLEET=(
  seestorm-ingest
  seestorm-ingest-dixie
  seestorm-ingest-gulf
  seestorm-ingest-midwest
  seestorm-ingest-mountain
  seestorm-ingest-northeast
  seestorm-ingest-pacific
  seestorm-ingest-plains
)

CONFIG="$(cd "$(dirname "$0")/.." && pwd)/fly.toml"

command -v flyctl >/dev/null 2>&1 || { echo "error: flyctl not found on PATH" >&2; exit 1; }
fly auth whoami >/dev/null 2>&1 || { echo "error: not authenticated to Fly (run: fly auth login)" >&2; exit 1; }

if [[ "${1:-}" == "--check" ]]; then
  printf '%-30s %s\n' "APP" "CURRENT IMAGE"
  for app in "${FLEET[@]}"; do
    img="$(fly image show -a "$app" 2>/dev/null | grep -oE 'deployment-[A-Za-z0-9]+' | head -1)"
    printf '%-30s %s\n' "$app" "${img:-<unknown>}"
  done
  exit 0
fi

echo "Deploying SeeStorm ingest fleet (${#FLEET[@]} apps) from ${CONFIG}"
echo "Role/region come from each app's durable Fly secrets (MODE, NWS_AREA)."
echo

failed=()
for app in "${FLEET[@]}"; do
  echo "==> ${app}"
  if fly deploy -a "$app" -c "$CONFIG" --remote-only -y; then
    echo "    ok: ${app}"
  else
    echo "    FAILED: ${app}" >&2
    failed+=("$app")
  fi
  echo
done

if (( ${#failed[@]} > 0 )); then
  echo "Done with errors. Failed: ${failed[*]}" >&2
  exit 1
fi
echo "Done. All ${#FLEET[@]} apps deployed."
