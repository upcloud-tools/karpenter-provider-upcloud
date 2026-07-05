{{- define "karpenter-upcloud.secretName" -}}
{{- if .Values.existingSecret }}
{{- .Values.existingSecret }}
{{- else }}
{{- include "karpenter-upcloud.fullname" . }}
{{- end }}
{{- end }}
