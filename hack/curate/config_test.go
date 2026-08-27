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
		"agent-platform-mcps", "backstage", "agent-sandbox", "cloudnative-pg",
	}, names)
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
			mutate: strings.Replace(fixtureConfig, "version: 2.13.0", "version: 2.x", 1),
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
			mutate: strings.Replace(fixtureConfig, "{action: keep}", "{action: forward}", 1),
			want:   `unknown action "forward"`,
		},
		"lift only with component or toggle": {
			mutate: strings.Replace(fixtureConfig, "ingress: {action: wiring}", "ingress: {action: wiring, lift: [x]}", 1),
			want:   "lift is only valid",
		},
		"exactly one dependencies key": {
			mutate: strings.Replace(fixtureConfig, "components: {action: dependencies}", "components: {action: drop}", 1),
			want:   "exactly one key must have action",
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
