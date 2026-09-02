package auth

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/kubebolt/kubebolt/apps/api/internal/audit"
)

// auditAdmin records one administrative-plane mutation: users, teams, tokens,
// org limits.
//
// It is the sibling of api.auditMutation, which covers cluster mutations. Two
// helpers rather than one because they live in packages that cannot import each
// other, and both write to the same trail through audit.Emit — so a reviewer
// reading /admin/actions sees "restarted a Deployment" and "granted someone
// admin" in one chronological list. Splitting the STORE was never on the table:
// the question an auditor asks is "what happened on this account", not "what
// happened in each subsystem".
//
// ClusterID is deliberately left empty. The administrative plane is not scoped
// to a cluster, and stamping whichever cluster happened to be selected in the
// operator's UI would invent an association that does not exist — worse than no
// value, because it reads as evidence.
//
// Never called with a password, token value, or any other credential in params.
// The audit trail is long-lived by design; a secret written here outlives the
// rotation that was supposed to retire it.
func auditAdmin(r *http.Request, action, targetType, targetName string, params map[string]any, err error) {
	source := r.Header.Get("X-KubeBolt-Action-Source")
	if source == "" {
		source = "ui"
	}
	var userID, username string
	if claims := ContextClaims(r); claims != nil {
		userID = claims.UserID
		username = claims.Username
	}
	role := string(ContextRole(r))
	result := "success"

	attrs := []any{
		slog.String("audit", "admin"),
		slog.String("action", action),
		slog.String("source", source),
		slog.String("user_id", userID),
		slog.String("username", username),
		slog.String("role", role),
		slog.String("target_type", targetType),
		slog.String("target_name", targetName),
		slog.Any("params", params),
	}
	if err != nil {
		result = "error"
		attrs = append(attrs, slog.String("error", err.Error()))
	}
	attrs = append(attrs, slog.String("result", result))
	if err != nil {
		slog.Warn("admin mutation", attrs...)
	} else {
		slog.Info("admin mutation", attrs...)
	}

	if !audit.Enabled() {
		return
	}
	rec := &audit.Record{
		ID:         uuid.New().String(),
		Timestamp:  time.Now().UTC(),
		TenantID:   ContextTenantID(r),
		Source:     source,
		UserID:     userID,
		Username:   username,
		Role:       role,
		Action:     action,
		TargetType: targetType,
		TargetName: targetName,
		Params:     params,
		Result:     result,
	}
	if err != nil {
		rec.Error = err.Error()
	}
	audit.Emit(rec)
}
