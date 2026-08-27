package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// renderFixtureTemplates runs the template pipeline over fixtureTemplates.
func renderFixtureTemplates(t *testing.T, config *Config, sources map[string]string) (TemplateSet, error) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range sources {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	result, err := Transform(Inputs{
		Config:       config,
		Fleet:        parseDocument(t, fixtureFleet),
		Connectivity: parseDocument(t, fixtureConnectivity),
		Overlay:      parseDocument(t, ""),
	})
	require.NoError(t, err)
	return RenderTemplates(config, dir, result.Moves, result.ValueKeys)
}

func TestRenderTemplates(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	templates, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)

	require.NotContains(t, templates, "NOTES.txt", "an extra file is the umbrella's own and is never copied")
	helpers := string(templates["_helpers.tpl"])
	require.Contains(t, helpers, `define "agent-platform-standalone.name"`)
	require.Contains(t, helpers, `define "agent-platform-standalone.dnsEgress"`, "the fleet prefix is replaced, not stacked")

	netpol := string(templates["netpol.yaml"])
	for _, want := range []string{
		`include "agent-platform-standalone.name"`,
		`include "agent-platform-standalone.dnsEgress"`,
		".Values.components.muster.enabled",
		".Values.kagent.kagent.namespaceOverride",
		".Values.components.kagent.controllerRoute.enabled",
		`(index .Values.components "agent-sandbox").podSecurity.enabled`,
		`(index .Values "agent-platform-mcps").mcpServers`,
		"components.agent-sandbox.podSecurity.namespace must match",
	} {
		require.Contains(t, netpol, want)
	}
	require.NotContains(t, netpol, ".Values.agentSandbox")
	require.NotContains(t, netpol, ".Values.kagent.namespaceOverride")
}

func TestRenderTemplatesIsDeterministic(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	first, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)
	second, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)
	require.Equal(t, first, second)
}

// A template that reads a key this chart does not carry must fail the run: the
// copy would render an empty value and the operator would see no error.
func TestRenderTemplatesRejectsUnknownKey(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	sources := map[string]string{"netpol.yaml": "engine: {{ .Values.gitops.engine }}\n"}
	_, err := renderFixtureTemplates(t, config, sources)
	require.ErrorContains(t, err, "templates/netpol.yaml reads .Values.gitops")
}

func TestRenderTemplatesRejectsKeyOutsideTheValues(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	sources := map[string]string{"x.yaml": "a: {{ .Values.brandNew.field }}\n"}
	_, err := renderFixtureTemplates(t, config, sources)
	require.ErrorContains(t, err, `the generated values have no "brandNew" key`)
}

func TestRenderTemplatesLeavesDynamicLookupsAlone(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	source := `{{- range $moved -}}{{- if hasKey (index $.Values (first .) | default dict) "enabled" -}}{{- end -}}{{- end -}}`
	templates, err := renderFixtureTemplates(t, config, map[string]string{"x.yaml": source})
	require.NoError(t, err)
	require.Equal(t, source, string(templates["x.yaml"]))
}

func TestRenderTemplatesExplicitRewriteWins(t *testing.T) {
	config := parseConfig(t, strings.Replace(fixtureConfig,
		"    - from: .Values.muster.enabled\n      to: .Values.components.muster.enabled\n", "", 1))
	templates, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)
	require.Contains(t, string(templates["netpol.yaml"]), ".Values.muster.enabled",
		"without the rewrite the source path stands: muster.enabled is a real key here")
}
