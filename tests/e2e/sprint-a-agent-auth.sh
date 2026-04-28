#!/usr/bin/env bash
# Sprint A end-to-end agent auth test against a kind cluster.
#
# Validates the full loop committed across Sprint A:
#   1. Backend (helm install) starts with auth.enabled + agentIngest.authMode=enforced
#   2. Admin login → POST /admin/tenants/<default>/tokens → plaintext token
#   3. K8s Secret created with the token
#   4. Agent (helm install) mounts the Secret + dials the gRPC service
#   5. Backend log shows "agent registered" with tenant_id matching the default tenant
#   6. (Optional) Token revoke → next agent RPC fails Unauthenticated
#
# Usage:
#   ./sprint-a-agent-auth.sh setup          create kind + build + helm install
#   ./sprint-a-agent-auth.sh test           run assertions (requires setup first)
#   ./sprint-a-agent-auth.sh cleanup        helm uninstall + delete cluster
#   ./sprint-a-agent-auth.sh all            setup + test + cleanup
#
# Environment overrides:
#   CLUSTER          kind cluster name           (default: kubebolt-sprint-a)
#   NAMESPACE        kubernetes namespace        (default: kubebolt-system)
#   KEEP             "1" → skip cleanup at end   (default: empty)
#   API_PORT         host port for kubectl pf    (default: 18080)

set -euo pipefail

CLUSTER="${CLUSTER:-kubebolt-sprint-a}"
NAMESPACE="${NAMESPACE:-kubebolt-system}"
RELEASE="kubebolt"
AGENT_RELEASE="kubebolt-agent"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
API_PORT="${API_PORT:-18080}"
ADMIN_PASSWORD="admin123"
TOKEN_LABEL="e2e-test"
PF_PID_FILE="/tmp/kubebolt-e2e-pf.pid"

# ─── helpers ──────────────────────────────────────────────────────────

log()  { printf '\n\033[1;36m▶\033[0m %s\n' "$*"; }
ok()   { printf '  \033[1;32m✓\033[0m %s\n' "$*"; }
fail() { printf '  \033[1;31m✗\033[0m %s\n' "$*"; exit 1; }
info() { printf '    %s\n' "$*"; }

require_tool() {
  command -v "$1" >/dev/null 2>&1 || fail "missing tool: $1"
}

# ─── cmd_setup ────────────────────────────────────────────────────────

cmd_setup() {
  for tool in kind kubectl helm docker jq curl; do require_tool "$tool"; done

  log "Ensuring kind cluster '${CLUSTER}'"
  if kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
    info "cluster exists, reusing"
  else
    kind create cluster --name "${CLUSTER}"
  fi
  kubectl cluster-info --context "kind-${CLUSTER}" >/dev/null

  log "Building API image (kubebolt-api:e2e)"
  # Uses the e2e-specific Dockerfile that builds with root context so
  # the apps/api → packages/proto replace directive resolves. The
  # shipped apps/api/Dockerfile is context=apps/api and breaks here.
  docker build --quiet -t kubebolt-api:e2e -f "${ROOT}/tests/e2e/Dockerfile.api-e2e" "${ROOT}"
  ok "image built"

  log "Building agent image (kubebolt-agent:e2e)"
  docker build --quiet -t kubebolt-agent:e2e -f "${ROOT}/packages/agent/Dockerfile" "${ROOT}"
  ok "image built"

  log "Loading images into kind"
  kind load docker-image kubebolt-api:e2e --name "${CLUSTER}"
  kind load docker-image kubebolt-agent:e2e --name "${CLUSTER}"
  ok "images loaded"

  log "Helm install backend with auth.enabled + agentIngest.authMode=enforced"
  kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1 || kubectl create namespace "${NAMESPACE}"
  helm upgrade --install "${RELEASE}" "${ROOT}/deploy/helm/kubebolt" \
    --namespace "${NAMESPACE}" \
    --set api.image.repository=kubebolt-api \
    --set api.image.tag=e2e \
    --set api.image.pullPolicy=Never \
    --set web.image.repository=nginx \
    --set web.image.tag=alpine \
    --set web.image.pullPolicy=IfNotPresent \
    --set auth.enabled=true \
    --set auth.adminPassword="${ADMIN_PASSWORD}" \
    --set auth.persistence.enabled=false \
    --set agentIngest.authMode=enforced \
    --set ingress.enabled=false \
    --wait --timeout=3m
  ok "backend ready"

  log "Issuing ingest token via admin REST"
  start_port_forward
  local jwt token tenant_id
  jwt=$(curl -sf -X POST "http://127.0.0.1:${API_PORT}/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | jq -r .accessToken)
  [[ -n "${jwt}" && "${jwt}" != "null" ]] || fail "login failed (empty JWT)"
  ok "admin login"

  tenant_id=$(curl -sf -H "Authorization: Bearer ${jwt}" \
    "http://127.0.0.1:${API_PORT}/api/v1/admin/tenants" \
    | jq -r '.[] | select(.name=="default") | .id')
  [[ -n "${tenant_id}" ]] || fail "default tenant not found"
  echo "${tenant_id}" > /tmp/kubebolt-e2e-tenant-id
  ok "default tenant: ${tenant_id}"

  local issued
  issued=$(curl -sf -X POST -H "Authorization: Bearer ${jwt}" \
    -H 'Content-Type: application/json' \
    -d "{\"label\":\"${TOKEN_LABEL}\"}" \
    "http://127.0.0.1:${API_PORT}/api/v1/admin/tenants/${tenant_id}/tokens")
  token=$(echo "${issued}" | jq -r .token)
  echo "${issued}" | jq -r .info.id > /tmp/kubebolt-e2e-token-id
  [[ -n "${token}" && "${token}" != "null" ]] || fail "token issue failed"
  ok "issued token (prefix $(echo "${token}" | cut -c1-12)...)"

  log "Creating K8s Secret with the token"
  kubectl create secret generic kubebolt-ingest-token \
    --namespace "${NAMESPACE}" \
    --from-literal=token="${token}" \
    --dry-run=client -o yaml | kubectl apply -f -
  ok "secret created"

  log "Helm install agent with auth.mode=ingest-token"
  helm upgrade --install "${AGENT_RELEASE}" "${ROOT}/deploy/helm/kubebolt-agent" \
    --namespace "${NAMESPACE}" \
    --set image.repository=kubebolt-agent \
    --set image.tag=e2e \
    --set image.pullPolicy=Never \
    --set backendUrl="${RELEASE}-agent-ingest:9090" \
    --set auth.mode=ingest-token \
    --set auth.ingestToken.existingSecret=kubebolt-ingest-token \
    --wait --timeout=2m
  ok "agent installed"
}

start_port_forward() {
  stop_port_forward
  kubectl port-forward -n "${NAMESPACE}" "svc/${RELEASE}-api" "${API_PORT}":8080 >/dev/null 2>&1 &
  echo $! > "${PF_PID_FILE}"
  for _ in {1..30}; do
    curl -sf "http://127.0.0.1:${API_PORT}/health" >/dev/null 2>&1 && return 0
    sleep 1
  done
  fail "port-forward did not become ready"
}

stop_port_forward() {
  [[ -f "${PF_PID_FILE}" ]] || return 0
  kill "$(cat "${PF_PID_FILE}")" 2>/dev/null || true
  rm -f "${PF_PID_FILE}"
}

# ─── cmd_test ─────────────────────────────────────────────────────────

cmd_test() {
  log "Asserting agent connected and authenticated"

  local tenant_id
  tenant_id=$(cat /tmp/kubebolt-e2e-tenant-id 2>/dev/null) || fail "run setup first"

  # Two signals confirm the auth path is wired end-to-end:
  #   1. "agent registered" line stamped with our tenant_id (initial dial)
  #   2. "received metric batch" stamped with our tenant_id (streaming)
  # Either is sufficient. set +e around the grep because it returns 1
  # when nothing matches, which under set -e would abort the loop.
  local matched=0
  for i in {1..60}; do
    set +e
    local hits
    hits=$(kubectl logs -n "${NAMESPACE}" -l app.kubernetes.io/component=api -c api --tail=500 2>/dev/null \
      | grep -E "(agent registered|received metric batch).*tenant_id=${tenant_id}" \
      | wc -l | tr -d ' ')
    set -e
    if [[ "${hits}" -gt 0 ]]; then
      ok "backend authenticated agent against tenant_id=${tenant_id} (${hits} matching log lines)"
      kubectl logs -n "${NAMESPACE}" -l app.kubernetes.io/component=api -c api --tail=500 \
        | grep -E "(agent registered|received metric batch).*tenant_id=${tenant_id}" \
        | tail -1 | sed 's/^/    /'
      matched=1
      break
    fi
    sleep 1
  done
  [[ ${matched} -eq 1 ]] || fail "agent never authenticated after 60s"

  # The agent-side ack ("registered agent_id=...") is implied by a
  # successful backend registration in the assertion above — agents
  # that fail to read the ack disconnect immediately and never start
  # streaming. Skipping a redundant assertion here keeps the test
  # robust against agent log rotation in long-running clusters.

  # The auth interceptor runs once per gRPC stream open, so revoking a
  # token does NOT terminate an active StreamMetrics — that is a Sprint
  # A limitation tracked for Sprint A.5 (server-push reconnect signal).
  # To assert the revoke actually invalidated state, we delete the agent
  # pod: the DaemonSet recreates it, the new pod re-dials with the
  # (now revoked) token, and the interceptor rejects on Register.
  log "Revoking the token to validate cache invalidation"
  start_port_forward
  local jwt token_id
  jwt=$(curl -sf -X POST "http://127.0.0.1:${API_PORT}/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"${ADMIN_PASSWORD}\"}" | jq -r .accessToken)
  token_id=$(cat /tmp/kubebolt-e2e-token-id)
  curl -sf -X DELETE -H "Authorization: Bearer ${jwt}" \
    "http://127.0.0.1:${API_PORT}/api/v1/admin/tenants/${tenant_id}/tokens/${token_id}" >/dev/null
  ok "token revoked"

  # Force a fresh dial by deleting the agent pod. DaemonSet recreates
  # it within seconds; the new pod re-dials with the (now revoked) token.
  log "Forcing agent reconnect"
  kubectl delete pod -n "${NAMESPACE}" -l app.kubernetes.io/name=kubebolt-agent --wait=false >/dev/null
  ok "agent pod deleted, daemonset will recreate"

  # The new pod's first Register call must hit ErrTokenNotFound (the
  # revoke removed the index entry) → mapped to Unauthenticated by the
  # interceptor. We watch the agent log for that wire error.
  log "Waiting for fresh agent to fail authentication"
  set +e
  local found=0
  for i in {1..90}; do
    local logs
    logs=$(kubectl logs -n "${NAMESPACE}" -l app.kubernetes.io/name=kubebolt-agent --since=30s 2>/dev/null)
    if echo "${logs}" | grep -qE "Unauthenticated|invalid credentials|register:.*PermissionDenied"; then
      ok "agent observed authentication failure post-revoke"
      echo "${logs}" | grep -iE "unauth|invalid|register" | tail -2 | sed 's/^/    /'
      found=1
      break
    fi
    sleep 1
  done
  set -e
  stop_port_forward
  [[ ${found} -eq 1 ]] || fail "agent never observed auth failure within 90s of revoke"
}

# ─── cmd_cleanup ──────────────────────────────────────────────────────

cmd_cleanup() {
  log "Cleaning up"
  stop_port_forward
  helm uninstall -n "${NAMESPACE}" "${AGENT_RELEASE}" 2>/dev/null || true
  helm uninstall -n "${NAMESPACE}" "${RELEASE}" 2>/dev/null || true
  kubectl delete namespace "${NAMESPACE}" --ignore-not-found
  rm -f /tmp/kubebolt-e2e-tenant-id /tmp/kubebolt-e2e-token-id
  if [[ -z "${KEEP:-}" ]]; then
    kind delete cluster --name "${CLUSTER}" 2>/dev/null || true
    ok "cluster deleted"
  else
    info "KEEP=1 set — leaving cluster up for inspection"
  fi
}

# ─── dispatch ─────────────────────────────────────────────────────────

case "${1:-}" in
  setup)   cmd_setup ;;
  test)    cmd_test ;;
  cleanup) cmd_cleanup ;;
  all)
    cmd_setup
    cmd_test
    [[ -z "${KEEP:-}" ]] && cmd_cleanup
    ;;
  *)
    echo "usage: $0 {setup|test|cleanup|all}" >&2
    exit 64
    ;;
esac
