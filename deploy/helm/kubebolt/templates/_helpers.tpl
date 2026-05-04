{{/*
Expand the name of the chart.
*/}}
{{- define "kubebolt.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "kubebolt.fullname" -}}
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
Common labels
*/}}
{{- define "kubebolt.labels" -}}
helm.sh/chart: {{ include "kubebolt.name" . }}-{{ .Chart.Version }}
{{ include "kubebolt.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "kubebolt.selectorLabels" -}}
app.kubernetes.io/name: {{ include "kubebolt.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Service account name
*/}}
{{- define "kubebolt.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "kubebolt.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Embedded VictoriaMetrics workload name (StatefulSet + Service share it).
*/}}
{{- define "kubebolt.victoriametrics.fullname" -}}
{{- printf "%s-victoriametrics" (include "kubebolt.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
URL the API uses to write samples and run PromQL. Resolves to the
in-cluster embedded VictoriaMetrics Service when embedded.enabled is
true; otherwise to the user-provided externalUrl. Fails template
rendering if neither is set so misconfigurations surface at install
time, not at first sample write.
*/}}
{{- define "kubebolt.metricsStorageUrl" -}}
{{- if .Values.metrics.storage.embedded.enabled -}}
http://{{ include "kubebolt.victoriametrics.fullname" . }}:8428
{{- else if .Values.metrics.storage.externalUrl -}}
{{ .Values.metrics.storage.externalUrl }}
{{- else -}}
{{- fail "metrics.storage.embedded.enabled is false but metrics.storage.externalUrl is empty — set one of them" -}}
{{- end -}}
{{- end }}
