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

// DiscordNotifier sends messages via a Discord webhook using embeds.
// Discord embed docs: https://discord.com/developers/docs/resources/channel#embed-object
type DiscordNotifier struct {
	webhookURL string
	client     *http.Client
}

// NewDiscordNotifier creates a Discord notifier. Accepts a webhook URL in either
// form: plain ("https://discord.com/api/webhooks/...") or with query params.
func NewDiscordNotifier(webhookURL string) *DiscordNotifier {
	return &DiscordNotifier{
		webhookURL: webhookURL,
		client:     &http.Client{Timeout: 10 * time.Second},
	}
}

func (d *DiscordNotifier) Name() string {
	return "discord"
}

// Send delivers an insight to Discord as an embed, color-coded by severity.
func (d *DiscordNotifier) Send(ctx context.Context, e Event) error {
	payload := d.buildPayload(e)

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal discord payload: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.webhookURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build discord request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("discord webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("discord webhook returned %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// buildPayload constructs a Discord webhook payload with a rich embed.
// Discord expects color as a decimal RGB integer.
func (d *DiscordNotifier) buildPayload(e Event) map[string]any {
	ins := e.Insight
	icon, _ := severityIconAndColor(ins.Severity, "discord")
	color := discordColorForSeverity(ins.Severity)

	title := fmt.Sprintf("%s %s — %s", icon, strings.ToUpper(ins.Severity), ins.Title)
	if len(title) > 256 {
		title = title[:253] + "..."
	}

	description := ins.Message
	if ins.Suggestion != "" {
		description += "\n\n💡 **Suggestion**\n" + ins.Suggestion
	}
	// Discord hard-caps description at 4096 chars
	if len(description) > 4000 {
		description = description[:3997] + "..."
	}

	fields := []map[string]any{
		{"name": "Resource", "value": fmt.Sprintf("`%s`", ins.Resource), "inline": true},
	}
	if ins.Namespace != "" {
		fields = append(fields, map[string]any{
			"name": "Namespace", "value": fmt.Sprintf("`%s`", ins.Namespace), "inline": true,
		})
	}
	if ins.Category != "" {
		fields = append(fields, map[string]any{
			"name": "Category", "value": ins.Category, "inline": true,
		})
	}
	if e.ClusterName != "" {
		fields = append(fields, map[string]any{
			"name": "Cluster", "value": fmt.Sprintf("`%s`", e.ClusterName), "inline": true,
		})
	}

	embed := map[string]any{
		"title":       title,
		"description": description,
		"color":       color,
		"fields":      fields,
		"timestamp":   ins.FirstSeen.UTC().Format("2006-01-02T15:04:05Z"),
		"footer":      map[string]any{"text": "KubeBolt"},
	}
	// Clickable title: deep link to the resource when possible, base URL otherwise.
	// Discord renders the title as a hyperlink when `url` is set.
	if e.BaseURL != "" {
		if deepLink := resourceURL(e.BaseURL, ins); deepLink != "" {
			embed["url"] = deepLink
		} else {
			embed["url"] = e.BaseURL
		}
	}

	return map[string]any{
		"username":   "KubeBolt",
		"embeds":     []map[string]any{embed},
		"avatar_url": "https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/docs/images/kubebolt-icon.png",
	}
}

// discordColorForSeverity returns the decimal RGB color for Discord embeds.
func discordColorForSeverity(severity string) int {
	switch severity {
	case "critical":
		return 0xE74C3C // red
	case "warning":
		return 0xF39C12 // amber
	case "info":
		return 0x3498DB // blue
	default:
		return 0x95A5A6 // grey
	}
}
