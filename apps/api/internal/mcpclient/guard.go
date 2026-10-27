package mcpclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// DialGuard keeps the MCP client from being turned against the network
// KubeBolt runs in. Endpoints are built from curated templates and validated
// fields, so this is the second barrier, for the day one of those slips: a
// connection to loopback, link-local (the cloud metadata services at
// 169.254.169.254 and fd00:ec2::254 live there or in ULA space), private,
// CGNAT, unspecified, multicast or reserved addresses is refused at dial time,
// after DNS resolution, so a public name that resolves inward is caught too.
//
// AllowHostPorts lists the only exceptions, as host:port — the local Azure
// MCP sidecar on 127.0.0.1 in practice. An IP entry matches the resolved
// address; a hostname entry matches the name the client was asked to dial and
// then trusts whatever it resolves to, because the operator named it.
//
// The zero value is ready to use and allows nothing. Use it by pointer.
type DialGuard struct {
	AllowHostPorts []string

	// proxies are the proxy addresses taken from the environment
	// (HTTPS_PROXY, ...). They are the operator's own egress path, so a proxy
	// on a private address is dialled; the target behind it was already
	// checked on the URL.
	proxies sync.Map
}

// NewDialGuard returns a guard that admits only the given host:port pairs
// past the address checks.
func NewDialGuard(allowHostPorts ...string) *DialGuard {
	return &DialGuard{AllowHostPorts: allowHostPorts}
}

// BlockedError is returned when the guard refuses an address.
type BlockedError struct {
	Addr   string
	Reason string
}

func (e *BlockedError) Error() string {
	return "mcpclient: connection to " + e.Addr + " blocked (" + e.Reason + " address)"
}

// Allowed reports whether hostport is on the allowlist.
func (g *DialGuard) Allowed(hostport string) bool {
	if g == nil {
		return false
	}
	want, ok := normalizeHostPort(hostport)
	if !ok {
		return false
	}
	for _, a := range g.AllowHostPorts {
		if n, ok := normalizeHostPort(a); ok && n == want {
			return true
		}
	}
	return false
}

// Control is a net.Dialer Control function: it sees the resolved ip:port of
// every connection attempt and refuses the blocked ranges.
func (g *DialGuard) Control(network, address string, _ syscall.RawConn) error {
	if g.Allowed(address) {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return &BlockedError{Addr: address, Reason: "unparseable"}
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Control is only ever called with a literal address; anything else
		// is not something to connect to.
		return &BlockedError{Addr: address, Reason: "non-literal"}
	}
	if reason := blockedReason(ip); reason != "" {
		return &BlockedError{Addr: address, Reason: reason}
	}
	return nil
}

// DialContext dials addr with the address checks applied, unless addr (as
// requested, before resolution) is allowlisted or is the environment's proxy.
func (g *DialGuard) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if !g.Allowed(addr) && !g.isProxy(addr) {
		d.Control = g.Control
	}
	return d.DialContext(ctx, network, addr)
}

// CheckURL applies the guard to an endpoint before any request is made, so a
// misconfiguration fails at construction with a clear error instead of at
// the first dial: only https, or plain http to an allowlisted host:port; no
// credentials in the URL; no literal address or localhost name in a blocked
// range unless allowlisted.
func (g *DialGuard) CheckURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("mcpclient: empty endpoint")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("mcpclient: endpoint scheme %q is not http(s)", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("mcpclient: endpoint must not carry credentials")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("mcpclient: endpoint has no host")
	}
	hp := hostPortOf(u)
	if g.Allowed(hp) {
		return nil
	}
	if u.Scheme == "http" {
		return fmt.Errorf("mcpclient: plain http is only allowed for allowlisted local endpoints, not %s", hp)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if reason := blockedReason(ip); reason != "" {
			return &BlockedError{Addr: hp, Reason: reason}
		}
		return nil
	}
	lh := strings.TrimSuffix(strings.ToLower(host), ".")
	if lh == "localhost" || strings.HasSuffix(lh, ".localhost") {
		return &BlockedError{Addr: hp, Reason: "loopback"}
	}
	return nil
}

// HTTPClient returns the client New builds by default: the guarded transport
// and no redirects — an MCP endpoint has no business redirecting a POST, and
// following one would re-point a signed request at another host. Callers that
// hold many clients (one per Google service endpoint) can build it once and
// share its connection pool through WithHTTPClient without losing the guard.
func (g *DialGuard) HTTPClient() *http.Client {
	return &http.Client{
		Transport:     g.Transport(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Transport returns an http.Transport whose every connection goes through the
// guard. It does not refuse redirects; HTTPClient does.
func (g *DialGuard) Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 g.proxy,
		DialContext:           g.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// proxy resolves the environment's proxy for req and remembers its address
// so DialContext lets the transport reach it.
func (g *DialGuard) proxy(req *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(req)
	if u != nil {
		g.noteProxy(u)
	}
	return u, err
}

func (g *DialGuard) noteProxy(u *url.URL) {
	if hp, ok := normalizeHostPort(hostPortOf(u)); ok {
		g.proxies.Store(hp, struct{}{})
	}
}

func (g *DialGuard) isProxy(addr string) bool {
	hp, ok := normalizeHostPort(addr)
	if !ok {
		return false
	}
	_, found := g.proxies.Load(hp)
	return found
}

// EndpointHostPort returns the host:port an endpoint URL dials, with the
// scheme's default port filled in — the exact form to put in AllowHostPorts
// for that endpoint (e.g. "127.0.0.1:5001" for the Azure sidecar).
func EndpointHostPort(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("mcpclient: invalid endpoint: %w", err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("mcpclient: endpoint has no host")
	}
	return hostPortOf(u), nil
}

// hostPortOf is u's host:port with the scheme's default port filled in, the
// same address the transport dials.
func hostPortOf(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "http":
			port = "80"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "443"
		}
	}
	return net.JoinHostPort(u.Hostname(), port)
}

// normalizeHostPort canonicalises host:port for comparison: lower-case host
// without a trailing dot, IPs in their canonical (unmapped) form.
func normalizeHostPort(s string) (string, bool) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(s))
	if err != nil || host == "" || port == "" {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		host = ip.Unmap().WithZone("").String()
	} else {
		host = strings.TrimSuffix(strings.ToLower(host), ".")
	}
	return net.JoinHostPort(host, port), true
}

var (
	// nat64 is the well-known NAT64 prefix: the translator connects to the
	// IPv4 address embedded in the low 32 bits.
	nat64 = netip.MustParsePrefix("64:ff9b::/96")

	extraBlocked = []struct {
		prefix netip.Prefix
		reason string
	}{
		{netip.MustParsePrefix("0.0.0.0/8"), "unspecified"},
		{netip.MustParsePrefix("100.64.0.0/10"), "shared (CGNAT)"},
		{netip.MustParsePrefix("192.0.0.0/24"), "reserved"},
		{netip.MustParsePrefix("198.18.0.0/15"), "reserved"},
		{netip.MustParsePrefix("240.0.0.0/4"), "reserved"},
		// Azure's platform endpoint (wireserver, DNS, health probes) is a
		// public address that only means something from inside Azure — where
		// the SaaS runs.
		{netip.MustParsePrefix("168.63.129.16/32"), "cloud platform"},
		{netip.MustParsePrefix("::/96"), "reserved"},
		{netip.MustParsePrefix("fec0::/10"), "site-local"},
	}
)

// blockedReason names the range ip falls in, or "" when it may be dialled.
func blockedReason(ip netip.Addr) string {
	ip = ip.Unmap().WithZone("")
	if nat64.Contains(ip) {
		b := ip.As16()
		if r := blockedReason(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})); r != "" {
			return r
		}
	}
	switch {
	case !ip.IsValid():
		return "invalid"
	case ip.IsUnspecified():
		return "unspecified"
	case ip.IsLoopback():
		return "loopback"
	case ip.IsLinkLocalUnicast(), ip.IsLinkLocalMulticast():
		return "link-local"
	case ip.IsMulticast(), ip.IsInterfaceLocalMulticast():
		return "multicast"
	case ip.IsPrivate():
		return "private"
	}
	for _, b := range extraBlocked {
		if b.prefix.Contains(ip) {
			return b.reason
		}
	}
	return ""
}
