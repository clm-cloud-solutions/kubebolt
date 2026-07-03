package settings

import "encoding/json"

// agent_ingest_url is a PLATFORM-level setting: the external address remote
// agents dial to reach this backend's gRPC ingest (host:port, e.g.
// agent.kubebolt.io:443). One URL per install — NOT per-org. Env baseline
// (KUBEBOLT_AGENT_INGEST_URL) + an install-global platform-admin override (only
// platform admins may edit it), mirroring notifications_base_url.
//
// When non-empty it signals a HOSTED deployment: the add-cluster wizard uses it
// as the sole backendUrl default and switches to SaaS-mode (TLS on, auth
// required, the operator creates the token secret in their OWN cluster rather
// than the wizard materializing it in the currently-connected one).
const agentIngestURLKey = "agent_ingest_url"

type storedAgentIngestURL struct {
	URL string `json:"url"`
}

// agentIngestURLOverride returns the platform-admin override, or "" if none.
func (r *Runtime) agentIngestURLOverride() string {
	raw, err := r.store.GetSetting(agentIngestURLKey)
	if err != nil || len(raw) == 0 {
		return ""
	}
	var s storedAgentIngestURL
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s.URL
}

// AgentIngestURLEnvDefault is the env baseline (KUBEBOLT_AGENT_INGEST_URL).
func (r *Runtime) AgentIngestURLEnvDefault() string {
	return r.envIngestChannel.AgentIngestURL
}

// AgentIngestURL is the effective value: platform override ?? env baseline.
// Non-empty means "hosted deployment" to the add-cluster wizard.
func (r *Runtime) AgentIngestURL() string {
	if ov := r.agentIngestURLOverride(); ov != "" {
		return ov
	}
	return r.envIngestChannel.AgentIngestURL
}

// PutAgentIngestURL stores the platform-admin override. An empty value clears it
// (falls back to the env baseline). Platform-admin only (gated at the route).
func (r *Runtime) PutAgentIngestURL(url string) error {
	if url == "" {
		return r.store.SetSetting(agentIngestURLKey, []byte("null"))
	}
	encoded, err := json.Marshal(storedAgentIngestURL{URL: url})
	if err != nil {
		return err
	}
	return r.store.SetSetting(agentIngestURLKey, encoded)
}
