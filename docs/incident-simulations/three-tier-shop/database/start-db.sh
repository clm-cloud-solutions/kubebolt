#!/usr/bin/env bash
# Recovers from the cascade: restarts the external database container. The
# backend readiness probes go green again, endpoints repopulate, and the
# frontend recovers — all without any pod restarts (liveness never tripped).
set -euo pipefail
DB_NAME="${DB_NAME:-shop-db}"
echo "==> Starting '$DB_NAME' to recover..."
docker start "$DB_NAME" >/dev/null
echo "==> Started. Backend should go Ready within ~10s; frontend follows."
