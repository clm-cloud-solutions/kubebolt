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
