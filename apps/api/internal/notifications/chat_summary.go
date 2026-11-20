package notifications

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Burst summaries for the chat channels: one Slack message or one Discord
// embed instead of one per insight. Same counts and wording as the email
// (summary.go computes them).

// SendSummary posts one Slack message for a burst.
func (s *SlackNotifier) SendSummary(ctx context.Context, sum Summary) error {
	if len(sum.Items) == 1 {
		return s.Send(ctx, sum.Items[0].Event)
	}
	return postWebhook(ctx, s.client, s.webhookURL, slackSummaryPayload(viewFromBurst(sum)), "slack")
}

// SendSummary posts one Discord embed for a burst.
func (d *DiscordNotifier) SendSummary(ctx context.Context, sum Summary) error {
	if len(sum.Items) == 1 {
		return d.Send(ctx, sum.Items[0].Event)
	}
	return postWebhook(ctx, d.client, d.webhookURL, discordSummaryPayload(viewFromBurst(sum), sum), "discord")
}

func postWebhook(ctx context.Context, client *http.Client, url string, payload any, channel string) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s payload: %w", channel, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build %s request: %w", channel, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s webhook: %w", channel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s webhook returned %d: %s", channel, resp.StatusCode, string(respBody))
	}
	return nil
}

// topSeverity is the most severe open severity in the view ("" when none).
func (v summaryView) topSeverity() string {
	for _, sev := range []string{"critical", "warning", "info"} {
		if v.Counts[sev] > 0 {
			return sev
		}
	}
	return ""
}

// slackEscape escapes the three characters Slack's mrkdwn treats as control.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// clipText cuts s to max bytes on a line boundary when it can.
func clipText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndex(cut, "\n"); i > max/2 {
		cut = cut[:i]
	}
	return cut + "\n…"
}

func slackSummaryPayload(v summaryView) map[string]any {
	icon, color := severityIconAndColor(v.topSeverity(), "slack")
	if v.ResolvedBurst {
		icon, color = "✅", "#22c55e"
	}
	head := fmt.Sprintf("%s *%s*", icon, slackEscape(v.headline()))
	if v.Partial {
		head += " _(still going)_"
	}
	if line := v.severityLine(); line != "" {
		head += "\n" + line
	}
	head += "\n" + slackEscape(v.Lead)

	blocks := []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": clipText(head, 2900)}},
	}

	if len(v.Critical) > 0 {
		title := "Critical — listed one by one"
		if v.ResolvedBurst {
			title = "Critical — resolved"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "*%s*", title)
		for _, it := range v.Critical {
			fmt.Fprintf(&b, "\n• *%s* `%s`", slackEscape(plainTitle(it.Event)), slackEscape(it.Insight.Resource))
			if url := resourceURL(it.BaseURL, it.Insight); url != "" {
				fmt.Fprintf(&b, " <%s|Open>", url)
			}
			if it.AlsoSentAlone {
				b.WriteString(" _(also sent on its own)_")
			}
		}
		if v.MoreCritical > 0 {
			fmt.Fprintf(&b, "\n• and %d more critical", v.MoreCritical)
		}
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": clipText(b.String(), 2900)}})
	}

	for _, sec := range v.Sections {
		var b strings.Builder
		b.WriteString("*By rule*")
		for _, r := range sec.Rules {
			state := r.Severity
			if r.Resolved {
				state = "resolved"
			}
			fmt.Fprintf(&b, "\n`%5d` %s · %s", r.Count, slackEscape(r.Title), state)
			if where := r.namespacesText(); where != "" {
				fmt.Fprintf(&b, " — %s", slackEscape(where))
			}
		}
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": clipText(b.String(), 2900)}})
	}

	if v.InsightsURL != "" {
		blocks = append(blocks, map[string]any{
			"type": "actions",
			"elements": []map[string]any{{
				"type":  "button",
				"text":  map[string]any{"type": "plain_text", "text": fmt.Sprintf("Open the %d insights", v.Total), "emoji": true},
				"url":   v.InsightsURL,
				"style": "primary",
			}},
		})
	}
	blocks = append(blocks, map[string]any{
		"type":     "context",
		"elements": []map[string]any{{"type": "mrkdwn", "text": slackEscape(v.Why)}},
	})

	return map[string]any{
		"text": v.headline(), // notification preview
		"attachments": []map[string]any{{
			"color":  color,
			"blocks": blocks,
		}},
	}
}

func discordSummaryPayload(v summaryView, s Summary) map[string]any {
	icon, _ := severityIconAndColor(v.topSeverity(), "discord")
	color := discordColorForSeverity(v.topSeverity())
	if v.ResolvedBurst {
		icon, color = "✅", 0x22c55e
	}
	title := icon + " " + v.headline()
	if v.Partial {
		title += " (still going)"
	}
	if len(title) > 256 {
		title = title[:253] + "..."
	}

	var b strings.Builder
	if line := v.severityLine(); line != "" {
		b.WriteString(line + "\n")
	}
	b.WriteString(v.Lead)
	if len(v.Critical) > 0 {
		label := "Critical — listed one by one"
		if v.ResolvedBurst {
			label = "Critical — resolved"
		}
		fmt.Fprintf(&b, "\n\n**%s**", label)
		for _, it := range v.Critical {
			fmt.Fprintf(&b, "\n• **%s** `%s`", plainTitle(it.Event), it.Insight.Resource)
			if url := resourceURL(it.BaseURL, it.Insight); url != "" {
				fmt.Fprintf(&b, " [Open](%s)", url)
			}
			if it.AlsoSentAlone {
				b.WriteString(" *(also sent on its own)*")
			}
		}
		if v.MoreCritical > 0 {
			fmt.Fprintf(&b, "\n• and %d more critical", v.MoreCritical)
		}
	}
	for _, sec := range v.Sections {
		b.WriteString("\n\n**By rule**")
		for _, r := range sec.Rules {
			state := r.Severity
			if r.Resolved {
				state = "resolved"
			}
			fmt.Fprintf(&b, "\n`%5d` %s · %s", r.Count, r.Title, state)
			if where := r.namespacesText(); where != "" {
				fmt.Fprintf(&b, " — %s", where)
			}
		}
	}

	why := v.Why
	if len(why) > 2000 {
		why = why[:1997] + "..."
	}
	embed := map[string]any{
		"title":       title,
		"description": clipText(b.String(), 4000),
		"color":       color,
		"footer":      map[string]any{"text": why},
		"timestamp":   s.Last.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if v.InsightsURL != "" {
		embed["url"] = v.InsightsURL
	}
	return map[string]any{
		"username":   "KubeBolt",
		"embeds":     []map[string]any{embed},
		"avatar_url": "https://raw.githubusercontent.com/clm-cloud-solutions/kubebolt/main/docs/images/kubebolt-icon.png",
	}
}
