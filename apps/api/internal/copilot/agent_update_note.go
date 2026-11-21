package copilot

// AgentUpdateNote marks, in list_clusters, a cluster whose agent is behind
// the newest release (agentUpdate.level "available" or "recommended", debt-24).
// Asked whether an agent is out of date, Kobi used to have no idea: the row
// carried agentUpdate, but nothing said what to do with it.
const AgentUpdateNote = "Its agent is out of date (agentUpdate). KubeBolt never upgrades the agent: give the person the " +
	"agentUpdate.upgrade command to run against that cluster, and the reasons when the level is \"recommended\". " +
	"Installed with another release name or namespace: they change kubebolt-agent and kubebolt-system. With ArgoCD " +
	"or Flux: they set the chart version to agentUpdate.latest instead."

// AgentVersionUnknownNote is added when the agent does not report its own
// version (agentUpdate.before): agents before 1.4.4 all send 1.0.0.
const AgentVersionUnknownNote = "This agent does not report its own version: every agent before 1.4.4 sends 1.0.0, " +
	"so the exact release is not known here. The Helm release kubebolt-agent in that cluster has it (Applications)."
