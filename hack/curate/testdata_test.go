package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const fixtureFleet = `global:
  registry: gsoci.azurecr.io
gitops:
  engine: flux
components:
  muster:
    chart: muster
    repository: oci://example/charts
    valuesFrom: muster
  kagent:
    chart: kagent
    repository: oci://example/charts
    valuesFrom: kagent
    valuesKey: kagent
    enabled: false
  agent-platform-mcps:
    chart: agent-platform-mcps
    repository: oci://example/charts
    valuesFrom: agent-platform-mcps
    enabled: true
  agent-sandbox:
    chart: agent-sandbox
    repository: oci://example/charts
    enabled: false
  agent-platform-connectivity:
    chart: agent-platform-connectivity
    repository: oci://example/charts
    forwardAllValues: true
ingress:
  parentRefs: []
muster:  # @schema skipProperties: true
  enabled: true
  fullnameOverride: muster
kagent:
  # route comment survives
  controllerRoute:
    enabled: false
  controller:
    image: kagent-controller
    env:
      - name: OTEL_EXPORTER_OTLP_HEADERS
        value: X-Scope-OrgID=giantswarm
agent-platform-mcps:
  mcpServers: []
agentSandbox:
  podSecurity:
    enabled: true
`

const fixtureConnectivity = `global:
  registry: gsoci.azurecr.io
components:
  kagent:
    enabled: false
ingress:
  parentRefs: []
  fromConnectivity: true
  httpRoute:
    labels: {}
# netpol comment survives the move
networkPolicy:
  enabled: true
  flavor: cilium
postgres:
  enabled: false
  clusterName: kagent-pg
muster:
  fullnameOverride: muster
kagent: {}
agent-platform-mcps: {}
agentSandbox:
  podSecurity:
    enabled: true
`

const fixtureContract = `global:
  domain: ""
gatewayApi:
  gateway:
    create: false
ingress:
  httpRoute:
    labels: {}  # @schema type: object; additionalProperties: true
backstage:
  ingress:
    enabled: false
`

const fixtureOverlay = `global:
  networkPolicy:
    enabled: false
    flavor: kubernetes
muster:
  replicaCount: 1
kagent:
  kagent:
    controller:
      env: []
`

const fixtureGiantswarmInputs = `global:
  domain: example.gigantic.io
gatewayApi:
  gateway:
    create: true
`

const fixtureConfig = `fleet:
  repository: oci://example/charts
  chart: agent-platform
  connectivityChart: agent-platform-connectivity
  version: 3.0.0
  inlineComponents: [agent-platform-connectivity]
chart:
  apiVersion: v2
  name: agent-platform-standalone
  version: 0.0.0-dev
dependencies:
  - name: muster
    range: "5.x"
    version: "5.5.3"
  - name: kagent
    range: "0.x"
    version: "0.1.37"
  - name: agent-platform-mcps
    range: "0.x"
    version: "0.6.7"
  - name: agent-sandbox
    range: "0.x"
    version: "0.2.23"
  - name: backstage
    range: "0.x"
    version: "0.195.2"
    repository: oci://example/extra
    enabled: false
keys:
  global: {action: keep, allowShadow: true}
  gitops: {action: drop}
  components: {action: dependencies, allowShadow: true}
  ingress: {action: wiring, allowShadow: true}
  networkPolicy: {action: wiring, moveTo: global.networkPolicy}
  postgres: {action: wiring}
  gatewayApi: {action: umbrella}
  muster:
    action: component
    keepEnabled: true
    allowShadow: true
  kagent:
    action: component
    lift: [controllerRoute]
    allowShadow: true
  agent-platform-mcps: {action: component, allowShadow: true}
  agentSandbox:
    action: lift
    chart: agent-sandbox
    lift: [podSecurity]
    allowShadow: true
templates:
  rewrite:
    - from: .Values.muster.enabled
      to: .Values.components.muster.enabled
  extra:
    - NOTES.txt
`

// fixtureTemplates is the source chart's template tree: one helper file, one
// template that reads every kind of moved path, and the notes file the umbrella
// owns.
var fixtureTemplates = map[string]string{
	"_helpers.tpl": `{{- define "name" -}}
{{ .Chart.Name }}
{{- end -}}
{{- define "agent-platform.dnsEgress" -}}
dns
{{- end -}}
`,
	"netpol.yaml": `{{- /* agentSandbox.podSecurity.namespace must match the controller namespace. */ -}}
{{- if and .Values.muster.enabled .Values.components.kagent.enabled }}
name: {{ include "name" . }}
ns: {{ .Values.kagent.namespaceOverride }}
route: {{ .Values.kagent.controllerRoute.enabled }}
sandbox: {{ .Values.agentSandbox.podSecurity.enabled }}
mcps: {{ (index .Values "agent-platform-mcps").mcpServers }}
flavor: {{ .Values.networkPolicy.flavor }}
dns: {{ include "agent-platform.dnsEgress" . }}
{{- end }}
`,
	"NOTES.txt": "the source chart's notes\n",
}

func parseConfig(t *testing.T, raw string) *Config {
	t.Helper()
	var config Config
	require.NoError(t, yaml.Unmarshal([]byte(raw), &config))
	require.NoError(t, config.Validate())
	return &config
}

func parseDocument(t *testing.T, raw string) *yaml.Node {
	t.Helper()
	document, err := parseYAML([]byte(raw), "fixture")
	require.NoError(t, err)
	return document
}

func fixtureInputs(t *testing.T) Inputs {
	t.Helper()
	return Inputs{
		Config:           parseConfig(t, fixtureConfig),
		Fleet:            parseDocument(t, fixtureFleet),
		Connectivity:     parseDocument(t, fixtureConnectivity),
		Contract:         parseDocument(t, fixtureContract),
		Overlay:          parseDocument(t, fixtureOverlay),
		GiantswarmInputs: parseDocument(t, fixtureGiantswarmInputs),
	}
}

func decodeValues(t *testing.T, document *yaml.Node) map[string]any {
	t.Helper()
	var values map[string]any
	require.NoError(t, document.Decode(&values))
	return values
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	mapping, ok := value.(map[string]any)
	require.True(t, ok, "expected a mapping, got %T", value)
	return mapping
}
