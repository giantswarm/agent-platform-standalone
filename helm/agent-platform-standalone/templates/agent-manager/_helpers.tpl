{{/* vim: set filetype=mustache: */}}
{{/*
Helpers of the agent-manager component's umbrella wiring (templates/agent-manager/).
Hand-authored: listed in curate.yaml templates.extra, never generated.

Two values blocks feed these templates: components.agent-manager (the umbrella
wiring — route, JWT policy, network policies, guards; hyphenated, so reached
through index) and agent-manager (the dependency chart's own values: the kagent
namespace, the agent chart URL, skill repositories, Service name and port),
read here the way the model-manager templates read theirs — a dependency's
values cannot be derived at render time, so the umbrella reads what the chart
will see.
*/}}

{{/*
Truthy when the agent-manager dependency is on (components.agent-manager.enabled).
*/}}
{{- define "agent-platform-standalone.agentManager.enabled" -}}
{{- include "agent-platform-standalone.componentEnabled" (dict "root" . "name" "agent-manager") -}}
{{- end -}}

{{/*
The dependency's values block, agent-manager (a dict; empty when unset).
*/}}
{{- define "agent-platform-standalone.agentManager.chartValues" -}}
{{- index .Values "agent-manager" | default dict | toJson -}}
{{- end -}}

{{/*
The agent-manager Service name. Single source of truth: the umbrella pins
agent-manager.fullnameOverride (overlay/contract.yaml), which the sub-chart
uses verbatim for its Service, and the AgentgatewayBackend host and the
network policies target exactly that name — a misconfiguration fails the
render instead of a silent 503.
*/}}
{{- define "agent-platform-standalone.agentManager.fullname" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- required "agent-manager.fullnameOverride must be set — the umbrella's route and network policies target this exact Service name" (dig "fullnameOverride" "" $chart) -}}
{{- end -}}

{{/*
The port the agent-manager Service listens on (agent-manager.service.port, default 8080).
*/}}
{{- define "agent-platform-standalone.agentManager.servicePort" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- dig "service" "port" 8080 $chart -}}
{{- end -}}

{{/*
The namespace agent-manager creates agents in by default (agent-manager.kagent.namespace).
*/}}
{{- define "agent-platform-standalone.agentManager.kagentNamespace" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- dig "kagent" "namespace" "kagent" $chart -}}
{{- end -}}

{{/*
The host of the agent chart's OCI registry (agent-manager.agentChart.ociUrl),
the one destination the service must reach for the chart's versions and
values schema.
*/}}
{{- define "agent-platform-standalone.agentManager.registryHost" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- $url := dig "agentChart" "ociUrl" "oci://gsoci.azurecr.io/charts/giantswarm/agent" $chart -}}
{{- regexReplaceAll "^oci://([^/]+)/.*$" $url "${1}" -}}
{{- end -}}

{{/*
The public hostname of the agent-manager route: the override when set, else
agentgateway.<global.domain> — the same hostname as the kagent controller route.
*/}}
{{- define "agent-platform-standalone.agentManager.hostname" -}}
{{- $route := (index .Values.components "agent-manager").route -}}
{{- include "agent-platform-standalone.hostname" (dict "ctx" . "prefix" "agentgateway" "override" $route.hostname "key" "components.agent-manager.route.hostname") -}}
{{- end -}}

{{/*
Labels of every object the umbrella renders for the component.
*/}}
{{- define "agent-platform-standalone.agentManager.labels" -}}
{{ include "agent-platform-standalone.labels.common" . }}
app.kubernetes.io/component: agent-manager
{{- end -}}

{{/*
The selector labels of the agent-manager pods, as the sub-chart stamps them
(app.kubernetes.io/name from its chart name or nameOverride, instance = the
release name Helm hands the sub-chart, which is this release's).
Rendered as YAML mapping entries; the caller provides the indentation.
*/}}
{{- define "agent-platform-standalone.agentManager.podSelector" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
app.kubernetes.io/name: {{ dig "nameOverride" "" $chart | default "agent-manager" }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}
