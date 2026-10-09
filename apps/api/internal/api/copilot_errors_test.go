package api

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
)

// Field report 2026-10-08: a 404 from the provider reached the chat as «Set
// KUBEBOLT_AI_MODEL to a model your account has access to (e.g.
// claude-3-5-sonnet-latest, …)» — a deployment variable, stale model ids and
// "your account" read by a SaaS user who runs none of it. Whatever fails, the
// message names neither variables, URLs, model ids nor the provider's body.
func TestFriendlyCopilotError_RevealsNothingInternal(t *testing.T) {
	secretBody := `{"type":"error","error":{"type":"not_found_error","message":"model: claude-nope (org org_01HXYZ)"}}`
	errs := []error{
		&copilot.ProviderHTTPError{StatusCode: 401, Provider: "anthropic", Body: `{"error":"invalid x-api-key sk-ant-…1234"}`},
		&copilot.ProviderHTTPError{StatusCode: 404, Provider: "anthropic", Body: secretBody},
		&copilot.ProviderHTTPError{StatusCode: 404, Provider: "openai", Body: "Not Found"},
		&copilot.ProviderHTTPError{StatusCode: 429, Provider: "anthropic", Body: `{"error":{"type":"rate_limit_error"}}`},
		&copilot.ProviderHTTPError{StatusCode: 529, Provider: "anthropic", Body: `{"error":{"type":"overloaded_error"}}`},
		&copilot.ProviderHTTPError{StatusCode: 500, Provider: "openai", Body: "upstream crashed at 10.0.3.7"},
		&copilot.ProviderHTTPError{StatusCode: 400, Provider: "anthropic", Body: `{"error":{"message":"prompt is too long: 210000 tokens > 200000 maximum"}}`},
		&copilot.ProviderHTTPError{StatusCode: 418, Provider: "openai", Body: "teapot at https://internal.example"},
		&url.Error{Op: "Post", URL: "http://127.0.0.1:9/v1/messages", Err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}},
		fmt.Errorf("parse response: %w", errors.New("unexpected EOF")),
	}
	for _, multiTenant := range []bool{false, true} {
		withMultiTenant(t, multiTenant)
		for _, err := range errs {
			msg := friendlyCopilotError(err)
			for _, leak := range []string{"KUBEBOLT_", "http", "127.0.0.1", "10.0.3.7", "claude", "gpt", "sk-ant", "org_01", "your account", "anthropic", "openai"} {
				if strings.Contains(strings.ToLower(msg), strings.ToLower(leak)) {
					t.Errorf("multiTenant=%v %v: message %q reveals %q", multiTenant, err, msg, leak)
				}
			}
			if msg == "" {
				t.Errorf("multiTenant=%v %v: empty message", multiTenant, err)
			}
		}
	}
}

// Who can fix it depends on who runs the AI: KubeBolt under platform-managed
// AI, the org's admin when the org connected its own provider.
func TestFriendlyCopilotError_NamesWhoCanFixIt(t *testing.T) {
	notFound := &copilot.ProviderHTTPError{StatusCode: 404, Provider: "anthropic"}

	withMultiTenant(t, true)
	if msg := friendlyCopilotError(notFound); !strings.Contains(msg, "KubeBolt support") {
		t.Errorf("platform-managed: %q should point at KubeBolt support", msg)
	}
	withMultiTenant(t, false)
	if msg := friendlyCopilotError(notFound); !strings.Contains(msg, "AI (Kobi)") {
		t.Errorf("own provider: %q should point the admin at the AI settings", msg)
	}

	// A busy provider is nobody's configuration: just try again.
	busy := friendlyCopilotError(&copilot.ProviderHTTPError{StatusCode: 529})
	if strings.Contains(busy, "admin") || strings.Contains(busy, "support") {
		t.Errorf("overload is transient, got %q", busy)
	}
	if long := friendlyCopilotError(&copilot.ProviderHTTPError{StatusCode: 400, Body: "prompt is too long"}); !strings.Contains(long, "Compact") {
		t.Errorf("an oversized prompt should suggest compacting, got %q", long)
	}
}
