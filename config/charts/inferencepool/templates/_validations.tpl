{{/*
common validations
*/}}
{{- define "gateway-api-inference-extension.validations.inferencepool.common" -}}
{{- if or (empty $.Values.inferencePool.modelServers) (not (hasKey $.Values.inferencePool.modelServers "matchLabels")) }}
{{- fail ".Values.inferencePool.modelServers.matchLabels is required" }}
{{- end }}
{{- end -}}
