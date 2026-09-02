package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
)

const (
	repoSchema = "../../" + defaultSchema
	repoDir    = "../../" + defaultDir
)

// Every shipped preset validates against the schema the portal codes
// against, so a bad preset fails the PR instead of the serve flow.
func TestShippedPresetsValidate(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(repoSchema)
	require.NoError(t, err)
	require.NoError(t, validateFiles(schema, repoDir))

	paths, err := filepath.Glob(filepath.Join(repoDir, "*.yaml"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(paths), 5, "the seed corpus ships")
}

func TestValidateFilesRejectsBadPresets(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(repoSchema)
	require.NoError(t, err)

	cases := map[string]struct {
		name, body, want string
	}{
		"missing requirements": {
			name: "no-req",
			body: "apiVersion: agent-platform.giantswarm.io/v1alpha1\nkind: ServingPreset\nmetadata:\n  name: no-req\nspec:\n  displayName: x\n  model:\n    id: a/b\n    storageUri: hf://a/b\n",
			want: "missing property 'requirements'",
		},
		"unknown field": {
			name: "typo",
			body: "apiVersion: agent-platform.giantswarm.io/v1alpha1\nkind: ServingPreset\nmetadata:\n  name: typo\nspec:\n  displayName: x\n  model:\n    id: a/b\n    storageUri: hf://a/b\n  requirements:\n    weightsGiB: 1\n  vllmArgs: []\n",
			want: "additional properties 'vllmArgs' not allowed",
		},
		"name must match file": {
			name: "other",
			body: "apiVersion: agent-platform.giantswarm.io/v1alpha1\nkind: ServingPreset\nmetadata:\n  name: named\nspec:\n  displayName: x\n  model:\n    id: a/b\n    storageUri: hf://a/b\n  requirements:\n    weightsGiB: 1\n",
			want: `metadata.name "named" must equal the file name "other"`,
		},
		"two chat template sources": {
			name: "two",
			body: "apiVersion: agent-platform.giantswarm.io/v1alpha1\nkind: ServingPreset\nmetadata:\n  name: two\nspec:\n  displayName: x\n  model:\n    id: a/b\n    storageUri: hf://a/b\n  requirements:\n    weightsGiB: 1\n  chatTemplate:\n    file: a.jinja\n    content: b\n",
			want: "oneOf",
		},
		"unshipped chat template": {
			name: "ghost",
			body: "apiVersion: agent-platform.giantswarm.io/v1alpha1\nkind: ServingPreset\nmetadata:\n  name: ghost\nspec:\n  displayName: x\n  model:\n    id: a/b\n    storageUri: hf://a/b\n  requirements:\n    weightsGiB: 1\n  chatTemplate:\n    file: ghost.jinja\n",
			want: `spec.chatTemplate.file "ghost.jinja" is not shipped`,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "presets")
			require.NoError(t, os.MkdirAll(dir, 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "..", "chat-templates"), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, testCase.name+".yaml"), []byte(testCase.body), 0o644))
			err := validateFiles(schema, dir)
			require.Error(t, err)
			require.True(t, strings.Contains(err.Error(), testCase.want), "got: %v", err)
		})
	}
}

// The render mode is what `make verify-model-serving` runs: a published
// preset must carry the defaults and the resolved chat template.
func TestValidateRender(t *testing.T) {
	schema, err := jsonschema.NewCompiler().Compile(repoSchema)
	require.NoError(t, err)

	good := `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: agent-platform-model-serving
  namespace: agent-platform
  labels:
    agent-platform.giantswarm.io/model-serving-config: "true"
data:
  config.yaml: |
    apiVersion: agent-platform.giantswarm.io/v1alpha1
    kind: ModelServingConfig
    spec:
      namespace: model-serving
      runtime: kserve-vllm
      presets:
        namespace: agent-platform
        labelSelector: agent-platform.giantswarm.io/serving-preset=true
        names:
          - demo
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: agent-platform-serving-preset-demo
  namespace: agent-platform
  labels:
    agent-platform.giantswarm.io/serving-preset: "true"
    agent-platform.giantswarm.io/preset: demo
    agent-platform.giantswarm.io/preset-source: shipped
data:
  preset.yaml: |
    apiVersion: agent-platform.giantswarm.io/v1alpha1
    kind: ServingPreset
    metadata:
      name: demo
    spec:
      displayName: Demo
      model:
        id: org/demo
        storageUri: hf://org/demo
        format: vLLM
      runtime: kserve-vllm
      args:
        - --max-model-len=4096
        - --chat-template=/mnt/chat-template/chat-template.jinja
      chatTemplate:
        configMap: agent-platform-chat-template-demo
        key: chat-template.jinja
        mountPath: /mnt/chat-template
      resources:
        gpus: 1
      requirements:
        weightsGiB: 4
        overheadGiB: 30
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: agent-platform-chat-template-demo
  namespace: model-serving
  labels:
    agent-platform.giantswarm.io/serving-asset: chat-template
data:
  chat-template.jinja: "{{ messages }}"
`
	require.NoError(t, validateRender(schema, strings.NewReader(good)))

	t.Run("chat template ConfigMap key missing", func(t *testing.T) {
		bad := strings.Replace(good, `  chat-template.jinja: "{{ messages }}"`, `  other.jinja: "{{ messages }}"`, 1)
		err := validateRender(schema, strings.NewReader(bad))
		require.ErrorContains(t, err, "has no key chat-template.jinja")
	})
	t.Run("flag not appended", func(t *testing.T) {
		bad := strings.Replace(good, "        - --chat-template=/mnt/chat-template/chat-template.jinja\n", "", 1)
		err := validateRender(schema, strings.NewReader(bad))
		require.ErrorContains(t, err, "args must end with --chat-template=/mnt/chat-template/chat-template.jinja")
	})
	t.Run("authoring key leaked", func(t *testing.T) {
		bad := strings.Replace(good, "        configMap: agent-platform-chat-template-demo\n", "        configMap: agent-platform-chat-template-demo\n        file: demo.jinja\n", 1)
		err := validateRender(schema, strings.NewReader(bad))
		require.ErrorContains(t, err, "oneOf")
	})
	t.Run("preset not listed in discovery", func(t *testing.T) {
		bad := strings.Replace(good, "          - demo\n", "          - other\n", 1)
		err := validateRender(schema, strings.NewReader(bad))
		require.ErrorContains(t, err, "not listed in the discovery ConfigMap")
	})
	t.Run("no discovery ConfigMap", func(t *testing.T) {
		bad := strings.Replace(good, `agent-platform.giantswarm.io/model-serving-config: "true"`, `x: "y"`, 1)
		err := validateRender(schema, strings.NewReader(bad))
		require.ErrorContains(t, err, "no ConfigMap labelled")
	})
}
