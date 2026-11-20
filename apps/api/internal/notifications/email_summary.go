package notifications

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// The summary email: one message that counts and classifies many insights
// instead of listing them — a burst from one cluster, or a digest too long to
// list. summary.go computes every figure and sentence; this file lays them out
// in the same frame as the per-insight email (buildBodies).

// summaryEmail renders the subject, HTML and text of a summary.
func summaryEmail(v summaryView) (subject, htmlBody, textBody string) {
	subject = summarySubject(v)

	eyebrow := "Burst summary · " + v.Cluster
	switch {
	case v.Digest != "":
		eyebrow = strings.ToUpper(string(v.Digest)[:1]) + string(v.Digest)[1:] + " digest"
	case v.ResolvedBurst:
		eyebrow = "Resolved · " + v.Cluster
	}
	if v.Partial {
		eyebrow += " · still going"
	}

	data := summaryEmailData{
		Subject:      subject,
		BannerBg:     summaryBanner(v),
		Eyebrow:      eyebrow,
		Headline:     v.headline(),
		SeverityLine: v.severityLine(),
		Lead:         v.Lead,
		Stats: []summaryStat{
			{"Critical", v.Counts["critical"], "#b91c1c"},
			{"Warning", v.Counts["warning"], "#92400e"},
			{"Info", v.Counts["info"], "#1e40af"},
		},
		MoreCritical: v.MoreCritical,
		InsightsURL:  v.InsightsURL,
		WhyLabel:     "Why one email:",
		Why:          v.Why,
		Timestamp:    time.Now().UTC().Format("2006-01-02 15:04 UTC"),
	}
	if v.Digest != "" {
		data.WhyLabel = "Why a summary:"
	}
	if len(v.Critical) > 0 {
		total := len(v.Critical) + v.MoreCritical
		data.CriticalLabel = fmt.Sprintf("Critical · %d — listed one by one", total)
		if v.ResolvedBurst {
			data.CriticalLabel = fmt.Sprintf("Critical · %d — resolved", total)
		}
	}
	for _, it := range v.Critical {
		row := summaryCriticalRow{
			Title:         plainTitle(it.Event),
			Resource:      it.Insight.Resource,
			Message:       clipRunes(it.Insight.Message, 160),
			URL:           resourceURL(it.BaseURL, it.Insight),
			AlsoSentAlone: it.AlsoSentAlone,
		}
		if v.Digest != "" {
			row.Cluster = it.ClusterName
		}
		data.Critical = append(data.Critical, row)
	}
	for _, sec := range v.Sections {
		label := "All of it, by rule"
		if v.Digest != "" {
			label = fmt.Sprintf("%s · %d", sec.Cluster, sec.Total)
		}
		out := summaryEmailSection{Label: label}
		for _, r := range sec.Rules {
			state := strings.ToUpper(r.Severity)
			bg, fg := severityBadgeColors(r.Severity)
			if r.Resolved {
				state, bg, fg = "RESOLVED", "#dcfce7", "#166534"
			}
			out.Rules = append(out.Rules, summaryEmailRule{
				Count: r.Count, Title: r.Title, State: state, BadgeBg: bg, BadgeFg: fg, Where: r.namespacesText(),
			})
		}
		data.Sections = append(data.Sections, out)
	}
	data.CTA = "Open Insights in KubeBolt"
	if v.Digest == "" {
		data.CTA = fmt.Sprintf("Open the %d insights in KubeBolt", v.Total)
	}

	var buf bytes.Buffer
	_ = summaryTemplate.Execute(&buf, data)
	return subject, buf.String(), summaryText(v)
}

func summarySubject(v summaryView) string {
	if v.Digest != "" {
		title := strings.ToUpper(string(v.Digest)[:1]) + string(v.Digest)[1:]
		breakdown := strings.ReplaceAll(v.severityLine(), " · ", ", ")
		if breakdown == "" {
			return fmt.Sprintf("[KubeBolt] %s digest — %d insights", title, v.Total)
		}
		return fmt.Sprintf("[KubeBolt] %s digest — %d insights (%s)", title, v.Total, breakdown)
	}
	subject := "[KubeBolt] " + v.headline()
	if n := v.Counts["critical"]; n > 0 && !v.ResolvedBurst {
		subject += fmt.Sprintf(" — %d critical", n)
	}
	if v.Partial {
		subject += " (still going)"
	}
	return subject
}

// summaryBanner is the per-insight email's banner, by the summary's most
// severe open insight; green for a burst of resolutions.
func summaryBanner(v summaryView) template.CSS {
	switch {
	case v.ResolvedBurst:
		return template.CSS("linear-gradient(135deg,#16a34a,#22c55e)")
	case v.Counts["critical"] > 0:
		return template.CSS("linear-gradient(135deg,#dc2626,#ef4444)")
	case v.Counts["warning"] > 0:
		return template.CSS("linear-gradient(135deg,#ea580c,#f59e0b)")
	default:
		return template.CSS("linear-gradient(135deg,#2563eb,#3b82f6)")
	}
}

// clipRunes cuts s to max runes, with an ellipsis.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// summaryText is the plain-text alternative: the same counts, no cards.
func summaryText(v summaryView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", v.headline())
	if line := v.severityLine(); line != "" {
		fmt.Fprintf(&b, "%s\n", line)
	}
	if v.Lead != "" {
		fmt.Fprintf(&b, "\n%s\n", v.Lead)
	}
	if len(v.Critical) > 0 {
		b.WriteString("\nCritical:\n")
		for _, it := range v.Critical {
			fmt.Fprintf(&b, "- %s: %s", plainTitle(it.Event), it.Insight.Resource)
			if v.Digest != "" && it.ClusterName != "" {
				fmt.Fprintf(&b, " (cluster %s)", it.ClusterName)
			}
			if it.AlsoSentAlone {
				b.WriteString(" [also sent on its own]")
			}
			b.WriteString("\n")
			if url := resourceURL(it.BaseURL, it.Insight); url != "" {
				fmt.Fprintf(&b, "  %s\n", url)
			}
		}
		if v.MoreCritical > 0 {
			fmt.Fprintf(&b, "- and %d more critical\n", v.MoreCritical)
		}
	}
	for _, sec := range v.Sections {
		if v.Digest != "" {
			fmt.Fprintf(&b, "\n%s (%d):\n", sec.Cluster, sec.Total)
		} else {
			b.WriteString("\nBy rule:\n")
		}
		for _, r := range sec.Rules {
			state := strings.ToLower(r.Severity)
			if r.Resolved {
				state = "resolved"
			}
			fmt.Fprintf(&b, "%6d  %s (%s)", r.Count, r.Title, state)
			if where := r.namespacesText(); where != "" {
				fmt.Fprintf(&b, " — %s", where)
			}
			b.WriteString("\n")
		}
	}
	if v.InsightsURL != "" {
		fmt.Fprintf(&b, "\nOpen: %s\n", v.InsightsURL)
	}
	if v.Why != "" {
		fmt.Fprintf(&b, "\n%s\n", v.Why)
	}
	b.WriteString("\nKubeBolt · AI operations platform for Kubernetes · https://kubebolt.io\n")
	return b.String()
}

type summaryStat struct {
	Label string
	Count int
	Color string
}

type summaryCriticalRow struct {
	Title, Resource, Cluster, Message, URL string
	AlsoSentAlone                          bool
}

type summaryEmailRule struct {
	Count                                 int
	Title, State, BadgeBg, BadgeFg, Where string
}

type summaryEmailSection struct {
	Label string
	Rules []summaryEmailRule
}

type summaryEmailData struct {
	Subject       string
	BannerBg      template.CSS
	Eyebrow       string
	Headline      string
	SeverityLine  string
	Lead          string
	Stats         []summaryStat
	CriticalLabel string
	Critical      []summaryCriticalRow
	MoreCritical  int
	Sections      []summaryEmailSection
	InsightsURL   string
	CTA           string
	WhyLabel      string
	Why           string
	Timestamp     string
}

// summaryTemplate shares the per-insight email's frame (htmlTemplate): banner,
// white card, footer. Table-based where layout matters, for mail clients.
var summaryTemplate = template.Must(template.New("summary").Parse(`<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>{{.Subject}}</title></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f5f5f5;margin:0;padding:24px;color:#1a1a1a;">
<div style="max-width:640px;margin:0 auto;background:#fff;border-radius:12px;overflow:hidden;box-shadow:0 2px 8px rgba(0,0,0,0.08);">
  <div style="padding:20px 24px;background:{{.BannerBg}};color:#fff;">
    <div style="font-size:12px;text-transform:uppercase;letter-spacing:0.1em;opacity:0.85;">KubeBolt · {{.Eyebrow}}</div>
    <div style="font-size:20px;font-weight:700;margin-top:4px;">{{.Headline}}</div>
    {{if .SeverityLine}}<div style="font-size:13px;margin-top:4px;opacity:0.9;">{{.SeverityLine}}</div>{{end}}
  </div>
  <div style="padding:20px 24px 8px;">
    {{if .Lead}}<p style="font-size:14px;line-height:1.6;color:#333;margin:0 0 16px;">{{.Lead}}</p>{{end}}
    <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="margin:0 0 8px;">
      <tr>
        {{range $i, $s := .Stats}}{{if $i}}<td width="8" style="font-size:0;line-height:0;">&nbsp;</td>{{end}}
        <td width="33%" style="padding:12px 14px;background:#fafafa;border-radius:8px;border-left:3px solid {{$s.Color}};">
          <div style="font-size:24px;font-weight:700;color:#1a1a1a;line-height:1;">{{$s.Count}}</div>
          <div style="font-size:10px;text-transform:uppercase;letter-spacing:0.1em;color:{{$s.Color}};margin-top:6px;">{{$s.Label}}</div>
        </td>{{end}}
      </tr>
    </table>
  </div>
  {{if .Critical}}
  <div style="padding:8px 24px 0;">
    <div style="font-size:11px;text-transform:uppercase;letter-spacing:0.1em;font-weight:600;color:#b91c1c;margin:0 0 8px;">{{.CriticalLabel}}</div>
    {{range .Critical}}
    <div style="padding:12px 14px;margin:0 0 8px;border:1px solid #eee;border-left:3px solid #dc2626;border-radius:8px;">
      <div style="font-size:14px;font-weight:600;color:#1a1a1a;">{{.Title}}{{if .AlsoSentAlone}} <span style="font-size:10px;text-transform:uppercase;letter-spacing:0.08em;color:#888;border:1px solid #ddd;border-radius:4px;padding:1px 5px;margin-left:6px;">also sent on its own</span>{{end}}</div>
      <div style="font-size:12px;color:#666;margin:4px 0;"><span style="font-family:monospace;background:#f5f5f5;padding:1px 6px;border-radius:3px;">{{.Resource}}</span>{{if .Cluster}}<span style="margin-left:8px;">on <strong>{{.Cluster}}</strong></span>{{end}}</div>
      {{if .Message}}<div style="font-size:13px;color:#333;line-height:1.5;">{{.Message}}</div>{{end}}
      {{if .URL}}<div style="margin-top:6px;"><a href="{{.URL}}" style="font-size:12px;color:#1DBD7D;text-decoration:none;">Open in KubeBolt →</a></div>{{end}}
    </div>
    {{end}}
    {{if .MoreCritical}}<div style="font-size:12px;color:#666;margin:0 0 8px;">+ {{.MoreCritical}} more critical{{if .InsightsURL}} — <a href="{{.InsightsURL}}" style="color:#1DBD7D;text-decoration:none;">open them in KubeBolt →</a>{{end}}</div>{{end}}
  </div>
  {{end}}
  {{range .Sections}}
  <div style="padding:8px 24px 0;">
    <div style="font-size:11px;text-transform:uppercase;letter-spacing:0.1em;font-weight:600;color:#666;margin:8px 0;">{{.Label}}</div>
    <table role="presentation" cellpadding="0" cellspacing="0" border="0" width="100%" style="border:1px solid #eee;border-radius:8px;">
      {{range $i, $r := .Rules}}
      <tr>
        <td width="56" style="padding:12px 0 12px 14px;vertical-align:top;{{if $i}}border-top:1px solid #eee;{{end}}">
          <div style="font-size:18px;font-weight:700;color:#1a1a1a;">{{$r.Count}}</div>
        </td>
        <td style="padding:12px 14px 12px 8px;vertical-align:top;{{if $i}}border-top:1px solid #eee;{{end}}">
          <div style="font-size:14px;font-weight:600;color:#1a1a1a;">{{$r.Title}} <span style="display:inline-block;padding:1px 8px;border-radius:999px;font-size:10px;font-weight:600;letter-spacing:0.05em;background:{{$r.BadgeBg}};color:{{$r.BadgeFg}};margin-left:4px;">{{$r.State}}</span></div>
          {{if $r.Where}}<div style="font-size:12px;color:#666;margin-top:4px;line-height:1.5;">{{$r.Where}}</div>{{end}}
        </td>
      </tr>
      {{end}}
    </table>
  </div>
  {{end}}
  {{if .InsightsURL}}
  <div style="padding:16px 24px 0;">
    <a href="{{.InsightsURL}}" style="display:inline-block;padding:10px 16px;background:#1DBD7D;color:#fff;text-decoration:none;border-radius:6px;font-size:13px;font-weight:500;">{{.CTA}} →</a>
  </div>
  {{end}}
  <div style="padding:16px 24px 20px;">
    <p style="font-size:12px;line-height:1.6;color:#666;margin:0;"><strong style="color:#333;">{{.WhyLabel}}</strong> {{.Why}}</p>
  </div>
  <div style="padding:18px 24px;background:#fafafa;font-size:11px;color:#999;text-align:center;border-top:1px solid #eee;line-height:1.7;">
    <div style="font-size:13px;font-weight:700;color:#1a1a1a;letter-spacing:-0.01em;">KubeBolt</div>
    <div style="color:#888;">AI operations platform for Kubernetes · <a href="https://kubebolt.io" style="color:#1DBD7D;text-decoration:none;">kubebolt.io</a></div>
    <div style="margin-top:4px;">Sent {{.Timestamp}}</div>
  </div>
</div>
</body></html>`))
