// Package proxy is the agent-side half of the K8s API proxy. It receives
// KubeProxyRequest payloads on the AgentChannel, executes them against
// the local in-cluster apiserver using the agent's ServiceAccount
// credentials, and sends KubeProxyResponse / KubeProxyWatchEvent back
// to the backend.
//
// One KubeAPIProxy per agent process; stateless beyond the configured
// transport. The agent's identity (SA token + cluster CA) is embedded
// in the http.RoundTripper built from rest.Config — the backend does
// NOT supply credentials in KubeProxyRequest.Headers, and any
// Authorization that comes through is stripped to prevent token
// substitution attacks.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"k8s.io/client-go/rest"

	"github.com/kubebolt/kubebolt/packages/agent/internal/channel"
	agentv2 "github.com/kubebolt/kubebolt/packages/proto/gen/kubebolt/agent/v2"
)

// MaxBodyBytes caps the size of unary request and response bodies the
// proxy will materialize in memory. 10 MiB is comfortable for any
// regular K8s payload (a Pod with status sub-resource is ~5-15 KiB; a
// list of 1000 pods is ~3-5 MiB) while preventing OOM if a misbehaving
// caller streams something huge through.
const MaxBodyBytes = 10 * 1024 * 1024

// KubeAPIProxy executes KubeProxyRequest against the local apiserver.
type KubeAPIProxy struct {
	transport http.RoundTripper
	baseURL   string
}

// New builds a KubeAPIProxy from a rest.Config — typically
// rest.InClusterConfig() so the agent runs against its own cluster.
func New(cfg *rest.Config) (*KubeAPIProxy, error) {
	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, fmt.Errorf("kube proxy: build transport: %w", err)
	}
	return &KubeAPIProxy{
		transport: transport,
		baseURL:   strings.TrimRight(cfg.Host, "/"),
	}, nil
}

// HandleRequest executes a unary kube call and returns a
// KubeProxyResponse. HTTP-level errors (4xx / 5xx) are NOT errors here
// — they ride back as StatusCode + Body; the Error field is reserved
// for network / serialization failures the backend can't infer
// otherwise.
func (p *KubeAPIProxy) HandleRequest(ctx context.Context, req *agentv2.KubeProxyRequest) *agentv2.KubeProxyResponse {
	if int64(len(req.GetBody())) > MaxBodyBytes {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("request body exceeds %d bytes", MaxBodyBytes)}
	}

	httpReq, err := p.buildRequest(ctx, req)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}
	}

	resp, err := p.transport.RoundTrip(httpReq)
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: err.Error()}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("read body: %v", err)}
	}
	if int64(len(body)) > MaxBodyBytes {
		return &agentv2.KubeProxyResponse{Error: fmt.Sprintf("response body exceeds %d bytes", MaxBodyBytes)}
	}

	return &agentv2.KubeProxyResponse{
		StatusCode: uint32(resp.StatusCode),
		Headers:    flattenHeaders(resp.Header),
		Body:       body,
	}
}

// HandleWatch opens a watch stream against the apiserver and returns a
// channel that emits each event as a KubeProxyWatchEvent until the
// stream ends (apiserver closes the connection) or ctx is cancelled.
//
// The wire format K8s ships on a watch endpoint is newline-delimited
// JSON of shape {"type": "...", "object": <raw>}; we decode incrementally
// and emit one KubeProxyWatchEvent per line. The raw object bytes pass
// through unchanged — the backend's client-go reflector parses it.
func (p *KubeAPIProxy) HandleWatch(ctx context.Context, req *agentv2.KubeProxyRequest) (<-chan *agentv2.KubeProxyWatchEvent, error) {
	httpReq, err := p.buildRequest(ctx, req)
	if err != nil {
		return nil, err
	}

	resp, err := p.transport.RoundTrip(httpReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("watch HTTP %d: %s", resp.StatusCode, string(body))
	}

	out := make(chan *agentv2.KubeProxyWatchEvent, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		dec := json.NewDecoder(resp.Body)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			var raw struct {
				Type   string          `json:"type"`
				Object json.RawMessage `json:"object"`
			}
			if err := dec.Decode(&raw); err != nil {
				return // EOF or parse error; either way the stream is done
			}
			ev := &agentv2.KubeProxyWatchEvent{
				EventType: raw.Type,
				Object:    raw.Object,
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// buildRequest constructs the http.Request the proxy sends to the
// apiserver.
//
// Header filtering: any "Authorization" the backend supplied is stripped
// — the agent uses ITS OWN SA token (carried by the transport built from
// rest.Config). Allowing the backend's Authorization through would
// invert the trust model: the apiserver would see the backend's
// credential rather than the agent's, opening a path for the backend to
// impersonate other principals if it ever became compromised.
//
// Hop-by-hop headers (Connection / Transfer-Encoding / Content-Length)
// are stripped because http.NewRequest synthesizes its own.
func (p *KubeAPIProxy) buildRequest(ctx context.Context, req *agentv2.KubeProxyRequest) (*http.Request, error) {
	method := req.GetMethod()
	if method == "" {
		method = "GET"
	}
	url := p.baseURL + req.GetPath()
	var body io.Reader
	if len(req.GetBody()) > 0 {
		body = bytes.NewReader(req.GetBody())
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	for k, v := range req.GetHeaders() {
		if isStrippedHeader(k) {
			continue
		}
		httpReq.Header.Set(k, v)
	}
	return httpReq, nil
}

func isStrippedHeader(k string) bool {
	switch strings.ToLower(k) {
	case "authorization",
		"host",
		"connection",
		"transfer-encoding",
		"content-length":
		return true
	}
	return false
}

func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

// ─── channel.Handler adapter ─────────────────────────────────────────

// Handler is the channel.Handler implementation that delegates
// kube_request dispatch to the KubeAPIProxy. Other Handler methods are
// no-ops; the agent's metrics + heartbeat code keeps living in the
// shipper / channel layer.
type Handler struct {
	proxy *KubeAPIProxy
}

// NewHandler wires a KubeAPIProxy as a channel.Handler.
func NewHandler(p *KubeAPIProxy) *Handler {
	return &Handler{proxy: p}
}

func (h *Handler) HandleHeartbeatAck(*agentv2.HeartbeatAck)     {}
func (h *Handler) HandleConfigUpdate(*agentv2.ConfigUpdate)     {}
func (h *Handler) HandleDisconnect(*agentv2.Disconnect) error   { return nil }

// HandleKubeRequest is invoked by the channel.Client in a fresh
// goroutine for every BackendMessage_KubeRequest. Replies travel back
// through client.Send.
//
// Lifecycle:
//   - Unary: one HandleRequest call, one KubeProxyResponse send. Done.
//   - Watch: HandleWatch opens an event chan; for each event we send
//     KubeProxyWatchEvent. When the apiserver closes the stream OR
//     ctx is cancelled (Run() exited), we send StreamClosed as the
//     terminal marker so the backend's transport can clean up its
//     pending entry without waiting for a timeout.
//
// Send errors after the first one drop the rest of the watch on the
// floor — the stream is already broken; logging once is enough.
func (h *Handler) HandleKubeRequest(ctx context.Context, client *channel.Client, requestID string, req *agentv2.KubeProxyRequest) {
	if req.GetWatch() {
		h.handleWatch(ctx, client, requestID, req)
		return
	}
	resp := h.proxy.HandleRequest(ctx, req)
	if err := client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind:      &agentv2.AgentMessage_KubeResponse{KubeResponse: resp},
	}); err != nil {
		slog.Warn("agent proxy: send kube_response failed",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()))
	}
}

func (h *Handler) handleWatch(ctx context.Context, client *channel.Client, requestID string, req *agentv2.KubeProxyRequest) {
	events, err := h.proxy.HandleWatch(ctx, req)
	if err != nil {
		_ = client.Send(&agentv2.AgentMessage{
			RequestId: requestID,
			Kind: &agentv2.AgentMessage_KubeResponse{
				KubeResponse: &agentv2.KubeProxyResponse{Error: err.Error()},
			},
		})
		return
	}
	for ev := range events {
		if err := client.Send(&agentv2.AgentMessage{
			RequestId: requestID,
			Kind:      &agentv2.AgentMessage_KubeEvent{KubeEvent: ev},
		}); err != nil {
			slog.Warn("agent proxy: send watch event failed",
				slog.String("request_id", requestID),
				slog.String("error", err.Error()))
			return
		}
	}
	// Stream ended naturally (apiserver closed the body) OR ctx was
	// cancelled. Either way, signal terminal to the backend so its
	// transport.Multiplexor releases the pending slot.
	reason := "agent_watch_ended"
	if ctx.Err() != nil {
		reason = "agent_ctx_cancelled"
	}
	_ = client.Send(&agentv2.AgentMessage{
		RequestId: requestID,
		Kind: &agentv2.AgentMessage_StreamClosed{
			StreamClosed: &agentv2.StreamClosed{Reason: reason},
		},
	})
}
