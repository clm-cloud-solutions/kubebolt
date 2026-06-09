#!/usr/bin/env bash
# Removes the entire three-tier demo: the three namespaces (and everything in
# them) plus the external database container.
set -euo pipefail
DB_NAME="${DB_NAME:-shop-db}"

echo "==> Deleting namespaces shop-frontend / shop-backend / shop-data..."
kubectl delete ns shop-frontend shop-backend shop-data --ignore-not-found

echo "==> Removing external DB container '$DB_NAME'..."
docker rm -f "$DB_NAME" >/dev/null 2>&1 || true

echo "==> Done."
