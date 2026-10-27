package mcpclient

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/kubebolt/kubebolt/apps/api/internal/mcp/wire"
)

// inbound is any JSON-RPC message a server can send: a response (id, result
// or error), a request (id, method) or a notification (method only).
type inbound struct {
	wire.RawResponse
	Method string `json:"method,omitempty"`
}

// errNoResponse means the stream ended without the response we waited for.
var errNoResponse = errors.New("mcpclient: stream ended without a response")

// matchInbound scans one decoded payload — a single message or a batch — for
// the response to want. Server-to-client requests (sampling, elicitation,
// roots) and notifications (progress, logging) are skipped and never
// answered: KubeBolt implements none of those capabilities and does not
// advertise them, and a server blocked on one gets our silence until the
// call times out.
func matchInbound(data []byte, want json.RawMessage, log *slog.Logger) (*inbound, error) {
	data = bytes.TrimSpace(data)
	var msgs []inbound
	if len(data) > 0 && data[0] == '[' {
		if err := json.Unmarshal(data, &msgs); err != nil {
			return nil, err
		}
	} else {
		var m inbound
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		msgs = []inbound{m}
	}
	for i := range msgs {
		m := &msgs[i]
		if m.Method != "" {
			log.Debug("mcp: ignoring server message", "method", m.Method, "request", len(m.ID) > 0)
			continue
		}
		if bytes.Equal(bytes.TrimSpace(m.ID), want) {
			return m, nil
		}
	}
	return nil, nil
}

// readSSE reads a text/event-stream body until the JSON-RPC response whose id
// is want arrives. It returns the response, or — when the stream ends first —
// the data of the event that was still being assembled, which is what a
// truncated read leaves behind.
func readSSE(r io.Reader, want json.RawMessage, log *slog.Logger) (*inbound, []byte, error) {
	br := bufio.NewReader(r)
	var (
		data      bytes.Buffer
		eventType string
	)
	dispatch := func() (*inbound, error) {
		defer func() { data.Reset(); eventType = "" }()
		if data.Len() == 0 || (eventType != "" && eventType != "message") {
			return nil, nil
		}
		m, err := matchInbound(bytes.TrimSuffix(data.Bytes(), []byte("\n")), want, log)
		if err != nil {
			// One malformed event does not poison the stream; the response
			// may still follow.
			log.Debug("mcp: skipping undecodable SSE event", "err", err)
			return nil, nil
		}
		return m, nil
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, nil, err
		}
		complete := strings.HasSuffix(line, "\n")
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if !complete {
			// The stream ended mid-line. A server that closes right after the
			// last data line, without the blank line, still sent a whole
			// message; a cut stream did not, and its data is the partial.
			if line != "" {
				appendField(&data, &eventType, line)
			}
			if data.Len() > 0 {
				pending := append([]byte(nil), data.Bytes()...)
				if m, _ := dispatch(); m != nil {
					return m, nil, nil
				}
				return nil, bytes.TrimSuffix(pending, []byte("\n")), errNoResponse
			}
			return nil, nil, errNoResponse
		}
		if line == "" {
			if m, err := dispatch(); m != nil || err != nil {
				return m, nil, err
			}
			continue
		}
		appendField(&data, &eventType, line)
	}
}

// appendField applies one SSE field line to the event being assembled. Only
// data and event matter here; id and retry serve reconnection, which a
// single request/response exchange does not do.
func appendField(data *bytes.Buffer, eventType *string, line string) {
	if strings.HasPrefix(line, ":") {
		return // comment / keep-alive
	}
	field, value, found := strings.Cut(line, ":")
	if found {
		value = strings.TrimPrefix(value, " ")
	}
	switch field {
	case "data":
		data.WriteString(value)
		data.WriteByte('\n')
	case "event":
		*eventType = value
	}
}
