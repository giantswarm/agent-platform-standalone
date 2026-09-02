package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestLoadConfigRepoFile(t *testing.T) {
	config, err := LoadConfig(filepath.Join("..", "..", "curate.yaml"))
	require.NoError(t, err)
	require.Equal(t, "agent-platform", config.Fleet.Chart)
	names := make([]string, 0, len(config.Dependencies))
	for _, dependency := range config.Dependencies {
		names = append(names, dependency.Name)
	}
	require.ElementsMatch(t, []string{
		"muster", "kagent", "agentgateway", "valkey", "dicebear", "klaus-gateway",
		"agent-platform-mcps", "mcp-kubernetes", "backstage", "agent-sandbox",
		"cloudnative-pg",
	}, names)
	require.Equal(t, []string{"modelServing"}, config.UmbrellaComponents, "components the umbrella renders itself, without a dependency")
}

func TestConfigValidate(t *testing.T) {
	cases := map[string]struct {
		mutate string
		want   string
	}{
		"range must be major-bounded": {
			mutate: strings.Replace(fixtureConfig, `range: "5.x"`, `range: ">=5.0.0"`, 1),
			want:   "must have the form <major>.x",
		},
		"fleet pin must be exact": {
			mutate: strings.Replace(fixtureConfig, "version: 3.0.0", "version: 3.x", 1),
			want:   "must be an exact version",
		},
		"extra dependency needs enabled": {
			mutate: strings.Replace(fixtureConfig, "    enabled: false\n", "", 1),
			want:   `extra dependency "backstage" must set enabled`,
		},
		"fleet dependency must not set enabled": {
			mutate: strings.Replace(fixtureConfig, "  - name: muster\n    range: \"5.x\"\n", "  - name: muster\n    range: \"5.x\"\n    enabled: true\n", 1),
			want:   `fleet dependency "muster" must not set enabled`,
		},
		"chart.dependencies is generated": {
			mutate: strings.Replace(fixtureConfig, "  version: 0.0.0-dev\n", "  version: 0.0.0-dev\n  dependencies: []\n", 1),
			want:   "chart.dependencies is generated",
		},
		"unknown action": {
			mutate: strings.Replace(fixtureConfig, "{action: keep, allowShadow: true}", "{action: forward, allowShadow: true}", 1),
			want:   `unknown action "forward"`,
		},
		"lift only with component or lift": {
			mutate: strings.Replace(fixtureConfig, "ingress: {action: wiring, allowShadow: true}", "ingress: {action: wiring, allowShadow: true, lift: [x]}", 1),
			want:   "lift is only valid",
		},
		"action lift needs a chart": {
			mutate: strings.Replace(fixtureConfig, "    chart: agent-sandbox\n", "", 1),
			want:   "action lift must name the dependency it wires",
		},
		"chart must be a dependency": {
			mutate: strings.Replace(fixtureConfig, "chart: agent-sandbox", "chart: ghost", 1),
			want:   `chart "ghost" is not a dependency`,
		},
		"keepEnabled only with component": {
			mutate: strings.Replace(fixtureConfig, "ingress: {action: wiring, allowShadow: true}", "ingress: {action: wiring, allowShadow: true, keepEnabled: true}", 1),
			want:   "keepEnabled is only valid with action component",
		},
		"exactly one dependencies key": {
			mutate: strings.Replace(fixtureConfig, "components: {action: dependencies, allowShadow: true}", "components: {action: drop, allowShadow: true}", 1),
			want:   "exactly one key must have action",
		},
		"patch needs file and find": {
			mutate: fixtureConfig + "  patch:\n    - file: netpol.yaml\n",
			want:   "templates.patch[0]: file and find are required",
		},
		"annotation must not be empty": {
			mutate: fixtureConfig + "annotations:\n  ingress.parentRefs: \"\"\n",
			want:   "annotations.ingress.parentRefs",
		},
		"umbrella component must not be a dependency": {
			mutate: fixtureConfig + "umbrellaComponents: [kagent]\n",
			want:   `umbrellaComponents: "kagent" is a dependency`,
		},
		"umbrella component listed twice": {
			mutate: fixtureConfig + "umbrellaComponents: [serving, serving]\n",
			want:   `umbrellaComponents: "serving" listed twice`,
		},
		"umbrella component must be an identifier": {
			mutate: fixtureConfig + "umbrellaComponents: [model-serving]\n",
			want:   `umbrellaComponents: "model-serving" must be a plain identifier`,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			var config Config
			require.NoError(t, yaml.Unmarshal([]byte(testCase.mutate), &config))
			require.ErrorContains(t, config.Validate(), testCase.want)
		})
	}
}
