// Command presets validates ServingPreset documents against the chart's
// preset schema (helm/agent-platform-standalone/files/model-serving/
// serving-preset.schema.json) — the contract the portal's serve flow and
// model-manager read.
//
// Two modes:
//
//	presets [-schema FILE] [-dir DIR]     validate every shipped preset file
//	presets [-schema FILE] -render FILE   validate a rendered chart (`-` reads
//	                                      stdin): every published preset
//	                                      ConfigMap, its labels, its chat
//	                                      template ConfigMap and the
//	                                      discovery ConfigMap
//
// A bad preset is a PR review problem: `go test ./hack/presets` runs the file
// mode, `make verify-model-serving` the render mode.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	defaultSchema = "helm/agent-platform-standalone/files/model-serving/serving-preset.schema.json"
	defaultDir    = "helm/agent-platform-standalone/files/model-serving/presets"

	presetLabel       = "agent-platform.giantswarm.io/serving-preset"
	presetNameLabel   = "agent-platform.giantswarm.io/preset"
	presetSourceLabel = "agent-platform.giantswarm.io/preset-source"
	assetLabel        = "agent-platform.giantswarm.io/serving-asset"
	configLabel       = "agent-platform.giantswarm.io/model-serving-config"
	presetPrefix      = "agent-platform-serving-preset-"
	configName        = "agent-platform-model-serving"
)

func main() {
	schemaPath := flag.String("schema", defaultSchema, "the ServingPreset JSON schema")
	dir := flag.String("dir", defaultDir, "directory of shipped preset files (file mode)")
	render := flag.String("render", "", "a rendered chart to validate instead of the files; - reads stdin")
	flag.Parse()

	schema, err := jsonschema.NewCompiler().Compile(*schemaPath)
	if err != nil {
		fail(err)
	}
	if *render == "" {
		if err := validateFiles(schema, *dir); err != nil {
			fail(err)
		}
		return
	}
	var input io.Reader = os.Stdin
	if *render != "-" {
		file, err := os.Open(*render)
		if err != nil {
			fail(err)
		}
		defer file.Close()
		input = file
	}
	if err := validateRender(schema, input); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "presets:", err)
	os.Exit(1)
}

// validateFiles checks every shipped preset file: schema-valid, named after
// its file, and pointing at a shipped chat template when it names one.
func validateFiles(schema *jsonschema.Schema, dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no preset files in %s", dir)
	}
	var problems []string
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		document, err := yamlToJSON(raw)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if err := schema.Validate(document); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		preset := document.(map[string]any)
		name := field(preset, "metadata", "name")
		if stem := strings.TrimSuffix(filepath.Base(path), ".yaml"); name != stem {
			problems = append(problems, fmt.Sprintf("%s: metadata.name %q must equal the file name %q", path, name, stem))
		}
		if file := field(preset, "spec", "chatTemplate", "file"); file != "" {
			template := filepath.Join(dir, "..", "chat-templates", file)
			if _, err := os.Stat(template); err != nil {
				problems = append(problems, fmt.Sprintf("%s: spec.chatTemplate.file %q is not shipped (%s)", path, file, template))
			}
		}
		for _, key := range []string{"content", "existingConfigMap", "configMap"} {
			if field(preset, "spec", "chatTemplate", key) != "" {
				problems = append(problems, fmt.Sprintf("%s: a shipped preset references its chat template by file, not %s", path, key))
			}
		}
		fmt.Printf("ok  %s\n", path)
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	return nil
}

// object is one rendered Kubernetes manifest, the parts this tool reads.
type object struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string            `yaml:"name"`
		Namespace string            `yaml:"namespace"`
		Labels    map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec map[string]any    `yaml:"spec"`
}

// validateRender checks a rendered chart: the discovery ConfigMap, and every
// preset ConfigMap it lists — schema-valid in its published form (runtime,
// format, gpus and overhead filled, chat template resolved to a ConfigMap that
// exists in the serving namespace with the right key, the --chat-template flag
// last in args), labelled for the portal's selector.
func validateRender(schema *jsonschema.Schema, input io.Reader) error {
	raw, err := io.ReadAll(input)
	if err != nil {
		return err
	}
	var objects []object
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var obj object
		if err := decoder.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("parse render: %w", err)
		}
		if obj.Kind != "" {
			objects = append(objects, obj)
		}
	}

	var config *object
	presets := map[string]object{}
	configMaps := map[string]object{}
	for i, obj := range objects {
		if obj.Kind != "ConfigMap" {
			continue
		}
		configMaps[obj.Metadata.Namespace+"/"+obj.Metadata.Name] = obj
		if obj.Metadata.Labels[configLabel] == "true" {
			if config != nil {
				return fmt.Errorf("two discovery ConfigMaps (%s)", configLabel)
			}
			config = &objects[i]
		}
		if obj.Metadata.Labels[presetLabel] == "true" {
			presets[obj.Metadata.Name] = obj
		}
	}
	if config == nil {
		return fmt.Errorf("no ConfigMap labelled %s=true in the render", configLabel)
	}
	if config.Metadata.Name != configName {
		return fmt.Errorf("discovery ConfigMap is named %q, expected %q", config.Metadata.Name, configName)
	}
	var discovery struct {
		Spec struct {
			Namespace string `yaml:"namespace"`
			Runtime   string `yaml:"runtime"`
			Presets   struct {
				Namespace     string   `yaml:"namespace"`
				LabelSelector string   `yaml:"labelSelector"`
				Names         []string `yaml:"names"`
			} `yaml:"presets"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(config.Data["config.yaml"]), &discovery); err != nil {
		return fmt.Errorf("discovery ConfigMap config.yaml: %w", err)
	}
	if want := presetLabel + "=true"; discovery.Spec.Presets.LabelSelector != want {
		return fmt.Errorf("discovery labelSelector %q, expected %q", discovery.Spec.Presets.LabelSelector, want)
	}
	if discovery.Spec.Presets.Namespace != config.Metadata.Namespace {
		return fmt.Errorf("discovery presets.namespace %q differs from the release namespace %q", discovery.Spec.Presets.Namespace, config.Metadata.Namespace)
	}

	var problems []string
	listed := map[string]bool{}
	for _, name := range discovery.Spec.Presets.Names {
		listed[name] = true
		if _, ok := presets[presetPrefix+name]; !ok {
			problems = append(problems, fmt.Sprintf("discovery lists preset %q but no ConfigMap %s%s is labelled %s=true", name, presetPrefix, name, presetLabel))
		}
	}
	names := make([]string, 0, len(presets))
	for name := range presets {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, cmName := range names {
		cm := presets[cmName]
		where := fmt.Sprintf("ConfigMap %s/%s", cm.Metadata.Namespace, cm.Metadata.Name)
		name := strings.TrimPrefix(cm.Metadata.Name, presetPrefix)
		if name == cm.Metadata.Name {
			problems = append(problems, fmt.Sprintf("%s: name must start with %s", where, presetPrefix))
			continue
		}
		if !listed[name] {
			problems = append(problems, fmt.Sprintf("%s: not listed in the discovery ConfigMap", where))
		}
		if cm.Metadata.Namespace != config.Metadata.Namespace {
			problems = append(problems, fmt.Sprintf("%s: presets live in the release namespace %s", where, config.Metadata.Namespace))
		}
		if cm.Metadata.Labels[presetNameLabel] != name {
			problems = append(problems, fmt.Sprintf("%s: label %s=%q, expected %q", where, presetNameLabel, cm.Metadata.Labels[presetNameLabel], name))
		}
		if source := cm.Metadata.Labels[presetSourceLabel]; source != "shipped" && source != "values" {
			problems = append(problems, fmt.Sprintf("%s: label %s=%q, expected shipped or values", where, presetSourceLabel, source))
		}
		document, err := yamlToJSON([]byte(cm.Data["preset.yaml"]))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: preset.yaml: %v", where, err))
			continue
		}
		if err := schema.Validate(document); err != nil {
			problems = append(problems, fmt.Sprintf("%s: preset.yaml: %v", where, err))
			continue
		}
		preset := document.(map[string]any)
		if got := field(preset, "metadata", "name"); got != name {
			problems = append(problems, fmt.Sprintf("%s: preset.yaml metadata.name %q, expected %q", where, got, name))
		}
		// The published form carries every default the portal would otherwise
		// have to know.
		for _, path := range [][]string{{"spec", "runtime"}, {"spec", "model", "format"}} {
			if field(preset, path...) == "" {
				problems = append(problems, fmt.Sprintf("%s: published preset lacks %s", where, strings.Join(path, ".")))
			}
		}
		for _, path := range [][]string{{"spec", "resources", "gpus"}, {"spec", "requirements", "overheadGiB"}} {
			if _, ok := lookup(preset, path...); !ok {
				problems = append(problems, fmt.Sprintf("%s: published preset lacks %s", where, strings.Join(path, ".")))
			}
		}
		if template, ok := lookup(preset, "spec", "chatTemplate"); ok {
			problems = append(problems, checkChatTemplate(where, preset, template, discovery.Spec.Namespace, configMaps)...)
		}
		fmt.Printf("ok  %s (%s, runtime %s)\n", where, cm.Metadata.Labels[presetSourceLabel], field(preset, "spec", "runtime"))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}
	fmt.Printf("ok  discovery %s/%s: serving namespace %s, runtime %s, %d presets\n",
		config.Metadata.Namespace, config.Metadata.Name, discovery.Spec.Namespace, discovery.Spec.Runtime, len(presets))
	return nil
}

// checkChatTemplate verifies the published chat template: resolved to a
// ConfigMap (the authoring keys are gone), the ConfigMap rendered in the
// serving namespace with the key, the mount flag appended last.
func checkChatTemplate(where string, preset map[string]any, template any, servingNamespace string, configMaps map[string]object) []string {
	var problems []string
	fields, _ := template.(map[string]any)
	for _, key := range []string{"file", "content", "existingConfigMap"} {
		if _, ok := fields[key]; ok {
			problems = append(problems, fmt.Sprintf("%s: published chatTemplate still carries the authoring key %s", where, key))
		}
	}
	configMap, _ := fields["configMap"].(string)
	key, _ := fields["key"].(string)
	mountPath, _ := fields["mountPath"].(string)
	if configMap == "" || key == "" || mountPath == "" {
		problems = append(problems, fmt.Sprintf("%s: published chatTemplate needs configMap, key and mountPath", where))
		return problems
	}
	if cm, ok := configMaps[servingNamespace+"/"+configMap]; ok {
		if _, ok := cm.Data[key]; !ok {
			problems = append(problems, fmt.Sprintf("%s: chat template ConfigMap %s has no key %s", where, configMap, key))
		}
		if cm.Metadata.Labels[assetLabel] != "chat-template" {
			problems = append(problems, fmt.Sprintf("%s: chat template ConfigMap %s lacks label %s=chat-template", where, configMap, assetLabel))
		}
	} else {
		fmt.Printf("    %s: chat template ConfigMap %s/%s is not in the render (existingConfigMap)\n", where, servingNamespace, configMap)
	}
	args, _ := lookup(preset, "spec", "args")
	list, _ := args.([]any)
	want := fmt.Sprintf("--chat-template=%s/%s", mountPath, key)
	if len(list) == 0 || list[len(list)-1] != want {
		problems = append(problems, fmt.Sprintf("%s: args must end with %s", where, want))
	}
	return problems
}

// yamlToJSON decodes YAML into the Go shape json.Unmarshal produces, which is
// what the schema validator expects.
func yamlToJSON(raw []byte) (any, error) {
	var value any
	if err := yaml.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var document any
	if err := json.Unmarshal(encoded, &document); err != nil {
		return nil, err
	}
	if document == nil {
		return nil, errors.New("empty document")
	}
	return document, nil
}

func lookup(document map[string]any, path ...string) (any, bool) {
	var current any = document
	for _, key := range path {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func field(document map[string]any, path ...string) string {
	value, ok := lookup(document, path...)
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}
