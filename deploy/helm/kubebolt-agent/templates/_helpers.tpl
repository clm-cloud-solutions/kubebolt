{{/*
Expand the name of the chart.
*/}}
{{- define "kubebolt-agent.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name (release-prefixed unless the release name
already contains the chart name).
*/}}
{{- define "kubebolt-agent.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Common labels applied to every object.
*/}}
{{- define "kubebolt-agent.labels" -}}
helm.sh/chart: {{ include "kubebolt-agent.name" . }}-{{ .Chart.Version }}
{{ include "kubebolt-agent.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: agent
app.kubernetes.io/part-of: kubebolt
{{- end }}

{{/*
Selector labels — stable across upgrades (never include version).
*/}}
{{- define "kubebolt-agent.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubebolt-agent.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name.
*/}}
{{- define "kubebolt-agent.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubebolt-agent.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Resolved image reference. Tag falls back to Chart.appVersion when
values.image.tag is empty so users don't have to pin it manually on
every upgrade.
*/}}
{{- define "kubebolt-agent.image" -}}
{{- $tag := default .Chart.AppVersion .Values.image.tag -}}
{{- printf "%s:%s" .Values.image.repository $tag -}}
{{- end }}

{{/*
Per-job metric_relabel_configs block reused across every scrape_configs
entry in templates/configmap-vmagent.yaml. Renders nothing when the
operator hasn't configured any rules. The 8-space indent matches the
scrape_configs[] level in the rendered YAML.
*/}}
{{- define "kubebolt-agent.metricRelabelConfigs" -}}
{{- with .Values.scrape.metricRelabelConfigs }}
        metric_relabel_configs:
{{ toYaml . | indent 10 }}
{{- end -}}
{{- end -}}

{{/*
Mutual-exclusion + shape check for Mode A (scrape.enabled) vs Mode C
(agent.promRead.enabled). One agent must have a single canonical
source of samples — both on would double-emit the same metric
families and corrupt the dashboards.

Hard-fail (not warning) is intentional: silently picking one path
would surprise the operator later when their PromQL doesn't agree
with itself. Called from the top of templates/daemonset.yaml so
`helm template` aborts before rendering anything else.
*/}}
{{- define "kubebolt-agent.validatePromRead" -}}
{{- $scrapeOn := and .Values.scrape .Values.scrape.enabled -}}
{{- $promReadOn := and .Values.agent .Values.agent.promRead .Values.agent.promRead.enabled -}}
{{- if and $scrapeOn $promReadOn -}}
{{- fail "scrape.enabled=true and agent.promRead.enabled=true are mutually exclusive — pick one sample source per agent. Mode A (scrape sidecar) and Mode C (read from customer Prom) cannot both be on; the agent would double-emit the same metric families." -}}
{{- end -}}
{{- if $promReadOn -}}
{{- if not .Values.agent.promRead.url -}}
{{- fail "agent.promRead.enabled=true requires agent.promRead.url" -}}
{{- end -}}
{{- $mode := default "none" .Values.agent.promRead.auth.mode -}}
{{- if eq $mode "basicAuth" -}}
{{- if not .Values.agent.promRead.auth.basicAuthUsername -}}
{{- fail "agent.promRead.auth.mode=basicAuth requires agent.promRead.auth.basicAuthUsername" -}}
{{- end -}}
{{- else if eq $mode "bearer" -}}
{{- if not .Values.agent.promRead.auth.bearerToken -}}
{{- fail "agent.promRead.auth.mode=bearer requires agent.promRead.auth.bearerToken (use extraEnv with valueFrom.secretKeyRef for production)" -}}
{{- end -}}
{{- else if eq $mode "awsSigV4" -}}
{{- if not .Values.agent.promRead.auth.awsRegion -}}
{{- fail "agent.promRead.auth.mode=awsSigV4 requires agent.promRead.auth.awsRegion (the AMP workspace's region — endpoints are region-scoped and SigV4 signs against the region)" -}}
{{- end -}}
{{- else if and (ne $mode "none") (ne $mode "basicAuth") (ne $mode "bearer") (ne $mode "awsSigV4") (ne $mode "gcpIam") (ne $mode "azureWorkloadIdentity") -}}
{{- fail (printf "agent.promRead.auth.mode=%q not recognized. Valid: none, basicAuth, bearer, gcpIam, awsSigV4, azureWorkloadIdentity." $mode) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
GOMEMLIMIT for the Go agent container, DERIVED from its memory limit so the Go
scavenger's soft target always sits below the cgroup limit. If GOMEMLIMIT ever
lands ABOVE the limit (the old hardcoded 100MiB once anyone lowered
limits.memory), the scavenger never triggers before the OOM killer — the
crash-loop this derivation makes impossible. Pass {limit, override}: override
(.Values.gomemlimit) wins verbatim; otherwise take 90% of the limit. Handles
Mi / Gi; an unrecognised unit is passed through verbatim; an empty limit yields
"" so the caller emits no GOMEMLIMIT env.
*/}}
{{- define "kubebolt-agent.gomemlimit" -}}
{{- if .override -}}
{{- .override -}}
{{- else if .limit -}}
{{- $lim := .limit | toString -}}
{{- if hasSuffix "Mi" $lim -}}
{{- printf "%dMiB" (mulf (trimSuffix "Mi" $lim | float64) 0.9 | floor | int) -}}
{{- else if hasSuffix "Gi" $lim -}}
{{- printf "%dMiB" (mulf (mulf (trimSuffix "Gi" $lim | float64) 1024.0) 0.9 | floor | int) -}}
{{- else -}}
{{- $lim -}}
{{- end -}}
{{- end -}}
{{- end -}}
