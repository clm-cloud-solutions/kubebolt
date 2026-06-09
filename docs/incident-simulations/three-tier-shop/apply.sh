#!/usr/bin/env bash
# Deploys the whole three-tier KubeShop demo and wires the data tier to the
# running external database container.
#
# Order: namespaces → data tier (Service + Endpoints pinned to the DB IP) →
# backend → frontend → loadgen.
#
# Prereqs: the external DB must already be running — run database/run-db.sh first.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DB_NETWORK="${DB_NETWORK:-kind}"
DB_NAME="${DB_NAME:-shop-db}"

if ! docker inspect "$DB_NAME" >/dev/null 2>&1; then
  echo "ERROR: DB container '$DB_NAME' not found. Run database/run-db.sh first." >&2
  exit 1
fi

SHOP_DB_IP="$(docker inspect -f "{{.NetworkSettings.Networks.${DB_NETWORK}.IPAddress}}" "$DB_NAME")"
if [ -z "$SHOP_DB_IP" ]; then
  echo "ERROR: could not read IP of '$DB_NAME' on network '$DB_NETWORK'." >&2
  exit 1
fi
echo "==> External DB '$DB_NAME' at ${SHOP_DB_IP}:5432"

echo "==> Applying namespaces..."
kubectl apply -f "$HERE/00-namespaces.yaml"

echo "==> Wiring data tier Service/Endpoints to ${SHOP_DB_IP}..."
sed "s|\${SHOP_DB_IP}|${SHOP_DB_IP}|g" "$HERE/10-data-tier.yaml" | kubectl apply -f -

echo "==> Applying backend tier..."
kubectl apply -f "$HERE/20-backend-tier.yaml"

echo "==> Applying frontend tier..."
kubectl apply -f "$HERE/30-frontend-tier.yaml"

echo "==> Applying load generator..."
kubectl apply -f "$HERE/40-loadgen.yaml"

echo
echo "==> Done. Watch it come up:"
echo "      kubectl get pods -n shop-backend -w"
echo "    Trigger the cascade:   database/stop-db.sh"
echo "    Recover:               database/start-db.sh"
echo "    (Optional) isolation:  kubectl apply -f $HERE/50-networkpolicies.yaml"
