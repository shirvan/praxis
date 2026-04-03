{{/*
Chart name.
*/}}
{{- define "praxis.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Fully qualified app name.
*/}}
{{- define "praxis.fullname" -}}
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
Chart version label.
*/}}
{{- define "praxis.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "praxis.labels" -}}
helm.sh/chart: {{ include "praxis.chart" . }}
app.kubernetes.io/part-of: praxis
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Selector labels for a component.
Usage: {{ include "praxis.selectorLabels" (dict "root" . "component" "core") }}
*/}}
{{- define "praxis.selectorLabels" -}}
app.kubernetes.io/name: {{ include "praxis.fullname" .root }}-{{ .component }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
{{- end }}

{{/*
Resolve the Restate ingress URL (internal service or external).
*/}}
{{- define "praxis.restateIngressUrl" -}}
{{- if .Values.restate.enabled -}}
http://{{ include "praxis.fullname" . }}-restate.{{ .Release.Namespace }}:8080
{{- else -}}
{{- required "restate.external.ingressUrl is required when restate.enabled=false" .Values.restate.external.ingressUrl -}}
{{- end -}}
{{- end }}

{{/*
Resolve the Restate admin URL (internal service or external).
*/}}
{{- define "praxis.restateAdminUrl" -}}
{{- if .Values.restate.enabled -}}
http://{{ include "praxis.fullname" . }}-restate.{{ .Release.Namespace }}:9070
{{- else -}}
{{- required "restate.external.adminUrl is required when restate.enabled=false" .Values.restate.external.adminUrl -}}
{{- end -}}
{{- end }}

{{/*
Build the image reference for a component.
Usage: {{ include "praxis.image" (dict "component" "core" "image" .Values.core.image "global" .Values.global) }}
*/}}
{{- define "praxis.image" -}}
{{- $tag := default .global.imageTag .image.tag -}}
{{- if .image.repository -}}
{{- printf "%s:%s" .image.repository $tag -}}
{{- else -}}
{{- printf "%s/praxis-%s:%s" .global.imageRegistry .component $tag -}}
{{- end -}}
{{- end }}

{{/*
Image pull secrets.
*/}}
{{- define "praxis.imagePullSecrets" -}}
{{- with .Values.global.imagePullSecrets }}
imagePullSecrets:
  {{- toYaml . | nindent 2 }}
{{- end }}
{{- end }}
