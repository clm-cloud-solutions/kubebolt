package mcpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

func TestBlockedReason(t *testing.T) {
	blocked := map[string]string{
		"169.254.169.254":          "link-local", // AWS/GCP/Azure IMDS
		"fd00:ec2::254":            "private",    // AWS IMDS over IPv6
		"10.0.0.1":                 "private",
		"172.16.5.4":               "private",
		"192.168.1.1":              "private",
		"127.0.0.1":                "loopback",
		"127.9.9.9":                "loopback",
		"::1":                      "loopback",
		"fe80::1":                  "link-local",
		"100.64.0.1":               "shared (CGNAT)",
		"100.100.100.200":          "shared (CGNAT)", // Alibaba metadata
		"0.0.0.0":                  "unspecified",
		"0.1.2.3":                  "unspecified",
		"::":                       "unspecified",
		"224.0.0.1":                "link-local", // link-local multicast
		"239.1.2.3":                "multicast",
		"ff02::1":                  "link-local",
		"ff05::2":                  "multicast",
		"255.255.255.255":          "reserved",
		"192.0.0.192":              "reserved", // Oracle Cloud metadata
		"168.63.129.16":            "cloud platform",
		"::ffff:169.254.169.254":   "link-local", // IPv4-mapped
		"::ffff:10.1.2.3":          "private",
		"64:ff9b::a9fe:a9fe":       "link-local", // NAT64 of 169.254.169.254
		"::a9fe:a9fe":              "reserved",   // IPv4-compatible
		"fec0::1":                  "site-local",
		"fe80::1%eth0":             "link-local",
		"2001:4860:4860::8888%eth": "",
	}
	for s, want := range blocked {
		ip, err := netip.ParseAddr(s)
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		if got := blockedReason(ip); got != want {
			t.Errorf("blockedReason(%s) = %q, want %q", s, got, want)
		}
	}
	for _, s := range []string{"8.8.8.8", "52.94.133.131", "142.250.72.10", "2001:4860:4860::8888", "64:ff9b::808:808"} {
		if r := blockedReason(netip.MustParseAddr(s)); r != "" {
			t.Errorf("public %s blocked as %q", s, r)
		}
	}
}

func TestControl(t *testing.T) {
	g := NewDialGuard("127.0.0.1:5001", "[::1]:5001")
	for _, tc := range []struct {
		addr string
		ok   bool
	}{
		{"169.254.169.254:80", false},
		{"10.0.0.1:443", false},
		{"127.0.0.1:5001", true},
		{"127.0.0.1:5002", false},
		{"[::1]:5001", true},
		{"[::ffff:127.0.0.1]:5001", true}, // the mapped form of an allowed address
		{"93.184.216.34:443", true},
		{"[2606:4700::6810:84e5]:443", true},
		{"not-an-address", false},
	} {
		err := g.Control("tcp", tc.addr, nil)
		if (err == nil) != tc.ok {
			t.Errorf("Control(%s) = %v, want ok=%v", tc.addr, err, tc.ok)
		}
		var be *BlockedError
		if err != nil && !errors.As(err, &be) {
			t.Errorf("Control(%s) error is %T, want *BlockedError", tc.addr, err)
		}
	}
	var zero *DialGuard
	if zero.Allowed("127.0.0.1:5001") {
		t.Error("a nil guard allows nothing")
	}
}

func TestDialContextAppliesTheGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	strict := &DialGuard{}
	if conn, err := strict.DialContext(ctx, "tcp", addr); err == nil {
		conn.Close()
		t.Fatal("dialed loopback without an allowlist")
	} else {
		var be *BlockedError
		if !errors.As(err, &be) || be.Reason != "loopback" {
			t.Errorf("err = %v, want a loopback BlockedError", err)
		}
	}
	// A public name that resolves inward is caught after resolution.
	if conn, err := strict.DialContext(ctx, "tcp", net.JoinHostPort("localhost", port)); err == nil {
		conn.Close()
		t.Fatal("dialed localhost without an allowlist")
	}

	for _, allow := range []string{addr, net.JoinHostPort("localhost", port)} {
		dial := addr
		if allow != addr {
			dial = allow
		}
		conn, err := NewDialGuard(allow).DialContext(ctx, "tcp", dial)
		if err != nil {
			t.Errorf("allowlisted %s: %v", allow, err)
			continue
		}
		conn.Close()
	}
}

func TestProxyAddressIsDialable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	g := &DialGuard{}
	g.noteProxy(&url.URL{Scheme: "http", Host: srv.Listener.Addr().String()})
	conn, err := g.DialContext(t.Context(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("the environment's proxy must be reachable: %v", err)
	}
	conn.Close()
}

func TestGuardedTransportEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) }))
	defer srv.Close()

	blocked := &http.Client{Transport: (&DialGuard{}).Transport()}
	if resp, err := blocked.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("guarded transport reached loopback")
	}
	allowed := &http.Client{Transport: NewDialGuard(srv.Listener.Addr().String()).Transport()}
	resp, err := allowed.Get(srv.URL)
	if err != nil {
		t.Fatalf("allowlisted: %v", err)
	}
	resp.Body.Close()
}

func TestNewRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the client followed a redirect")
	}))
	defer target.Close()
	f := newFake(t)
	f.hook = func(w http.ResponseWriter, r *http.Request, _ wire.Request, _ []byte) bool {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
		return true
	}
	srv := f.start()
	c, err := New(srv.URL, WithGuard(NewDialGuard(srv.Listener.Addr().String(), target.Listener.Addr().String())))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Initialize(t.Context())
	var he *HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusTemporaryRedirect {
		t.Errorf("err = %v, want the 307 as an *HTTPError", err)
	}
}

func TestEndpointHostPort(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:5001/":                "127.0.0.1:5001",
		"http://localhost/":                     "localhost:80",
		"https://aws-mcp.us-east-1.api.aws/mcp": "aws-mcp.us-east-1.api.aws:443",
		"http://[::1]:5001":                     "[::1]:5001",
	} {
		got, err := EndpointHostPort(raw)
		if err != nil || got != want {
			t.Errorf("EndpointHostPort(%q) = %q, %v; want %q", raw, got, err, want)
		}
		// The derived entry admits the endpoint it came from.
		if _, err := New(raw, WithGuard(NewDialGuard(got))); err != nil {
			t.Errorf("New(%q) with its own host:port allowlisted: %v", raw, err)
		}
	}
	for _, bad := range []string{"", "://x", "/relative"} {
		if _, err := EndpointHostPort(bad); err == nil {
			t.Errorf("EndpointHostPort(%q) accepted", bad)
		}
	}
}
