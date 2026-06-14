#!/usr/bin/env bash
# Starts the EXTERNAL database for the three-tier demo: a postgres container
# running OUTSIDE the cluster, attached to the same docker network as the kind
# nodes so in-cluster pods can route to it.
#
# Env overrides:
#   DB_NETWORK   docker network to attach to (default: kind)
#   DB_NAME      container name (default: shop-db)
#   DB_IMAGE     image (default: postgres:16-alpine)
set -euo pipefail

DB_NETWORK="${DB_NETWORK:-kind}"
DB_NAME="${DB_NAME:-shop-db}"
DB_IMAGE="${DB_IMAGE:-postgres:16-alpine}"

if ! docker network inspect "$DB_NETWORK" >/dev/null 2>&1; then
  echo "ERROR: docker network '$DB_NETWORK' not found." >&2
  echo "       For kind it's usually 'kind'. List with: docker network ls" >&2
  echo "       Override with: DB_NETWORK=<name> $0" >&2
  exit 1
fi

if docker inspect "$DB_NAME" >/dev/null 2>&1; then
  echo "==> Container '$DB_NAME' already exists; (re)starting it."
  docker start "$DB_NAME" >/dev/null
else
  echo "==> Starting '$DB_NAME' ($DB_IMAGE) on network '$DB_NETWORK'..."
  docker run -d \
    --name "$DB_NAME" \
    --network "$DB_NETWORK" \
    -e POSTGRES_PASSWORD=demo \
    -e POSTGRES_DB=shop \
    "$DB_IMAGE" >/dev/null
fi

# Give postgres a moment to bind :5432.
sleep 2
IP="$(docker inspect -f "{{.NetworkSettings.Networks.${DB_NETWORK}.IPAddress}}" "$DB_NAME")"

echo
echo "==> Database '$DB_NAME' is up at ${IP}:5432 on network '$DB_NETWORK'."
echo "    Next: run ./apply.sh (from the three-tier-shop dir) to wire the cluster to it."
