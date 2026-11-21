// Package agentversion tells whether a cluster's agent is out of date and why.
//
// KubeBolt never upgrades an agent itself: it is the customer's Helm release,
// often managed by GitOps, and upgrading it from here would take write access
// to its DaemonSet that KubeBolt does not need otherwise. What it does is say
// so, with the reason and the command (debt-24, decision d56). Each agent
// sends its version in its Hello; the cluster is judged by its oldest live
// agent, because that one sets what the cluster ships.
package agentversion

import (
	"strings"

	"golang.org/x/mod/semver"
)

// Latest is the newest agent release (the agent chart's appVersion, tag
// agent-v<Latest>). Bump it with every agent release:
// TestLatestMatchesTheAgentChart fails until it matches the chart.
const Latest = "1.4.9"

// Advisory is a reason to upgrade now: what an agent older than Below gets
// wrong, in words for the person who runs it.
type Advisory struct {
	Below  string
	Reason string
}

// Advisories, oldest fix first. An agent below any of them is «update
// recommended»; one that is only behind Latest is «update available».
var Advisories = []Advisory{
	{
		Below:  "1.4.8",
		Reason: "It stores each pod's network traffic twice and, for hostNetwork pods, every interface of the node: up to three times the series, and pod network charts read double.",
	},
	{
		Below:  "1.4.9",
		Reason: "With Hubble flows on, it never forgets a flow key while its pod runs: active series only grow, and the traffic map draws connections that ended long ago.",
	},
}

// Level says how far behind an agent is.
const (
	LevelCurrent     = "current"
	LevelAvailable   = "available"
	LevelRecommended = "recommended"
)

// Status is a cluster's agent version against the newest release.
type Status struct {
	// Version is the oldest version among the cluster's live agents.
	Version string `json:"version"`
	Latest  string `json:"latest"`
	Level   string `json:"level"`
	// Reasons apply when Level is recommended: what this version gets wrong.
	Reasons []string `json:"reasons,omitempty"`
}

// Evaluate judges a cluster by the versions its live agents declared. nil
// when none declared a release version: a development build ("dev") or an
// agent too old to send one says nothing about what is out of date.
func Evaluate(versions []string) *Status {
	oldest := ""
	for _, v := range versions {
		c := canonical(v)
		if c == "" {
			continue
		}
		if oldest == "" || semver.Compare(c, oldest) < 0 {
			oldest = c
		}
	}
	if oldest == "" {
		return nil
	}
	st := &Status{Version: strings.TrimPrefix(oldest, "v"), Latest: Latest, Level: LevelCurrent}
	if semver.Compare(oldest, canonical(Latest)) < 0 {
		st.Level = LevelAvailable
	}
	for _, a := range Advisories {
		if semver.Compare(oldest, canonical(a.Below)) < 0 {
			st.Level = LevelRecommended
			st.Reasons = append(st.Reasons, a.Reason)
		}
	}
	return st
}

// canonical returns a version as semver wants it ("v1.4.9"), or "" when it is
// not a release version. Pre-releases count as their release's predecessor,
// as semver orders them.
func canonical(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if v[0] != 'v' {
		v = "v" + v
	}
	if !semver.IsValid(v) {
		return ""
	}
	return v
}
