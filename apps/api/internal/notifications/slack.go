package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// SlackNotifier sends messages via a Slack incoming webhook.
// Uses Block Kit for rich formatting with color-coded severity.
type SlackNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewSlackNotifier creates a Slack notifier with a 10-second HTTP timeout.
// Set webhookURL to the "Incoming Webhook" URL from Slack (https://api.slack.com/messaging/webhooks).
func NewSlackNotifier(webhookURL string) *SlackNotifier {
	return &SlackNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *SlackNotifier) Name() string {
	return "slack"
}

// Send delivers an insight to Slack using Block Kit formatting.
// The attachment color matches the severity (red/amber/blue).
func (s *SlackNotifier) Send(ctx context.Context, e Event) error {
	payload := s.buildPayload(e)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("slack webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("slack webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// buildPayload constructs a Block Kit payload. Slack expects:
//
//	{ attachments: [{ color, blocks: [...] }] }
//
// We wrap everything in an attachment so the color bar on the left reflects severity.
func (s *SlackNotifier) buildPayload(e Event) map[string]any {
	ins := e.Insight

	icon, color := severityIconAndColor(ins.Severity, "slack")

	header := fmt.Sprintf("%s *%s* — %s", icon, strings.ToUpper(ins.Severity), ins.Title)
	if e.ClusterName != "" {
		header = fmt.Sprintf("%s _on cluster_ `%s`", header, e.ClusterName)
	}

	fields := []map[string]any{
		{"type": "mrkdwn", "text": fmt.Sprintf("*Resource*\n`%s`", ins.Resource)},
	}
	if ins.Namespace != "" {
		fields = append(fields, map[string]any{
			"type": "mrkdwn", "text": fmt.Sprintf("*Namespace*\n`%s`", ins.Namespace),
		})
	}
	if ins.Category != "" {
		fields = append(fields, map[string]any{
			"type": "mrkdwn", "text": fmt.Sprintf("*Category*\n%s", ins.Category),
		})
	}

	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": header}},
		{"type": "section", "fields": fields},
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": ins.Message}},
	}

	if ins.Suggestion != "" {
		blocks = append(blocks, map[string]any{
			"type": "section",
			"text": map[string]any{"type": "mrkdwn", "text": fmt.Sprintf(":bulb: *Suggestion*\n%s", ins.Suggestion)},
		})
	}

	// Context footer
	footer := fmt.Sprintf("KubeBolt · first seen %s", ins.FirstSeen.UTC().Format("2006-01-02 15:04 UTC"))
	if e.BaseURL != "" {
		footer = fmt.Sprintf("<%s|Open in KubeBolt> · %s", e.BaseURL, footer)
	}
	blocks = append(blocks, map[string]any{
		"type":     "context",
		"elements": []map[string]any{{"type": "mrkdwn", "text": footer}},
	})

	return map[string]any{
		"attachments": []map[string]any{
			{
				"color":  color,
				"blocks": blocks,
			},
		},
	}
}
