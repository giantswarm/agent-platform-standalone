package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
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
	in := fixtureInputs(t)
	in.Config = config
	result, err := Transform(in)
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
		".Values.global.networkPolicy.flavor",
		"components.agent-sandbox.podSecurity.namespace must match",
	} {
		require.Contains(t, netpol, want)
	}
	require.NotContains(t, netpol, ".Values.agentSandbox")
	require.NotContains(t, netpol, ".Values.kagent.namespaceOverride")
	require.NotContains(t, netpol, ".Values.networkPolicy", "a moveTo rule relocates the wiring block in the templates too")
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

// Prose (comments and fail messages) follows the full move list: lifted keys
// and nested blocks move, while longer identifiers, paths under a key that did
// not move, and domain names stay as written.
func TestRenderTemplatesRewritesProsePaths(t *testing.T) {
	config := parseConfig(t, fixtureConfig)
	source := `{{- fail "Set kagent.controllerRoute.hostname; kagent.namespaceOverride must match" }}
# muster.controllerRoute.hostname is nested under a carried key
# myagentSandbox.foo is a longer identifier
# kagent.dev is a domain, not a values path
# networkPolicy.flavor governs the policy flavor
`
	templates, err := renderFixtureTemplates(t, config, map[string]string{"x.yaml": source})
	require.NoError(t, err)
	text := string(templates["x.yaml"])
	require.Contains(t, text, "Set components.kagent.controllerRoute.hostname", "a lifted key moves in prose")
	require.Contains(t, text, "kagent.kagent.namespaceOverride must match", "a nested block moves in prose")
	require.Contains(t, text, "muster.controllerRoute.hostname is nested", "a path under an unmoved key stays")
	require.Contains(t, text, "myagentSandbox.foo is a longer identifier")
	require.Contains(t, text, "kagent.dev is a domain")
	require.Contains(t, text, "global.networkPolicy.flavor governs", "a moved wiring block moves in prose")
}

func TestRenderTemplatesAppliesPatches(t *testing.T) {
	patched := fixtureConfig + `  patch:
    - file: netpol.yaml
      find: |-
        dns: {{ include "agent-platform-standalone.dnsEgress" . }}
      replace: |-
        dns: patched
`
	config := parseConfig(t, patched)
	templates, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)
	require.Contains(t, string(templates["netpol.yaml"]), "dns: patched")

	stale := fixtureConfig + `  patch:
    - file: netpol.yaml
      find: |-
        upstream moved this text away
      replace: |-
        never applied
`
	config = parseConfig(t, stale)
	_, err = renderFixtureTemplates(t, config, fixtureTemplates)
	require.ErrorContains(t, err, "occurs 0 times in netpol.yaml")
}

func TestRenderTemplatesExplicitRewriteWins(t *testing.T) {
	config := parseConfig(t, strings.Replace(fixtureConfig,
		"    - from: .Values.muster.enabled\n      to: .Values.components.muster.enabled\n", "", 1))
	templates, err := renderFixtureTemplates(t, config, fixtureTemplates)
	require.NoError(t, err)
	require.Contains(t, string(templates["netpol.yaml"]), ".Values.muster.enabled",
		"without the rewrite the source path stands: muster.enabled is a real key here")
}

func TestRewriteValuesCommentsKeepsBlockRelativePaths(t *testing.T) {
	// The generated layout after the kagent component moved under its
	// subchart key: the outer block's comments name the kagent chart's keys
	// (absolute fleet paths), the model-manager block's comments name its own
	// kagent.* keys, which merely share the first segment.
	source := `# kagent.namespaceOverride at the root is the kagent chart's key
kagent:
  kagent:
    namespaceOverride: kagent
# ModelConfigs are wired into kagent.namespace, which must be the kagent
# component's namespace (kagent.namespaceOverride); an install without kagent
# sets kagent.disableWiring: true.
model-manager:
  kagent:
    namespace: kagent
    disableWiring: false
  mcp:
    # kagent.namespace is the block's own key here too, two levels down
    enabled: true
  # kagent.missing resolves nowhere, so the move stands
  other: 1
`
	var document yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(source), &document))
	moves := []PathMove{{From: []string{"kagent"}, To: []string{"kagent", "kagent"}}}
	rewriteValuesComments(&document, moves, []string{"kagent", "model-manager"})
	out, err := encodeYAML(&document)
	require.NoError(t, err)
	text := string(out)
	require.Contains(t, text, "# kagent.kagent.namespaceOverride at the root", "a root comment moves")
	require.Contains(t, text, "wired into kagent.namespace, which", "the block's own key stays")
	require.Contains(t, text, "namespace (kagent.kagent.namespaceOverride); an install", "the kagent chart's key moves inside the block too")
	require.Contains(t, text, "sets kagent.disableWiring: true", "the block's own key stays")
	require.Contains(t, text, "# kagent.namespace is the block's own key here too", "an enclosing block is a scope for nested comments")
	require.Contains(t, text, "# kagent.kagent.missing resolves nowhere", "a path that resolves in no block moves")
}
