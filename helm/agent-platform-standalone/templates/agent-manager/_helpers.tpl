{{/* vim: set filetype=mustache: */}}
{{/*
Helpers of the agent-manager component's wiring (templates/agent-manager/).

Two values blocks feed these templates: components.agent-manager (the umbrella wiring —
route, JWT policy, network policy inputs, guards) and agent-manager (the
component chart's own values: the kagent namespace, the agent chart URL, OAuth,
Service name and port; hyphenated, so reached through index), read here the
way the model-manager templates read theirs — a component release's values
cannot be derived at render time, so the wiring reads what the chart will see.
*/}}

{{/*
Truthy when the agent-manager component is on (components.agent-manager.enabled).
*/}}
{{- define "agent-platform-standalone.agentManager.enabled" -}}
{{- include "agent-platform-standalone.componentEnabled" (dict "root" . "name" "agent-manager") -}}
{{- end -}}

{{/*
The component chart's values block, agent-manager (a dict; empty when unset).
*/}}
{{- define "agent-platform-standalone.agentManager.chartValues" -}}
{{- index .Values "agent-manager" | default dict | toJson -}}
{{- end -}}

{{/*
The agent-manager Service name. Single source of truth: the umbrella pins
agent-manager.fullnameOverride (values.yaml), which the component chart uses
verbatim for its Service, and the AgentgatewayBackend host and the network
policies target exactly that name — a misconfiguration fails the render
instead of a silent 503.
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
Truthy when the component validates the caller's identity itself
(agent-manager.oauth.enabled): the network policies then admit egress to the
identity provider.
*/}}
{{- define "agent-platform-standalone.agentManager.oauthEnabled" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- if dig "oauth" "enabled" false $chart }}true{{ end -}}
{{- end -}}

{{/*
The identity provider the component validates tokens with
(agent-manager.oauth.provider): dex (the default) or google.
*/}}
{{- define "agent-platform-standalone.agentManager.oauthProvider" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- dig "oauth" "provider" "dex" $chart -}}
{{- end -}}

{{/*
The issuer URL the component validates tokens against: the dex provider's
agent-manager.oauth.dex.issuerURL, else global.identity.issuerUrl (the chart's
own fallback). Empty for the google provider (whose public endpoints
agent-platform.idpHosts names from the provider alone) and when neither is set.
*/}}
{{- define "agent-platform-standalone.agentManager.issuerUrl" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
{{- if eq (include "agent-platform-standalone.agentManager.oauthProvider" .) "dex" -}}
{{- dig "oauth" "dex" "issuerURL" "" $chart | default .Values.global.identity.issuerUrl -}}
{{- end -}}
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
The selector labels of the agent-manager pods, as the component chart stamps
them (app.kubernetes.io/name from its chart name or nameOverride). The
component runs as its own release, so it is selected by name only, not by a
release-scoped instance label — like the muster policies.
Rendered as YAML mapping entries; the caller provides the indentation.
*/}}
{{- define "agent-platform-standalone.agentManager.podSelector" -}}
{{- $chart := include "agent-platform-standalone.agentManager.chartValues" . | fromJson -}}
app.kubernetes.io/name: {{ dig "nameOverride" "" $chart | default "agent-manager" }}
{{- end -}}
