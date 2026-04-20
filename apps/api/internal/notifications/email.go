package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"html/template"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

// DigestMode controls how email notifications are grouped before delivery.
type DigestMode string

const (
	// DigestInstant sends one email per insight immediately (same latency as Slack/Discord).
	DigestInstant DigestMode = "instant"
	// DigestHourly aggregates insights received in the last hour into a single email.
	DigestHourly DigestMode = "hourly"
	// DigestDaily aggregates insights received in the last 24 hours into a single email.
	DigestDaily DigestMode = "daily"
)

// EmailConfig holds SMTP connection details and delivery mode.
type EmailConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string
	From       string
	To         []string // one or more recipient addresses
	DigestMode DigestMode
}

// EmailNotifier sends insights by SMTP. In digest mode it buffers events in
// memory and flushes them on a timer until Stop() is called.
type EmailNotifier struct {
	cfg EmailConfig

	mu       sync.Mutex
	buffer   []Event   // only used in digest modes
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewEmailNotifier creates an email notifier and, for digest modes, starts
// the background flusher goroutine. Call Stop() at shutdown to flush pending
// events one last time.
func NewEmailNotifier(cfg EmailConfig) *EmailNotifier {
	if cfg.DigestMode == "" {
		cfg.DigestMode = DigestInstant
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	n := &EmailNotifier{
		cfg:    cfg,
		stopCh: make(chan struct{}),
	}
	if cfg.DigestMode != DigestInstant {
		go n.flushLoop()
	}
	return n
}

func (n *EmailNotifier) Name() string { return "email" }

// DigestMode returns the configured delivery mode for introspection.
func (n *EmailNotifier) DigestMode() string { return string(n.cfg.DigestMode) }

// Send delivers an event. In instant mode it sends synchronously. In digest
// modes it queues the event and returns immediately.
func (n *EmailNotifier) Send(ctx context.Context, e Event) error {
	if n.cfg.DigestMode == DigestInstant {
		return n.sendEmail(ctx, []Event{e})
	}
	// Digest: append to buffer, deduping by the same key the Manager uses.
	n.mu.Lock()
	defer n.mu.Unlock()
	key := dedupKey(e)
	for i, existing := range n.buffer {
		if dedupKey(existing) == key {
			// Update in-place with the latest occurrence
			n.buffer[i] = e
			return nil
		}
	}
	n.buffer = append(n.buffer, e)
	return nil
}

// Stop terminates the digest flush loop and attempts a final delivery of any
// buffered events. Safe to call even when DigestMode is instant.
func (n *EmailNotifier) Stop() {
	n.stopOnce.Do(func() {
		close(n.stopCh)
		// Final flush so pending digest doesn't get lost on shutdown.
		n.mu.Lock()
		events := n.buffer
		n.buffer = nil
		n.mu.Unlock()
		if len(events) > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = n.sendEmail(ctx, events)
		}
	})
}

// flushLoop periodically drains the buffer and sends a digest email.
// Tick interval matches the digest mode.
func (n *EmailNotifier) flushLoop() {
	var interval time.Duration
	switch n.cfg.DigestMode {
	case DigestHourly:
		interval = time.Hour
	case DigestDaily:
		interval = 24 * time.Hour
	default:
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-n.stopCh:
			return
		case <-ticker.C:
			n.mu.Lock()
			events := n.buffer
			n.buffer = nil
			n.mu.Unlock()

			if len(events) == 0 {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := n.sendEmail(ctx, events)
			cancel()
			if err != nil {
				// On failure, re-queue events so the next tick retries.
				n.mu.Lock()
				n.buffer = append(events, n.buffer...)
				n.mu.Unlock()
			}
		}
	}
}

// sendEmail performs the SMTP transaction. Handles both STARTTLS (587) and
// implicit TLS (465). Single email can carry 1..N events (digest or instant).
func (n *EmailNotifier) sendEmail(ctx context.Context, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if n.cfg.From == "" || len(n.cfg.To) == 0 || n.cfg.Host == "" {
		return fmt.Errorf("email: missing required config (from/to/host)")
	}

	subject := buildSubject(events)
	htmlBody, textBody := buildBodies(events)

	msg := buildMessage(n.cfg.From, n.cfg.To, subject, textBody, htmlBody)

	addr := net.JoinHostPort(n.cfg.Host, fmt.Sprintf("%d", n.cfg.Port))

	// Port 465 = implicit TLS (SMTPS). Everything else = STARTTLS.
	useImplicitTLS := n.cfg.Port == 465

	// We respect ctx deadlines by setting a dialer.
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if deadline, ok := ctx.Deadline(); ok {
		dialer.Deadline = deadline
	}

	var conn net.Conn
	var err error
	if useImplicitTLS {
		tlsConn, tlsErr := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: n.cfg.Host})
		if tlsErr != nil {
			return fmt.Errorf("email: TLS dial %s: %w", addr, tlsErr)
		}
		conn = tlsConn
	} else {
		conn, err = dialer.Dial("tcp", addr)
		if err != nil {
			return fmt.Errorf("email: dial %s: %w", addr, err)
		}
	}

	client, err := smtp.NewClient(conn, n.cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("email: new client: %w", err)
	}
	defer client.Close()

	if !useImplicitTLS {
		// Upgrade to TLS when the server advertises STARTTLS. Most modern
		// servers require it before authentication.
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(&tls.Config{ServerName: n.cfg.Host}); err != nil {
				return fmt.Errorf("email: STARTTLS: %w", err)
			}
		}
	}

	if n.cfg.Username != "" {
		auth := smtp.PlainAuth("", n.cfg.Username, n.cfg.Password, n.cfg.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("email: auth: %w", err)
		}
	}

	if err := client.Mail(n.cfg.From); err != nil {
		return fmt.Errorf("email: MAIL FROM: %w", err)
	}
	for _, to := range n.cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("email: RCPT TO %s: %w", to, err)
		}
	}

	wc, err := client.Data()
	if err != nil {
		return fmt.Errorf("email: DATA: %w", err)
	}
	if _, err := wc.Write([]byte(msg)); err != nil {
		return fmt.Errorf("email: write body: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("email: close body: %w", err)
	}
	return client.Quit()
}

// dedupKey matches the Manager's deduplication key so instant and digest
// paths collapse the same repeated insight.
func dedupKey(e Event) string {
	return e.ClusterName + "|" + e.Insight.Resource + "|" + e.Insight.Title
}

// --- Message construction ---

func buildSubject(events []Event) string {
	if len(events) == 1 {
		ins := events[0].Insight
		return fmt.Sprintf("[KubeBolt] %s — %s", strings.ToUpper(ins.Severity), ins.Title)
	}
	// Digest: summarize by severity
	counts := map[string]int{}
	for _, e := range events {
		counts[e.Insight.Severity]++
	}
	parts := []string{}
	for _, sev := range []string{"critical", "warning", "info"} {
		if c := counts[sev]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, sev))
		}
	}
	return fmt.Sprintf("[KubeBolt] Digest — %d insights (%s)", len(events), strings.Join(parts, ", "))
}

// htmlTemplate renders a single event or a list into styled HTML.
// Uses table-based layout for broad email client compatibility.
var htmlTemplate = template.Must(template.New("email").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{.Subject}}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f5f5f5;margin:0;padding:24px;color:#1a1a1a;">
<div style="max-width:640px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 2px 8px rgba(0,0,0,0.08);">
  <div style="padding:20px 24px;background:linear-gradient(135deg,#1DBD7D,#22d68a);color:#fff;">
    <div style="font-size:12px;text-transform:uppercase;letter-spacing:0.1em;opacity:0.85;">KubeBolt</div>
    <div style="font-size:18px;font-weight:600;margin-top:4px;">{{.Subject}}</div>
  </div>
  <div style="padding:8px 0;">
    {{range .Events}}
    <div style="padding:16px 24px;border-bottom:1px solid #eee;">
      <div style="display:inline-block;padding:3px 10px;border-radius:999px;font-size:11px;font-weight:600;text-transform:uppercase;letter-spacing:0.05em;background:{{.BadgeBg}};color:{{.BadgeFg}};">{{.Severity}}</div>
      <div style="font-size:15px;font-weight:600;margin:8px 0 4px;color:#1a1a1a;">{{.Title}}</div>
      <div style="font-size:12px;color:#666;margin-bottom:10px;">
        <span style="font-family:monospace;background:#f5f5f5;padding:1px 6px;border-radius:3px;">{{.Resource}}</span>
        {{if .Namespace}}<span style="margin-left:8px;">in <strong>{{.Namespace}}</strong></span>{{end}}
        {{if .ClusterName}}<span style="margin-left:8px;">on <strong>{{.ClusterName}}</strong></span>{{end}}
      </div>
      <div style="font-size:13px;color:#333;line-height:1.5;">{{.Message}}</div>
      {{if .Suggestion}}
      <div style="margin-top:10px;padding:10px 12px;background:#fef9e7;border-left:3px solid #f5a623;border-radius:4px;font-size:12px;color:#666;">
        <strong style="color:#b07500;">💡 Suggestion</strong><br>{{.Suggestion}}
      </div>
      {{end}}
      {{if .URL}}
      <div style="margin-top:12px;">
        <a href="{{.URL}}" style="display:inline-block;padding:8px 14px;background:#1DBD7D;color:#fff;text-decoration:none;border-radius:6px;font-size:12px;font-weight:500;">Open in KubeBolt →</a>
      </div>
      {{end}}
    </div>
    {{end}}
  </div>
  <div style="padding:14px 24px;background:#fafafa;font-size:11px;color:#999;text-align:center;border-top:1px solid #eee;">
    Sent by KubeBolt · {{.Timestamp}}
  </div>
</div>
</body></html>`))

type emailEvent struct {
	Severity    string
	Title       string
	Resource    string
	Namespace   string
	ClusterName string
	Message     string
	Suggestion  string
	URL         string
	BadgeBg     string
	BadgeFg     string
}

type emailData struct {
	Subject   string
	Events    []emailEvent
	Timestamp string
}

func buildBodies(events []Event) (html string, text string) {
	renderedEvents := make([]emailEvent, 0, len(events))
	var textBuilder strings.Builder
	for _, e := range events {
		ins := e.Insight
		bg, fg := severityBadgeColors(ins.Severity)
		url := ""
		if e.BaseURL != "" {
			url = resourceURL(e.BaseURL, ins)
			if url == "" {
				url = e.BaseURL
			}
		}
		renderedEvents = append(renderedEvents, emailEvent{
			Severity:    strings.ToUpper(ins.Severity),
			Title:       ins.Title,
			Resource:    ins.Resource,
			Namespace:   ins.Namespace,
			ClusterName: e.ClusterName,
			Message:     ins.Message,
			Suggestion:  ins.Suggestion,
			URL:         url,
			BadgeBg:     bg,
			BadgeFg:     fg,
		})
		// Plain text fallback for clients that don't render HTML
		fmt.Fprintf(&textBuilder, "[%s] %s\n", strings.ToUpper(ins.Severity), ins.Title)
		fmt.Fprintf(&textBuilder, "Resource: %s\n", ins.Resource)
		if ins.Namespace != "" {
			fmt.Fprintf(&textBuilder, "Namespace: %s\n", ins.Namespace)
		}
		if e.ClusterName != "" {
			fmt.Fprintf(&textBuilder, "Cluster: %s\n", e.ClusterName)
		}
		fmt.Fprintf(&textBuilder, "\n%s\n", ins.Message)
		if ins.Suggestion != "" {
			fmt.Fprintf(&textBuilder, "\nSuggestion: %s\n", ins.Suggestion)
		}
		if url != "" {
			fmt.Fprintf(&textBuilder, "\nOpen: %s\n", url)
		}
		textBuilder.WriteString("\n---\n\n")
	}

	subject := buildSubject(events)
	data := emailData{
		Subject:   subject,
		Events:    renderedEvents,
		Timestamp: time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}
	var buf bytes.Buffer
	_ = htmlTemplate.Execute(&buf, data)
	return buf.String(), textBuilder.String()
}

// buildMessage assembles the RFC-5322 message with multipart/alternative
// so clients render HTML when they can and text otherwise.
func buildMessage(from string, to []string, subject, text, html string) string {
	boundary := "kubebolt-boundary-" + fmt.Sprintf("%d", time.Now().UnixNano())
	var msg strings.Builder
	fmt.Fprintf(&msg, "From: %s\r\n", from)
	fmt.Fprintf(&msg, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&msg, "Subject: %s\r\n", subject)
	msg.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", boundary)
	// Text part
	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msg.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	msg.WriteString(text)
	msg.WriteString("\r\n")
	// HTML part
	fmt.Fprintf(&msg, "--%s\r\n", boundary)
	msg.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	msg.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	msg.WriteString(html)
	msg.WriteString("\r\n")
	fmt.Fprintf(&msg, "--%s--\r\n", boundary)
	return msg.String()
}

// severityBadgeColors returns the background and foreground colors for the
// severity badge in the HTML email.
func severityBadgeColors(severity string) (bg, fg string) {
	switch severity {
	case "critical":
		return "#fee2e2", "#b91c1c"
	case "warning":
		return "#fef3c7", "#92400e"
	case "info":
		return "#dbeafe", "#1e40af"
	default:
		return "#f3f4f6", "#374151"
	}
}
