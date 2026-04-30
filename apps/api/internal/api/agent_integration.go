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

	// tenantsStore is nil when app auth is disabled. The UI receives
	// an empty tenants list and falls back to "paste a Secret name"
	// only.
	if h.tenantsStore != nil {
		tenants, err := h.tenantsStore.ListTenants()
		if err != nil {
			respondError(w, http.StatusInternalServerError, "list tenants: "+err.Error())
			return
		}
		for i := range tenants {
			out.Tenants = append(out.Tenants, agentTenantBrief{
				ID:       tenants[i].ID,
				Name:     tenants[i].Name,
				Disabled: tenants[i].Disabled,
			})
		}
	}
	respondJSON(w, http.StatusOK, out)
}

// agentIssueTokenRequest is the body the dialog's "Generate token +
// create Secret" button sends.
type agentIssueTokenRequest struct {
	TenantID   string `json:"tenantId"`
	Label      string `json:"label,omitempty"`
	Namespace  string `json:"namespace,omitempty"`  // defaults to "kubebolt-system"
	SecretName string `json:"secretName,omitempty"` // defaults to "kubebolt-agent-token"
	TTLSeconds int64  `json:"ttlSeconds,omitempty"`
}

// agentIssueTokenResponse intentionally OMITS the plaintext token.
// The token only ever lives in the cluster Secret we just created —
// the operator can retrieve it with `kubectl get secret -o yaml`
// when needed. Returning the plaintext over the API would force a
// "save it now or lose it" UX dialog, which is exactly what we're
// trying to eliminate by storing it server-side.
type agentIssueTokenResponse struct {
	SecretName  string `json:"secretName"`
	Namespace   string `json:"namespace"`
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
	conn := h.manager.Connector()
	if conn == nil {
		respondError(w, http.StatusServiceUnavailable, "cluster not connected")
		return
	}

	var req agentIssueTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.TenantID == "" {
		respondError(w, http.StatusBadRequest, "tenantId is required")
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
	label := req.Label
	if label == "" {
		label = "agent-install-wizard"
	}

	issuer := auth.ContextUserID(r)
	if issuer == "" {
		issuer = "system"
	}

	plaintext, tok, err := h.tenantsStore.IssueToken(req.TenantID, label, issuer, nil)
	if err != nil {
		// Tenant lookup miss → 404; everything else is a 400 from
		// the store's own validation (label too long, etc).
		if errors.Is(err, auth.ErrTenantNotFound) {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := upsertAgentTokenSecret(r.Context(), conn.Clientset(), ns, secretName, plaintext); err != nil {
		// Best-effort revoke so the issued token doesn't dangle
		// after we failed to wire it into the cluster. RevokeToken
		// errors are logged but don't override the underlying
		// failure that brought us here.
		if revokeErr := h.tenantsStore.RevokeToken(req.TenantID, tok.ID); revokeErr != nil {
			slog.Warn("issue-token: failed to revoke after Secret apply error",
				slog.String("error", revokeErr.Error()),
			)
		}
		respondError(w, http.StatusInternalServerError, "create token Secret: "+err.Error())
		return
	}

	respondJSON(w, http.StatusOK, agentIssueTokenResponse{
		SecretName:  secretName,
		Namespace:   ns,
		TokenPrefix: tok.Prefix,
		TokenLabel:  tok.Label,
		TenantID:    req.TenantID,
	})
}

// upsertAgentTokenSecret creates the Secret on first use, updates
// otherwise. Labels mark it managed-by KubeBolt so future cleanup
// paths can reason about ownership. Auto-creates the namespace
// when it doesn't exist — the wizard's "Generate token + create
// Secret" button is supposed to be self-contained, the operator
// shouldn't have to pre-provision the namespace.
func upsertAgentTokenSecret(ctx context.Context, cs kubernetes.Interface, ns, name, token string) error {
	// Ensure the namespace exists. Tolerate AlreadyExists for the
	// common case where Install / Configure already ran (or the
	// operator pre-created it manually). Real errors (RBAC denied,
	// API down) bubble up.
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
