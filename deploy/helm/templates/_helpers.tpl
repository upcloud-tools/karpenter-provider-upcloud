{{- define "karpenter-upcloud.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{- define "karpenter-upcloud.fullname" -}}
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

{{- define "karpenter-upcloud.labels" -}}
helm.sh/chart: {{ include "karpenter-upcloud.name" . }}-{{ .Chart.Version | replace "+" "_" }}
{{ include "karpenter-upcloud.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.commonLabels }}
{{ tpl (toYaml .) $ }}
{{- end }}
{{- end }}

{{- define "karpenter-upcloud.selectorLabels" -}}
app.kubernetes.io/name: {{ include "karpenter-upcloud.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{- define "karpenter-upcloud.image" -}}
{{- $repository := .Values.image.repository -}}
{{- $tag := .Values.image.tag | default .Chart.AppVersion -}}
{{- printf "%s:%s" $repository $tag -}}
{{- end }}

{{- define "karpenter-upcloud.secretName" -}}
{{- if .Values.config.auth.existingSecret }}
{{- .Values.config.auth.existingSecret }}
{{- else }}
{{- include "karpenter-upcloud.fullname" . }}
{{- end }}
{{- end }}
