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
  # events: only for a cluster that does not serve events.k8s.io (the same Events, older API).
  resources: [pods, events]
  verbs: [list, watch]
- apiGroups: [apps]
  resources: [deployments, replicasets, statefulsets, daemonsets, controllerrevisions]
  verbs: [list, watch]
# Kubernetes Events (evidence) and autoscalers (scaling context). Read only.
- apiGroups: [events.k8s.io]
  resources: [events]
  verbs: [list, watch]
- apiGroups: [autoscaling]
  resources: [horizontalpodautoscalers]
  verbs: [list, watch]
{{- end -}}

{{/* Name of the Secret that holds the token. */}}
{{- define "opstide.authSecretName" -}}
{{- default (printf "%s-auth" (include "opstide.fullname" .)) .Values.auth.existingSecret -}}
{{- end }}

{{/*
Fingerprint of the token, for the pod annotation, so a changed token restarts the pod (it is read
at start). Only for your own Secret: a generated one never changes, and an annotation that changed
after the first upgrade would restart the pod, which is a gap in the record, for nothing.
`helm template` cannot look the Secret up and gets a fixed value.
*/}}
{{- define "opstide.tokenChecksum" -}}
{{- $s := lookup "v1" "Secret" .Release.Namespace (include "opstide.authSecretName" .) -}}
{{- if and .Values.auth.existingSecret $s $s.data (hasKey $s.data "token") -}}
{{- index $s.data "token" | sha256sum -}}
{{- else -}}
{{- "generated-or-unknown" | sha256sum -}}
{{- end -}}
{{- end }}
