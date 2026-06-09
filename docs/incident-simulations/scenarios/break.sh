#!/usr/bin/env bash
# Arms scenario 05 by pushing a SECOND, broken revision onto shop-api so a
# rollback has a known-good target to return to.
set -euo pipefail

NS="${NS:-kobi-incident-lab}"
DEPLOY="shop-api"

echo "==> Current revision history for $DEPLOY:"
kubectl -n "$NS" rollout history "deploy/$DEPLOY" || true

echo "==> Pushing broken image (revision 2)..."
kubectl -n "$NS" set image "deploy/$DEPLOY" app=nginx:does-not-exist-2099
kubectl -n "$NS" annotate "deploy/$DEPLOY" \
  kubernetes.io/change-cause="rev2: BROKEN image tag (simulated bad deploy)" --overwrite

echo
echo "==> Done. shop-api now has a healthy rev1 and a broken rev2."
echo "    Ask Kobi to roll it back, or undo manually:"
echo "      kubectl -n $NS rollout undo deploy/$DEPLOY"
