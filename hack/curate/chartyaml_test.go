package main

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func mustGet(t *testing.T, mapping *yaml.Node, key string) *yaml.Node {
	t.Helper()
	_, value := mappingGet(mapping, key)
	require.NotNil(t, value, "key %q", key)
	return value
}

func TestBuildChartYAML(t *testing.T) {
	result, err := Transform(fixtureInputs(t))
	require.NoError(t, err)
	resolved := map[string]string{
		"muster": "5.5.3", "kagent": "0.1.37", "agent-platform-mcps": "0.6.7", "agent-sandbox": "0.2.23", "backstage": "0.195.2",
	}
	document, err := BuildChartYAML(parseConfig(t, fixtureConfig), result.Components, resolved)
	require.NoError(t, err)
	out, err := encodeYAML(document)
	require.NoError(t, err)
	text := string(out)

	require.Contains(t, text, "name: agent-platform-standalone")
	require.Contains(t, text, "version: 0.0.0-dev")
	require.Contains(t, text, `version: "5.5.3"  # range 5.x`, "two spaces before the comment, as chart-testing's yamllint demands")
	require.Contains(t, text, "condition: components.agent-platform-mcps.enabled")
	require.Contains(t, text, "repository: oci://example/extra", "extra dependency keeps its own repository")
	require.Contains(t, text, "repository: oci://example/charts", "fleet dependency takes the fleet repository")
	require.NotContains(t, text, "alias")

	var chart struct {
		Dependencies []struct {
			Name, Version, Repository, Condition string
		}
	}
	require.NoError(t, yaml.Unmarshal(out, &chart))
	require.Len(t, chart.Dependencies, 5)
	for _, dependency := range chart.Dependencies {
		require.Regexp(t, `^\d+\.\d+\.\d+$`, dependency.Version, dependency.Name)
		require.Equal(t, "components."+dependency.Name+".enabled", dependency.Condition)
	}
}

func TestBuildChartYAMLRejectsVersionOutsideRange(t *testing.T) {
	result, err := Transform(fixtureInputs(t))
	require.NoError(t, err)
	resolved := map[string]string{
		"muster": "6.0.0", "kagent": "0.1.37", "agent-platform-mcps": "0.6.7", "agent-sandbox": "0.2.23", "backstage": "0.195.2",
	}
	_, err = BuildChartYAML(parseConfig(t, fixtureConfig), result.Components, resolved)
	require.ErrorContains(t, err, `dependency "muster" resolved to 6.0.0, outside range 5.x`)
}

func TestBuildChartYAMLRejectsMissingResolution(t *testing.T) {
	result, err := Transform(fixtureInputs(t))
	require.NoError(t, err)
	_, err = BuildChartYAML(parseConfig(t, fixtureConfig), result.Components, map[string]string{"muster": "5.5.3"})
	require.ErrorContains(t, err, "no resolved version")
}
