package notifications

// severityIconAndColor returns the emoji icon and a channel-specific color for
// the given severity. Colors are:
//   - Slack: hex string (e.g. "#E74C3C")
//   - Discord: ignored (see discordColorForSeverity which returns an int)
//
// Supported severities: critical, warning, info. Unknown values get neutral styling.
func severityIconAndColor(severity, channel string) (icon, color string) {
	switch severity {
	case "critical":
		icon = "🔴"
		color = "#E74C3C"
	case "warning":
		icon = "🟠"
		color = "#F39C12"
	case "info":
		icon = "🔵"
		color = "#3498DB"
	default:
		icon = "⚪"
		color = "#95A5A6"
	}
	_ = channel // reserved for future per-channel customization
	return
}
