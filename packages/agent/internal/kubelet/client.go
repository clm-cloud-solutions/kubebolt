// Package kubelet provides an authenticated HTTPS client to the local node's
// kubelet (usually reachable at https://$NODE_IP:10250 from within a pod on
// that node).
//
// Auth: Bearer token from the ServiceAccount mounted at
// /var/run/secrets/kubernetes.io/serviceaccount/token. The kubelet accepts
// SA tokens when its --authentication-token-webhook is enabled (default
// on kubeadm, managed providers, and docker-desktop).
//
// TLS: Phase B uses InsecureSkipVerify because kubelet cert CA varies by
// distro. Proper verification belongs to Sprint 4 when we produce the
// production Helm chart.
package kubelet

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultTokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	defaultPort      = 10250
	defaultTimeout   = 10 * time.Second
)

type Client struct {
	baseURL   string
	tokenPath string
	http      *http.Client
}

type Option func(*Client)

func WithTokenPath(p string) Option       { return func(c *Client) { c.tokenPath = p } }
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New builds a client pointing at the kubelet on the given node IP. If
// nodeIP is empty it falls back to the KUBEBOLT_AGENT_NODE_IP env var, then
// to "127.0.0.1" for host-network debugging.
func New(nodeIP string, opts ...Option) *Client {
	if nodeIP == "" {
		nodeIP = os.Getenv("KUBEBOLT_AGENT_NODE_IP")
	}
	if nodeIP == "" {
		nodeIP = "127.0.0.1"
	}
	c := &Client{
		baseURL:   fmt.Sprintf("https://%s:%d", nodeIP, defaultPort),
		tokenPath: defaultTokenPath,
		http: &http.Client{
			Timeout: defaultTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Get performs an authenticated GET and returns the response body. Non-2xx
// responses are returned as errors.
func (c *Client) Get(ctx context.Context, path string) ([]byte, error) {
	url := c.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	token, err := c.readToken()
	if err != nil {
		return nil, fmt.Errorf("read SA token: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kubelet %s: %w", path, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kubelet %s: read body: %w", path, err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("kubelet %s: status %d: %s", path, resp.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

// BaseURL returns the URL the client is targeting. Useful for logging.
func (c *Client) BaseURL() string { return c.baseURL }

func (c *Client) readToken() (string, error) {
	b, err := os.ReadFile(c.tokenPath)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
