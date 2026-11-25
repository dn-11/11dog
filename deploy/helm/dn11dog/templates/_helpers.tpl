{{- define "dn11dog.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dn11dog.fullname" -}}
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

{{- define "dn11dog.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "dn11dog.labels" -}}
helm.sh/chart: {{ include "dn11dog.chart" . }}
{{ include "dn11dog.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "dn11dog.selectorLabels" -}}
app.kubernetes.io/name: {{ include "dn11dog.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "dn11dog.natsUrl" -}}
{{- if .Values.nats.url -}}
{{- .Values.nats.url -}}
{{- else -}}
{{- printf "nats://%s-nats:4222" (include "dn11dog.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "dn11dog.secretName" -}}
{{- if .Values.auth.existingSecret -}}
{{- .Values.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-auth" (include "dn11dog.fullname" .) -}}
{{- end -}}
{{- end -}}

{{- define "dn11dog.natsAuthSecretName" -}}
{{- if .Values.nats.auth.existingSecret -}}
{{- .Values.nats.auth.existingSecret -}}
{{- else -}}
{{- printf "%s-nats-auth" (include "dn11dog.fullname" .) -}}
{{- end -}}
{{- end -}}
