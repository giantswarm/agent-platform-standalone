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
    enabledFrom: kagent.enabled
  agent-platform-mcps:
    chart: agent-platform-mcps
    repository: oci://example/charts
    valuesFrom: agent-platform-mcps
    enabledFrom: mcps.enabled
  agent-sandbox:
    chart: agent-sandbox
    repository: oci://example/charts
    enabledFrom: agentSandbox.enabled
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
  enabled: false
  # route comment survives
  controllerRoute:
    enabled: false
  controller:
    image: kagent-controller
    env:
      - name: OTEL_EXPORTER_OTLP_HEADERS
        value: X-Scope-OrgID=giantswarm
mcps:
  enabled: true
agent-platform-mcps:
  mcpServers: []
agentSandbox:
  enabled: false
  podSecurity:
    enabled: true
`

const fixtureConnectivity = `global:
  registry: gsoci.azurecr.io
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
mcps:
  enabled: false
agent-platform-mcps: {}
agentSandbox:
  enabled: false
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
  version: 2.13.0
  inlineComponents: [agent-platform-connectivity]
chart:
  apiVersion: v2
  name: agent-platform-standalone
  version: 0.0.0-dev
dependencies:
  - name: muster
    range: "5.x"
  - name: kagent
    range: "0.x"
  - name: agent-platform-mcps
    range: "0.x"
  - name: agent-sandbox
    range: "0.x"
  - name: backstage
    range: "0.x"
    repository: oci://example/extra
    enabled: false
keys:
  global: {action: keep}
  gitops: {action: drop}
  components: {action: dependencies}
  ingress: {action: wiring}
  networkPolicy: {action: wiring, moveTo: global.networkPolicy}
  postgres: {action: wiring}
  gatewayApi: {action: umbrella}
  muster: {action: component}
  kagent:
    action: component
    lift: [controllerRoute]
  mcps: {action: toggle}
  agent-platform-mcps: {action: component}
  agentSandbox:
    action: toggle
    lift: [podSecurity]
`

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
		Config:       parseConfig(t, fixtureConfig),
		Fleet:        parseDocument(t, fixtureFleet),
		Connectivity: parseDocument(t, fixtureConnectivity),
		Contract:     parseDocument(t, fixtureContract),
		Overlay:      parseDocument(t, fixtureOverlay),
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
