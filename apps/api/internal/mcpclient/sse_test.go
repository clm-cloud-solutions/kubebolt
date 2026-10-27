package mcpclient

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestReadSSE(t *testing.T) {
	want := json.RawMessage("5")
	for name, tc := range map[string]struct {
		stream  string
		text    string // expected result text; "" means no response
		partial string
	}{
		"single": {
			stream: "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"a\"}}\n\n",
			text:   `{"x":"a"}`,
		},
		"crlf-and-default-event": {
			stream: "data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"crlf\"}}\r\n\r\n",
			text:   `{"x":"crlf"}`,
		},
		"multi-line-data": {
			stream: "data: {\"jsonrpc\":\"2.0\",\ndata: \"id\":5,\ndata: \"result\":{\"x\":\"joined\"}}\n\n",
			text:   `{"x":"joined"}`,
		},
		"no-space-after-colon": {
			stream: "data:{\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"tight\"}}\n\n",
			text:   `{"x":"tight"}`,
		},
		"batch": {
			stream: "data: [{\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\"},{\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"batch\"}}]\n\n",
			text:   `{"x":"batch"}`,
		},
		"skips-requests-notifications-others": {
			stream: ": ping\n\n" +
				"data: {\"jsonrpc\":\"2.0\",\"id\":5,\"method\":\"sampling/createMessage\"}\n\n" + // a server request that reuses our id
				"data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n" +
				"event: endpoint\ndata: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"wrong event\"}}\n\n" +
				"data: {\"jsonrpc\":\"2.0\",\"id\":6,\"result\":{\"x\":\"other\"}}\n\n" +
				"data: {broken\n\n" +
				"retry: 100\nid: 3\ndata: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"right\"}}\n\n",
			text: `{"x":"right"}`,
		},
		"eof-without-blank-line": {
			stream: "data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"eof\"}}\n",
			text:   `{"x":"eof"}`,
		},
		"eof-mid-line": {
			stream: "data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"x\":\"eof\"}}",
			text:   `{"x":"eof"}`,
		},
		"cut-stream": {
			stream:  "data: {\"jsonrpc\":\"2.0\",\"id\":5,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"partial",
			partial: `{"jsonrpc":"2.0","id":5,"result":{"content":[{"type":"text","text":"partial`,
		},
		"never-answers": {
			stream: "data: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\"}\n\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			m, partial, err := readSSE(strings.NewReader(tc.stream), want, discard)
			if tc.text == "" {
				if m != nil || !errors.Is(err, errNoResponse) {
					t.Fatalf("got m=%v err=%v, want errNoResponse", m, err)
				}
				if string(partial) != tc.partial {
					t.Errorf("partial = %q, want %q", partial, tc.partial)
				}
				return
			}
			if err != nil || m == nil {
				t.Fatalf("m=%v err=%v", m, err)
			}
			if string(m.Result) != tc.text {
				t.Errorf("result = %s, want %s", m.Result, tc.text)
			}
		})
	}
}

func TestSalvageText(t *testing.T) {
	full := `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"a \"q\" \\ \/ \n\t é 漢 é 😀 end"}],"isError":false}}`
	if got := salvageText([]byte(full)); got != "a \"q\" \\ / \n\t é 漢 é 😀 end" {
		t.Errorf("complete string decoded as %q", got)
	}
	for cut := 1; cut < len(full); cut++ {
		got := salvageText([]byte(full[:cut]))
		if !strings.Contains(full[:cut], `"text","text":"`) {
			continue // the payload has not started; the fallback applies
		}
		if !strings.HasPrefix("a \"q\" \\ / \n\t é 漢 é 😀 end", got) {
			t.Fatalf("cut at %d: %q is not a prefix of the payload", cut, got)
		}
	}
	// No text value: the partial body comes back, as valid UTF-8.
	if got := salvageText([]byte("{\"jsonrpc\":\"2.0\",\"result\":{\"structured\xe6")); got != `{"jsonrpc":"2.0","result":{"structured` {
		t.Errorf("fallback = %q", got)
	}
	// "text" as the type value is not the payload key.
	if got := salvageText([]byte(`{"content":[{"type":"text", "text" : "spaced`)); got != "spaced" {
		t.Errorf("spaced key = %q", got)
	}
}

func TestShortBody(t *testing.T) {
	if got := shortBody([]byte("<html><head><script>var x=1;</script></head><body><h1>Denied</h1> &amp; gone</body></html>"), "text/html"); got != "Denied & gone" {
		t.Errorf("html = %q", got)
	}
	if got := shortBody([]byte("  plain\n\n text  "), "text/plain"); got != "plain text" {
		t.Errorf("text = %q", got)
	}
	long := strings.Repeat("é", 1000)
	got := shortBody([]byte(long), "")
	if len(got) > maxErrorBody || !strings.HasSuffix(got, "…") || !strings.HasPrefix(long, strings.TrimSuffix(got, "…")) {
		t.Errorf("long body cut badly: %d bytes", len(got))
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"-3":                            0,
		"soon":                          0,
		"Thu, 24 Sep 2026 12:00:30 GMT": 30 * time.Second,
		"Thu, 24 Sep 2026 11:00:00 GMT": 0,
	} {
		if got := parseRetryAfter(v, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&HTTPError{Status: 404, Body: "gone"}, "mcpclient: HTTP 404 Not Found: gone"},
		{&RateLimitError{RetryAfter: 2 * time.Second}, "mcpclient: rate limited (retry after 2s)"},
		{&unauthorizedError{detail: "bad token"}, "mcpclient: unauthorized: bad token"},
		{&BlockedError{Addr: "10.0.0.1:443", Reason: "private"}, "mcpclient: connection to 10.0.0.1:443 blocked (private address)"},
	} {
		if tc.err.Error() != tc.want {
			t.Errorf("%T: %q, want %q", tc.err, tc.err.Error(), tc.want)
		}
	}
}
