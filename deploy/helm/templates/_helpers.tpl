{{/*
Expand the name of the chart.
*/}}
{{- define "helix-cluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "helix-cluster.fullname" -}}
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
Create chart name and version as used by the chart label.
*/}}
{{- define "helix-cluster.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "helix-cluster.labels" -}}
helm.sh/chart: {{ include "helix-cluster.chart" . }}
{{ include "helix-cluster.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "helix-cluster.selectorLabels" -}}
app.kubernetes.io/name: {{ include "helix-cluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "helix-cluster.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "helix-cluster.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Image helper
*/}}
{{- define "helix-cluster.image" -}}
{{- printf "%s/%s:%s" .Values.image.registry .imageName .Values.image.tag }}
{{- end }}

{{/*
Etcd endpoints helper
*/}}
{{- define "helix-cluster.etcdEndpoints" -}}
{{- $releaseName := .Release.Name }}
{{- $namespace := .Release.Namespace }}
{{- range $i := until (int .Values.etcd.replicas) }}
{{- if $i }},{{ end }}http://{{ $releaseName }}-etcd-{{ $i }}.{{ $releaseName }}-etcd.{{ $namespace }}.svc.cluster.local:2379
{{- end }}
{{- end }}
