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
const Latest = "1.4.11"

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

// unreportedVersion is what every agent before reportsVersionSince sends in
// its Hello, whatever its release: until 2026-10-12 the version was a
// constant in the agent (commit 7bac2c36). An agent saying 1.0.0 is
// therefore some release before reportsVersionSince, and nothing more is
// known from here: the installed chart (the Helm release) has the exact one.
const (
	unreportedVersion   = "1.0.0"
	reportsVersionSince = "1.4.4"
)

// The Helm release the install wizard creates.
const (
	AgentChart     = "oci://ghcr.io/clm-cloud-solutions/kubebolt/helm/kubebolt-agent"
	AgentRelease   = "kubebolt-agent"
	AgentNamespace = "kubebolt-system"
)

// UpgradeCommand is the Helm command that moves the agent to a release,
// keeping the person's settings and taking the new chart's defaults
// (--reset-then-reuse-values, Helm 3.14 or later). It names the release the
// install wizard creates; UpgradeCommandFor names the one a cluster has.
func UpgradeCommand(version string) string {
	return UpgradeCommandFor(version, Install{})
}

// Install is how the agent was installed in its cluster, read from the
// cluster itself: the release its DaemonSet (or promread Deployment) was
// created by (`app.kubernetes.io/instance`) and its namespace. Seen live on
// 2026-11-21: Production AKS's release is kb-agent-promread, yagan's
// kubebolt-agent-ee in namespace kubebolt-agent — the wizard's default names
// fit neither, and the command could not be pasted as it was.
type Install struct {
	Release   string
	Namespace string
	// TwoCostSources: promRead.cost and the agent's OpenCost exporter are both
	// on. The chart refuses that from 1.4.10 (every cost family was stored
	// twice), so the command turns promRead.cost off; the exporter keeps
	// reading OpenCost directly.
	TwoCostSources bool
}

// costSourcesRuleSince is the agent release whose chart refuses two sources
// of cost.
const costSourcesRuleSince = "1.4.10"

// UpgradeCommandFor is UpgradeCommand for the release and namespace a
// cluster's agent was installed with; an empty field keeps the default.
func UpgradeCommandFor(version string, in Install) string {
	release, namespace := AgentRelease, AgentNamespace
	if in.Release != "" {
		release = in.Release
	}
	if in.Namespace != "" {
		namespace = in.Namespace
	}
	cmd := "helm upgrade " + release + " " + AgentChart + " \\\n" +
		"  --namespace " + namespace + " --version " + version + " --reset-then-reuse-values"
	if in.TwoCostSources && semver.Compare(canonical(version), canonical(costSourcesRuleSince)) >= 0 {
		cmd += " \\\n  --set agent.promRead.cost.enabled=false"
	}
	return cmd
}

// Status is a cluster's agent version against the newest release.
type Status struct {
	// Version is the oldest version among the cluster's live agents. Empty
	// when that agent does not report its own: Before then says what is
	// known.
	Version string `json:"version,omitempty"`
	// Before is set when Version is unknown: the agent is a release older
	// than this one (agents before 1.4.4 all report 1.0.0).
	Before string `json:"before,omitempty"`
	Latest string `json:"latest"`
	Level  string `json:"level"`
	// Reasons apply when Level is recommended: what this version gets wrong.
	Reasons []string `json:"reasons,omitempty"`
	// Upgrade is the Helm command to the latest release, when Level is not
	// current. KubeBolt never runs it: the person who owns the release does.
	Upgrade string `json:"upgrade,omitempty"`
	// Release and Namespace are the agent's own, read from the cluster, when
	// it could be read (ForInstall); empty, Upgrade names the defaults.
	Release   string `json:"release,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// TurnsOffPromReadCost: the install reads OpenCost twice and Upgrade
	// turns promRead.cost off, as the new chart requires.
	TurnsOffPromReadCost bool `json:"turnsOffPromReadCost,omitempty"`
}

// ForInstall fits the status to how the agent was installed: the command
// names its release and namespace, and turns promRead.cost off when the
// install has two sources of cost. A nil install or a current agent leaves it
// as it is.
func (s *Status) ForInstall(in *Install) *Status {
	if s == nil || in == nil || s.Level == LevelCurrent {
		return s
	}
	s.Release, s.Namespace = in.Release, in.Namespace
	s.Upgrade = UpgradeCommandFor(s.Latest, *in)
	s.TurnsOffPromReadCost = strings.Contains(s.Upgrade, "agent.promRead.cost.enabled=false")
	return s
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
	if oldest == canonical(unreportedVersion) {
		st.Version, st.Before = "", reportsVersionSince
	}
	if semver.Compare(oldest, canonical(Latest)) < 0 {
		st.Level = LevelAvailable
	}
	for _, a := range Advisories {
		if semver.Compare(oldest, canonical(a.Below)) < 0 {
			st.Level = LevelRecommended
			st.Reasons = append(st.Reasons, a.Reason)
		}
	}
	if st.Level != LevelCurrent {
		st.Upgrade = UpgradeCommand(Latest)
	}
	return st
}

// Label names the agent's version for a person: "1.4.8", or "before 1.4.4"
// when it does not report its own.
func (s *Status) Label() string {
	if s.Version == "" && s.Before != "" {
		return "before " + s.Before
	}
	return s.Version
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
