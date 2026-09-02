{{/* vim: set filetype=mustache: */}}
{{/*
Helpers of the kserve umbrella component (templates/kserve/): the switch of
the kserve-crd, kserve-resources and kserve-llmisvc-resources dependencies.
Hand-authored: listed in curate.yaml templates.extra, never generated.
*/}}

{{/*
Truthy when the KServe control plane is on (components.kserve.enabled).
*/}}
{{- define "agent-platform-standalone.kserve.enabled" -}}
{{- include "agent-platform-standalone.componentEnabled" (dict "root" . "name" "kserve") -}}
{{- end -}}

{{/*
Truthy when the llm-d control plane is on: components.kserve.llmisvc.enabled
with the kserve component on (the guard fails the other combination).
*/}}
{{- define "agent-platform-standalone.kserve.llmisvcEnabled" -}}
{{- if and (include "agent-platform-standalone.kserve.enabled" .) .Values.components.kserve.llmisvc.enabled -}}true{{- end -}}
{{- end -}}

{{/*
Truthy when the cluster has the KServe serving APIs (ClusterServingRuntime,
InferenceService). Helm fills .Capabilities.APIVersions from the cluster on
install/upgrade; an offline `helm template` has to be told with --api-versions.
*/}}
{{- define "agent-platform-standalone.kserve.apiPresent" -}}
{{- if and (.Capabilities.APIVersions.Has "serving.kserve.io/v1alpha1") (.Capabilities.APIVersions.Has "serving.kserve.io/v1beta1") -}}true{{- end -}}
{{- end -}}

{{/*
The dependency values blocks (dicts; empty when unset).
*/}}
{{- define "agent-platform-standalone.kserve.resourcesValues" -}}
{{- index .Values "kserve-resources" | default dict | toJson -}}
{{- end -}}
{{- define "agent-platform-standalone.kserve.llmisvcValues" -}}
{{- index .Values "kserve-llmisvc-resources" | default dict | toJson -}}
{{- end -}}

{{/*
Labels of every object the component renders.
*/}}
{{- define "agent-platform-standalone.kserve.labels" -}}
{{ include "agent-platform-standalone.labels.common" . }}
app.kubernetes.io/component: kserve
{{- end -}}
