package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kubebolt/kubebolt/apps/api/internal/auth"
)

// agentAuthInfoResponse drives the "is the agent install/configure
// dialog allowed to proceed?" gating in the UI:
//
//   - Enforcement tells the dialog whether picking AuthMode="" is
//     compatible with the running backend. Enforced + empty auth =
//     refused at the pre-flight; the dialog disables Save with a
//     tooltip rather than letting the user discover the mismatch
//     after the agent rolls and crash-loops on the welcome.
//
//   - Tenants populates the dropdown for the "Generate token + create
//     Secret" flow. Disabled tenants are still surfaced so the
//     operator sees what's there, but the UI greys them out.
type agentAuthInfoResponse struct {
	Enforcement string             `json:"enforcement"`
	Tenants     []agentTenantBrief `json:"tenants"`
}

type agentTenantBrief struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Disabled bool   `json:"disabled"`
}

// handleAgentAuthInfo exposes the backend's agent-auth posture so
// the install/configure dialog can guide the operator. Admin-only —
// tenant identifiers are sensitive in multi-tenant deployments.
func (h *handlers) handleAgentAuthInfo(w http.ResponseWriter, r *http.Request) {
	enforcement := h.agentAuthEnforcement
	if enforcement == "" {
		enforcement = "disabled"
	}
	out := agentAuthInfoResponse{Enforcement: enforcement, Tenants: []agentTenantBrief{}}

	// Scope to the CALLER'S OWN org (the session tenant) — never a cross-org
	// picker. The add-cluster wizard registers a cluster into the org the
	// operator is signed into; the old ListTenants() leaked the entire org
	// roster into the dialog. tenantsStore is nil when app auth is disabled →
	// empty list, and the UI falls back to "paste a Secret name" only.
	if h.tenantsStore != nil {
		if t, err := h.tenantsStore.GetTenant(auth.ContextTenantID(r)); err == nil {
			out.Tenants = append(out.Tenants, agentTenantBrief{ID: t.ID, Name: t.Name, Disabled: t.Disabled})
		}
	}
	respondJSON(w, http.StatusOK, out)
}

// agentIssueTokenRequest is the body the dialog's "Generate token +
// create Secret" button sends.
type agentIssueTokenRequest struct {
	// TenantID is accepted for wire-compat but IGNORED — the token is always
	// scoped to the caller's session org (see handleAgentIssueToken).
	TenantID string `json:"tenantId,omitempty"`
	// TeamID is the team that will own the cluster registered with this token
	// (Track D — team-scoped clusters). Empty = unassigned. Multi-tenant only.
	TeamID string `json:"teamId,omitempty"`
	Label  string `json:"label,omitempty"`
	// Materialize=true creates the Secret in the CONNECTED cluster in one click
	// (backend-reachable flows: setup / configure / backend-applied install).
	// Omitted/false = issue-only: the plaintext token is returned so the operator
	// creates the Secret in a REMOTE cluster themselves (add-cluster wizard).
	Materialize bool   `json:"materialize,omitempty"`
	Namespace   string `json:"namespace,omitempty"`  // materialize path; defaults to "kubebolt-system"
	SecretName  string `json:"secretName,omitempty"` // materialize path; defaults to "kubebolt-agent-token"
	TTLSeconds  int64  `json:"ttlSeconds,omitempty"`
}

// agentIssueTokenResponse carries the issued token's identity plus EITHER the
// plaintext token (issue-only — the operator secures a REMOTE cluster via
// kubectl) OR the materialized Secret's name+namespace (backend-reachable
// flows). tokenPrefix/label/tenantId are always set. The plaintext is never
// re-retrievable; a fresh Generate mints a new token.
type agentIssueTokenResponse struct {
	Token       string `json:"token,omitempty"`      // issue-only path
	SecretName  string `json:"secretName,omitempty"` // materialize path
	Namespace   string `json:"namespace,omitempty"`  // materialize path
	TokenPrefix string `json:"tokenPrefix"`
	TokenLabel  string `json:"tokenLabel"`
	TenantID    string `json:"tenantId"`
}

// handleAgentIssueToken issues a fresh ingest token for the given
// tenant, then materializes a K8s Secret in the agent's namespace
// with the plaintext under data.token. The dialog wires this Secret
// name straight into AgentInstallConfig.AuthTokenSecret on Save —
// removing the manual `kubectl create secret` step that was the
// dominant friction point in the install flow.
func (h *handlers) handleAgentIssueToken(w http.ResponseWriter, r *http.Request) {
	if h.tenantsStore == nil {
		respondError(w, http.StatusServiceUnavailable, "tenants store not configured (KUBEBOLT_AUTH_ENABLED must be true to issue ingest tokens)")
		return
	}

	var req agentIssueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	// The token is ALWAYS scoped to the caller's OWN org (the session tenant),
	// never a client-supplied tenantId — the add-cluster wizard registers a
	// cluster into the org the operator is signed into. Cross-org issuance is
	// not a capability of this flow. Validate the org exists so a broken
	// session still 404s instead of minting an orphan token.
	orgID := auth.ContextTenantID(r)
	if _, err := h.tenantsStore.GetTenant(orgID); err != nil {
		if errors.Is(err, auth.ErrTenantNotFound) {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	label := req.Label
	if label == "" {
		label = "agent-install-wizard"
	}
	issuer := auth.ContextUserID(r)
	if issuer == "" {
		issuer = "system"
	}

	// Cluster-unscoped at issue time — the agent's own cluster_id ships in its
	// Hello after boot; TeamID (optional) decides which team owns the cluster
	// once it registers.
	plaintext, tok, err := h.ingestTokens.Issue(r.Context(), orgID, "", req.TeamID, label, issuer, nil)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := agentIssueTokenResponse{
		TokenPrefix: tok.Prefix,
		TokenLabel:  tok.Label,
		TenantID:    orgID,
	}

	if req.Materialize {
		// Backend-reachable flows (setup / configure / backend-applied install):
		// create the Secret in the connected cluster in one click.
		conn := h.manager.Connector(r.Context())
		if conn == nil {
			respondError(w, http.StatusServiceUnavailable, "cluster not connected — cannot materialize the Secret (omit materialize for a remote cluster)")
			return
		}
		ns := req.Namespace
		if ns == "" {
			ns = "kubebolt-system"
		}
		secretName := req.SecretName
		if secretName == "" {
			secretName = "kubebolt-agent-token"
		}
		if err := upsertAgentTokenSecret(r.Context(), conn.Clientset(), ns, secretName, plaintext); err != nil {
			// Best-effort revoke so the issued token doesn't dangle after we
			// failed to wire it into the cluster.
			if revokeErr := h.ingestTokens.Revoke(r.Context(), orgID, tok.ID); revokeErr != nil {
				slog.Warn("issue-token: failed to revoke after Secret apply error", slog.String("error", revokeErr.Error()))
			}
			respondError(w, http.StatusInternalServerError, "create token Secret: "+err.Error())
			return
		}
		resp.SecretName = secretName
		resp.Namespace = ns
	} else {
		// Issue-ONLY (SaaS onboarding): the agent lands in a REMOTE cluster the
		// backend can't reach, so return the plaintext token — shown ONCE — and
		// the wizard emits the `kubectl create secret` the operator runs in THEIR
		// cluster.
		resp.Token = plaintext
	}

	respondJSON(w, http.StatusOK, resp)
}

// upsertAgentTokenSecret creates the Secret on first use, updates otherwise.
// Labels mark it managed-by KubeBolt so future cleanup paths can reason about
// ownership. Auto-creates the namespace when it doesn't exist — the one-click
// materialize flow is meant to be self-contained. Used only when the request
// sets materialize=true (a cluster the backend can reach).
func upsertAgentTokenSecret(ctx context.Context, cs kubernetes.Interface, ns, name, token string) error {
	// Ensure the namespace exists. Tolerate AlreadyExists for the common case
	// where Install / Configure already ran (or the operator pre-created it).
	nsObj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kubebolt",
				"app.kubernetes.io/name":       "kubebolt-agent",
			},
		},
	}
	if _, err := cs.CoreV1().Namespaces().Create(ctx, nsObj, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure namespace %q: %w", ns, err)
	}

	desired := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "kubebolt",
				"app.kubernetes.io/name":       "kubebolt-agent",
				"kubebolt.dev/purpose":         "agent-ingest-token",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token": []byte(token),
		},
	}

	existing, err := cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = cs.CoreV1().Secrets(ns).Create(ctx, desired, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = desired.Data
	existing.Labels = desired.Labels
	existing.Type = desired.Type
	_, err = cs.CoreV1().Secrets(ns).Update(ctx, existing, metav1.UpdateOptions{})
	return err
}
