#!/usr/bin/env bash
# Triggers the cascade: stops the external database container. Within a few
# seconds the backend readiness probes fail, the backend pods leave their
# Service endpoints, and the failure rolls up to the frontend.
#
# Watch it with:
#   kubectl get pods -n shop-backend -n shop-frontend -w   # readiness flips
#   kubectl logs -n shop-frontend deploy/loadgen -f        # 200 -> 503
set -euo pipefail
DB_NAME="${DB_NAME:-shop-db}"
echo "==> Stopping '$DB_NAME' to trigger the cascade..."
docker stop "$DB_NAME" >/dev/null
echo "==> Stopped. Backend should go NotReady within ~10s; frontend follows."
