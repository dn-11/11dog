{{- define "11dog.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "11dog.fullname" -}}
{{- $fullname := "" -}}
{{- if .Values.fullnameOverride -}}
{{- $fullname = .Values.fullnameOverride -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- $fullname = .Release.Name -}}
{{- else -}}
{{- $fullname = printf "%s-%s" .Release.Name $name -}}
{{- end -}}
{{- end -}}
{{- $fullname | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "11dog.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "11dog.labels" -}}
helm.sh/chart: {{ include "11dog.chart" . }}
{{ include "11dog.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "11dog.selectorLabels" -}}
app.kubernetes.io/name: {{ include "11dog.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "11dog.natsUrl" -}}
{{- if .Values.mq.natsUrl -}}
{{- .Values.mq.natsUrl -}}
{{- else -}}
{{- printf "nats://%s-nats:4222" (include "11dog.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "11dog.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-auth" (include "11dog.fullname" .) -}}
{{- end -}}
{{- end -}}

