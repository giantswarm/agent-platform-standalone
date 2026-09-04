{{/* vim: set filetype=mustache: */}}
{{/*
Helpers of the model-manager component's wiring (templates/model-manager/).

Two values blocks feed these templates: components.model-manager (the umbrella wiring —
route, JWT policy, network policy inputs, guards) and model-manager (the
component chart's own values: backend, endpoint, kagent namespace, OAuth,
Service name and port; hyphenated, so reached through index), read here the
way the kagent templates read kagent.kagent.namespaceOverride — a component release's
values cannot be derived at render time, so the wiring reads what the chart
will see.
*/}}

{{/*
Truthy when the model-manager component is on (components.model-manager.enabled).
*/}}
{{- define "agent-platform-standalone.modelManager.enabled" -}}
{{- include "agent-platform-standalone.componentEnabled" (dict "root" . "name" "model-manager") -}}
{{- end -}}

{{/*
The component chart's values block, model-manager (a dict; empty when unset).
*/}}
{{- define "agent-platform-standalone.modelManager.chartValues" -}}
{{- index .Values "model-manager" | default dict | toJson -}}
{{- end -}}

{{/*
The model-manager Service name. Single source of truth: the umbrella pins
model-manager.fullnameOverride (values.yaml), which the component chart uses
verbatim for its Service, and the AgentgatewayBackend host and the network
policies target exactly that name — a misconfiguration fails the render
instead of a silent 503.
*/}}
{{- define "agent-platform-standalone.modelManager.fullname" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- required "model-manager.fullnameOverride must be set — the umbrella's route and network policies target this exact Service name" (dig "fullnameOverride" "" $chart) -}}
{{- end -}}

{{/*
The port the model-manager Service listens on (model-manager.service.port, default 8080).
*/}}
{{- define "agent-platform-standalone.modelManager.servicePort" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- dig "service" "port" 8080 $chart -}}
{{- end -}}

{{/*
The serving backend the chart is configured with (model-manager.backend).
*/}}
{{- define "agent-platform-standalone.modelManager.backend" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- dig "backend" "ollama" $chart -}}
{{- end -}}

{{/*
The Ollama API base URL model-manager dials (model-manager.ollama.endpoint).
*/}}
{{- define "agent-platform-standalone.modelManager.ollamaEndpoint" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- dig "ollama" "endpoint" "" $chart -}}
{{- end -}}

{{/*
The Ollama endpoint split for network policies, as JSON:
  { "host": "<host>", "port": <int>, "isIP": bool }
The port defaults from the scheme (80 / 443) when the URL carries none.
*/}}
{{- define "agent-platform-standalone.modelManager.ollamaTarget" -}}
{{- $endpoint := include "agent-platform-standalone.modelManager.ollamaEndpoint" . -}}
{{- $url := urlParse $endpoint -}}
{{- $hostport := $url.host | default "" -}}
{{- $host := $hostport -}}
{{- $port := 80 -}}
{{- if eq $url.scheme "https" }}{{- $port = 443 -}}{{- end -}}
{{- if contains ":" $hostport -}}
{{- $host = regexReplaceAll ":[0-9]+$" $hostport "" -}}
{{- $port = regexFind "[0-9]+$" $hostport | int -}}
{{- end -}}
{{- dict "host" $host "port" $port "isIP" (regexMatch `^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$` $host) | toJson -}}
{{- end -}}

{{/*
The namespace model-manager wires ModelConfigs into (model-manager.kagent.namespace).
*/}}
{{- define "agent-platform-standalone.modelManager.kagentNamespace" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- dig "kagent" "namespace" "kagent" $chart -}}
{{- end -}}

{{/*
Truthy when the component validates the caller's identity itself
(model-manager.oauth.enabled): the network policies then admit egress to the
identity provider.
*/}}
{{- define "agent-platform-standalone.modelManager.oauthEnabled" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- if dig "oauth" "enabled" false $chart }}true{{ end -}}
{{- end -}}

{{/*
The identity provider the component validates tokens with
(model-manager.oauth.provider): dex (the default) or google.
*/}}
{{- define "agent-platform-standalone.modelManager.oauthProvider" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- dig "oauth" "provider" "dex" $chart -}}
{{- end -}}

{{/*
The issuer URL the component validates tokens against: the dex provider's
model-manager.oauth.dex.issuerURL, else global.identity.issuerUrl (the chart's
own fallback). Empty for the google provider (whose public endpoints
agent-platform.idpHosts names from the provider alone) and when neither is set.
*/}}
{{- define "agent-platform-standalone.modelManager.issuerUrl" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
{{- if eq (include "agent-platform-standalone.modelManager.oauthProvider" .) "dex" -}}
{{- dig "oauth" "dex" "issuerURL" "" $chart | default .Values.global.identity.issuerUrl -}}
{{- end -}}
{{- end -}}

{{/*
The public hostname of the model-manager route: the override when set, else
agentgateway.<global.domain> — the same hostname as the kagent controller route.
*/}}
{{- define "agent-platform-standalone.modelManager.hostname" -}}
{{- $route := (index .Values.components "model-manager").route -}}
{{- include "agent-platform-standalone.hostname" (dict "ctx" . "prefix" "agentgateway" "override" $route.hostname "key" "components.model-manager.route.hostname") -}}
{{- end -}}

{{/*
Labels of every object the umbrella renders for the component.
*/}}
{{- define "agent-platform-standalone.modelManager.labels" -}}
{{ include "agent-platform-standalone.labels.common" . }}
app.kubernetes.io/component: model-manager
{{- end -}}

{{/*
The selector labels of the model-manager pods, as the component chart stamps
them (app.kubernetes.io/name from its chart name or nameOverride). The
component runs as its own release, so it is selected by name only, not by a
release-scoped instance label — like the muster policies.
Rendered as YAML mapping entries; the caller provides the indentation.
*/}}
{{- define "agent-platform-standalone.modelManager.podSelector" -}}
{{- $chart := include "agent-platform-standalone.modelManager.chartValues" . | fromJson -}}
app.kubernetes.io/name: {{ dig "nameOverride" "" $chart | default "model-manager" }}
{{- end -}}
