#!/usr/bin/env bash
# Inject a fault into ONE backend API, without touching the database or the other
# API — so you can demo "a single microservice is failing" as opposed to the whole
# data tier going down (database/stop-db.sh).
#
# Sets FAULT_MODE on the deployment via `kubectl set env` (rolling update, ~10s,
# applies to all replicas deterministically).
#
# Usage: fault-api.sh <orders|catalog> <error|unready|slow>
#   error   — pod stays READY, but GET / returns 500 (buggy API / app-level 5xx).
#             The tricky case: readiness is green and endpoints are present, yet
#             the API is broken — you must read logs/responses, not just readiness.
#   unready — readiness probe fails even though the DB is fine → the pod leaves its
#             Service endpoints. Distinct from a DB outage: the OTHER API stays
#             Ready, which proves the database itself is healthy.
#   slow    — GET / sleeps FAULT_LATENCY_MS (default 3000ms) before responding →
#             the frontend's 2s timeout trips and treats it as unreachable.
#
# Heal with: heal-api.sh <orders|catalog>
set -euo pipefail

API="${1:-}"
MODE="${2:-}"
if [ -z "$API" ] || [ -z "$MODE" ]; then
  echo "usage: fault-api.sh <orders|catalog> <error|unready|slow>" >&2
  exit 1
fi
case "$API" in
  orders)  DEPLOY=orders-api;;
  catalog) DEPLOY=catalog-api;;
  *) echo "first arg must be 'orders' or 'catalog'" >&2; exit 1;;
esac
case "$MODE" in
  error|unready|slow) ;;
  *) echo "second arg must be 'error', 'unready' or 'slow'" >&2; exit 1;;
esac

echo "==> Injecting FAULT_MODE=$MODE into $DEPLOY (shop-backend)..."
kubectl -n shop-backend set env "deploy/$DEPLOY" FAULT_MODE="$MODE"
kubectl -n shop-backend rollout status "deploy/$DEPLOY" --timeout=60s
echo
echo "==> $DEPLOY now running with FAULT_MODE=$MODE."
echo "    Watch it:   kubectl logs -n shop-backend -l app=$DEPLOY --prefix -f"
echo "    Heal it:    database/heal-api.sh $API"
