{{/*
Expand the name of the chart.
Usage: {{ include "no-latest-tag-webhook.name" . }}
*/}}
{{- define "no-latest-tag-webhook.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a fully qualified app name.
Truncated to 63 chars. If release name already contains chart name, use release name only.
Usage: {{ include "no-latest-tag-webhook.fullname" . }}
*/}}
{{- define "no-latest-tag-webhook.fullname" -}}
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
Chart label: <name>-<version>
Usage: {{ include "no-latest-tag-webhook.chart" . }}
*/}}
{{- define "no-latest-tag-webhook.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels — applied to every resource.
Usage: {{ include "no-latest-tag-webhook.labels" . | nindent 4 }}
*/}}
{{- define "no-latest-tag-webhook.labels" -}}
helm.sh/chart: {{ include "no-latest-tag-webhook.chart" . }}
{{ include "no-latest-tag-webhook.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels — used by Deployment selector and Service selector.
Usage: {{ include "no-latest-tag-webhook.selectorLabels" . | nindent 6 }}
*/}}
{{- define "no-latest-tag-webhook.selectorLabels" -}}
app.kubernetes.io/name: {{ include "no-latest-tag-webhook.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Full image reference: repository:tag
Usage: {{ include "no-latest-tag-webhook.image" . }}
*/}}
{{- define "no-latest-tag-webhook.image" -}}
{{- printf "%s:%s" .Values.image.repository (.Values.image.tag | default .Chart.AppVersion) }}
{{- end }}

{{/*
TLS secret name — created by cert-manager Certificate or manually.
Usage: {{ include "no-latest-tag-webhook.tlsSecretName" . }}
*/}}
{{- define "no-latest-tag-webhook.tlsSecretName" -}}
{{- printf "%s-tls" (include "no-latest-tag-webhook.fullname" .) }}
{{- end }}

{{/*
ClusterIssuer name.
Usage: {{ include "no-latest-tag-webhook.clusterIssuerName" . }}
*/}}
{{- define "no-latest-tag-webhook.clusterIssuerName" -}}
{{- printf "%s-selfsigned-issuer" (include "no-latest-tag-webhook.fullname" .) }}
{{- end }}fix(helm): add missing fullname helper template
