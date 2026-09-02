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
		"cloudnative-pg", "model-manager",
		"kserve-crd", "kserve-resources", "kserve-llmisvc-crd", "kserve-llmisvc-resources",
	}, names)
	require.Equal(t, []string{"modelServing", "kserve"}, config.UmbrellaComponents, "components the umbrella renders itself, without a dependency of their own")
	for _, name := range []string{"kserve-crd", "kserve-resources"} {
		dependency, ok := config.Dependency(name)
		require.True(t, ok)
		require.Equal(t, "components.kserve.enabled", dependency.Condition, name)
	}
	for _, name := range []string{"kserve-llmisvc-crd", "kserve-llmisvc-resources"} {
		dependency, ok := config.Dependency(name)
		require.True(t, ok)
		require.Equal(t, "components.kserve.llmisvc.enabled", dependency.Condition, name)
	}
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

// A condition override ties an extra dependency to an umbrella component's
// switch (a control plane split into a CRD chart and a controller chart).
func TestConfigValidateDependencyCondition(t *testing.T) {
	base := fixtureConfig + "umbrellaComponents: [serving]\n"
	extra := func(fields string) string {
		return strings.Replace(base, "    repository: oci://example/extra\n    enabled: false\n",
			"    repository: oci://example/extra\n"+fields, 1)
	}
	cases := map[string]struct {
		config string
		want   string
	}{
		"valid": {
			config: extra("    condition: components.serving.enabled\n"),
		},
		"valid nested": {
			config: extra("    condition: components.serving.controller.enabled\n"),
		},
		"enabled and condition are exclusive": {
			config: extra("    enabled: false\n    condition: components.serving.enabled\n"),
			want:   `dependency "backstage": enabled and condition are exclusive`,
		},
		"shape": {
			config: extra("    condition: serving.enabled\n"),
			want:   `must have the form components.<umbrellaComponent>[.<key>...].enabled`,
		},
		"must end in enabled": {
			config: extra("    condition: components.serving.on\n"),
			want:   `must have the form components.<umbrellaComponent>[.<key>...].enabled`,
		},
		"unknown umbrella component": {
			config: extra("    condition: components.other.enabled\n"),
			want:   `points at components.other, which is not declared in umbrellaComponents`,
		},
		"fleet dependency": {
			config: strings.Replace(base, "    version: \"5.5.3\"\n", "    version: \"5.5.3\"\n    condition: components.serving.enabled\n", 1),
			want:   `dependency "muster": condition is only valid on an extra dependency`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var config Config
			require.NoError(t, yaml.Unmarshal([]byte(tc.config), &config))
			err := config.Validate()
			if tc.want == "" {
				require.NoError(t, err)
				dependency, ok := config.Dependency("backstage")
				require.True(t, ok)
				require.False(t, dependency.HasOwnToggle())
				require.Equal(t, "serving", dependency.ConditionComponent())
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}
