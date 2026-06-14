#!/usr/bin/env bash
# Clear an injected fault from a backend API (removes FAULT_MODE → rolling update
# back to healthy).
#
# Usage: heal-api.sh <orders|catalog>
set -euo pipefail

API="${1:-}"
case "$API" in
  orders)  DEPLOY=orders-api;;
  catalog) DEPLOY=catalog-api;;
  *) echo "usage: heal-api.sh <orders|catalog>" >&2; exit 1;;
esac

echo "==> Healing $DEPLOY (removing FAULT_MODE)..."
kubectl -n shop-backend set env "deploy/$DEPLOY" FAULT_MODE- FAULT_LATENCY_MS- 2>/dev/null || true
kubectl -n shop-backend rollout status "deploy/$DEPLOY" --timeout=60s
echo "==> $DEPLOY healthy again."
