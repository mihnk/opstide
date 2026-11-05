{{- define "opstide.name" -}}
{{- .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "opstide.fullname" -}}
{{- if contains .Chart.Name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "opstide.selectorLabels" -}}
app.kubernetes.io/name: {{ include "opstide.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "opstide.labels" -}}
{{ include "opstide.selectorLabels" . }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end -}}

{{- define "opstide.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "opstide.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- if and .Values.rbac.create (not .Values.serviceAccount.name) -}}
{{- fail "serviceAccount.create=false with rbac.create=true needs serviceAccount.name; otherwise the permissions would go to the namespace's default ServiceAccount" -}}
{{- end -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/* Cluster-scoped names include the namespace, so two releases do not collide. */}}
{{- define "opstide.clusterRoleName" -}}
{{- printf "%s-%s" (include "opstide.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/* What OpsTide reads. The same rules for a ClusterRole or each Role. */}}
{{- define "opstide.rules" -}}
- apiGroups: [batch]
  resources: [cronjobs, jobs]
  verbs: [list, watch]
- apiGroups: [""]
  resources: [pods]
  verbs: [list, watch]
- apiGroups: [apps]
  resources: [deployments]
  verbs: [list, watch]
{{- end -}}
