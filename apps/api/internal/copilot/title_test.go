package copilot

import (
	"context"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/config"
)

func TestHeuristicTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "New conversation"},
		{"   ", "New conversation"},
		{"Why is the payments pod OOMKilled?", "Why is the payments pod OOMKilled"},
		{"  scale   the   api   deployment  ", "scale the api deployment"},
		{"Investigate this. Then that. And more.", "Investigate this"},
		{strings.Repeat("a", 200), strings.Repeat("a", conversationTitleMaxLen) + "…"},
	}
	for _, c := range cases {
		if got := HeuristicTitle(c.in); got != c.want {
			t.Errorf("HeuristicTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeTitle(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"payments pod OOMKilled"`, "payments pod OOMKilled"},
		{"ingress 503 after deploy.", "ingress 503 after deploy"},
		{"  node   pressure\n", "node pressure"},
		{"`backtick wrapped`", "backtick wrapped"},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeTitle(c.in); got != c.want {
			t.Errorf("SanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGenerateTitle_SuccessSanitizes(t *testing.T) {
	registerFake(t, "title-fake-ok", `  "payments pod OOMKilled."  `)
	title, err := GenerateTitle(
		context.Background(),
		config.ProviderConfig{Provider: "title-fake-ok"},
		"why is the payments pod OOMing?",
		"It's OOMKilled — memory limit too low.",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if title != "payments pod OOMKilled" {
		t.Fatalf("title = %q, want sanitized %q", title, "payments pod OOMKilled")
	}
}

func TestGenerateTitle_UnknownProviderErrors(t *testing.T) {
	if _, err := GenerateTitle(
		context.Background(),
		config.ProviderConfig{Provider: "no-such-provider-xyz"},
		"q", "",
	); err == nil {
		t.Fatalf("expected error for unknown provider")
	}
}

func TestGenerateTitle_EmptyReplyErrors(t *testing.T) {
	registerFake(t, "title-fake-empty", "    ")
	if _, err := GenerateTitle(
		context.Background(),
		config.ProviderConfig{Provider: "title-fake-empty"},
		"q", "",
	); err == nil {
		t.Fatalf("expected error when the model returns an empty title")
	}
}
