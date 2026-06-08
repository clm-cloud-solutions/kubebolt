package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestServeStdioRoundTrips(t *testing.T) {
	s := newTestServer(&fakeToolProvider{tools: []Tool{{Name: "a", Description: "d", InputSchema: map[string]interface{}{"type": "object"}}}})

	// Two requests + one notification (no response) on three lines.
	in := strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n")
	var out bytes.Buffer

	if err := ServeStdio(context.Background(), s, in, &out); err != nil {
		t.Fatalf("ServeStdio error: %v", err)
	}

	lines := splitNonEmptyLines(out.String())
	if len(lines) != 2 {
		t.Fatalf("got %d response lines, want 2 (notification yields none):\n%s", len(lines), out.String())
	}

	first := decodeLine(t, lines[0])
	if first["id"].(float64) != 1 {
		t.Errorf("first response id = %v, want 1", first["id"])
	}
	second := decodeLine(t, lines[1])
	if second["id"].(float64) != 2 {
		t.Errorf("second response id = %v, want 2", second["id"])
	}
	if _, ok := second["result"].(map[string]any)["tools"]; !ok {
		t.Errorf("second response missing tools: %s", lines[1])
	}
}

func TestServeStdioHandlesTrailingLineWithoutNewline(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	// No trailing newline on the final line.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"ping"}`)
	var out bytes.Buffer
	if err := ServeStdio(context.Background(), s, in, &out); err != nil {
		t.Fatalf("ServeStdio error: %v", err)
	}
	lines := splitNonEmptyLines(out.String())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %s", len(lines), out.String())
	}
	if decodeLine(t, lines[0])["id"].(float64) != 9 {
		t.Errorf("unexpected id: %s", lines[0])
	}
}

func TestServeStdioStopsOnCancelledContext(t *testing.T) {
	s := newTestServer(&fakeToolProvider{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled
	// blockingReader would hang if the loop didn't check ctx first.
	err := ServeStdio(ctx, s, neverReader{}, &bytes.Buffer{})
	if err == nil {
		t.Error("ServeStdio should return the context error when cancelled")
	}
}

// neverReader blocks forever on Read — used to prove the ctx check short
// circuits before the first read.
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) { select {} }

func splitNonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func decodeLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("line is not valid JSON: %v\n%s", err, line)
	}
	return m
}
