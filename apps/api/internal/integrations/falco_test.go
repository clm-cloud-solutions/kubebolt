package integrations

import (
	"context"
	"testing"
)

func TestFalcoNormalize(t *testing.T) {
	p := NewFalco().(SignalProvider)
	if p.IngestMode() != IngestHTTPSink {
		t.Fatalf("mode = %q", p.IngestMode())
	}

	body := []byte(`{
		"output": "19:32:18.123: Critical A shell was spawned in a container (user=root container=postgres)",
		"priority": "CRITICAL",
		"rule": "Terminal shell in container",
		"time": "2026-07-13T19:32:18.123Z",
		"output_fields": {"k8s.ns.name": "production", "k8s.pod.name": "postgres-0", "proc.cmdline": "sh -c id", "user.name": "root"},
		"hostname": "node-a1",
		"source": "syscalls"
	}`)
	sig, err := p.Normalize(context.Background(), body)
	if err != nil || len(sig.Events) != 1 {
		t.Fatalf("Normalize: %v / %d events", err, len(sig.Events))
	}
	ev := sig.Events[0]
	if ev.Source != "falco" || ev.RuleName != "Terminal shell in container" || ev.Priority != "Critical" {
		t.Fatalf("event core: %+v", ev)
	}
	if ev.Namespace != "production" || ev.PodName != "postgres-0" {
		t.Fatalf("k8s identity from output_fields: %+v", ev)
	}
	if ev.Fields["proc.cmdline"] != "sh -c id" || ev.Fields["hostname"] != "node-a1" {
		t.Fatalf("fields: %+v", ev.Fields)
	}
	if ev.At.Format("2006-01-02") != "2026-07-13" {
		t.Fatalf("time from payload: %v", ev.At)
	}

	// Garbage / wrong type / empty rule+output → errors.
	if _, err := p.Normalize(context.Background(), []byte("not json")); err == nil {
		t.Fatal("garbage must error")
	}
	if _, err := p.Normalize(context.Background(), 42); err == nil {
		t.Fatal("wrong type must error")
	}
	if _, err := p.Normalize(context.Background(), []byte(`{"priority":"Critical"}`)); err == nil {
		t.Fatal("payload with neither rule nor output must error")
	}
}
