// The client against KubeBolt's own MCP server — the one real server the
// test suite can run. It lives in the external test package so importing the
// server (and through it the copilot executor) can never form an import cycle
// if the copilot or the gateway later import this client.
package mcpclient_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kubebolt/kubebolt/apps/api/internal/copilot"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
	"github.com/kubebolt/kubebolt/apps/api/internal/mcpclient"
)

func kubeboltMCP(t *testing.T) *mcpclient.Client {
	t.Helper()
	srv := httptest.NewServer(mcp.Handler(mcp.NewServer(
		mcp.ServerInfo{Name: "kubebolt-kobi", Version: "test"},
		mcp.NewExecutorToolProvider(copilot.NewExecutor(nil)),
		mcp.NewKobiPromptProvider(),
	)))
	t.Cleanup(srv.Close)
	c, err := mcpclient.New(srv.URL, mcpclient.WithGuard(mcpclient.NewDialGuard(srv.Listener.Addr().String())))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAgainstKubeBoltMCPServer(t *testing.T) {
	c := kubeboltMCP(t)

	info, err := c.Initialize(t.Context())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.ServerInfo.Name != "kubebolt-kobi" || info.ProtocolVersion != wire.ProtocolVersion || info.Capabilities.Tools == nil {
		t.Errorf("initialize = %+v", info)
	}
	if c.SessionID() != "" {
		t.Error("KubeBolt's server is stateless; no session id expected")
	}

	tools, err := c.ListTools(t.Context())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
		if strings.HasPrefix(tl.Name, "propose_") {
			t.Errorf("mutating tool %s listed", tl.Name)
		}
	}
	if !names["get_kubebolt_docs"] || !names["get_cluster_overview"] {
		t.Fatalf("catalog lacks the expected read tools: %d tools", len(tools))
	}

	// A tool that needs no cluster succeeds.
	res, err := c.CallTool(t.Context(), "get_kubebolt_docs", json.RawMessage(`{"topic":"overview"}`))
	if err != nil || res.IsError || res.Truncated || res.Text() == "" {
		t.Errorf("get_kubebolt_docs: res=%+v err=%v", res, err)
	}

	// A cluster tool with no cluster is a tool failure, reported in the result.
	res, err = c.CallTool(t.Context(), "get_cluster_overview", nil)
	if err != nil || !res.IsError {
		t.Errorf("get_cluster_overview without a cluster: res=%+v err=%v", res, err)
	}

	// A withheld or unknown tool is a protocol error the caller can inspect.
	for _, name := range []string{"propose_delete_resource", "no_such_tool"} {
		_, err = c.CallTool(t.Context(), name, nil)
		var rpc *wire.Error
		if !errors.As(err, &rpc) || rpc.Code != wire.CodeInvalidParams {
			t.Errorf("%s: err = %v, want JSON-RPC -32602", name, err)
		}
	}

	// No session was issued, so Close has nothing to DELETE.
	if err := c.Close(t.Context()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestAgainstKubeBoltMCPServerConcurrently(t *testing.T) {
	c := kubeboltMCP(t)
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		go func() {
			res, err := c.CallTool(t.Context(), "get_kubebolt_docs", json.RawMessage(`{"topic":"overview"}`))
			if err == nil && res.IsError {
				err = errors.New(res.Text())
			}
			errs <- err
		}()
	}
	for i := 0; i < 20; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}
