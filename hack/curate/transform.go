package main

import (
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Inputs are the documents the transform reads.
type Inputs struct {
	Config       *Config
	Fleet        *yaml.Node
	Connectivity *yaml.Node
	// Contract is overlay/contract.yaml: the umbrella's input contract (new
	// global.* keys, umbrella-only keys, wiring the umbrella templates need).
	// Always applied; the Giant Swarm example never reverts it.
	Contract *yaml.Node
	// Overlay is overlay/vanilla.yaml: the fleet defaults a vanilla cluster
	// turns off. The Giant Swarm example reverts every leaf of it.
	Overlay *yaml.Node
}

type namedNode struct {
	key   *yaml.Node
	value *yaml.Node
}

// componentEntry accumulates the umbrella-owned `components.<chart>` block.
type componentEntry struct {
	enabled bool
	lifted  []namedNode
}

// Result is the output of Transform.
type Result struct {
	// Document is the generated values.yaml.
	Document *yaml.Node
	// Components is the fleet component list the dependency list was built from.
	Components []FleetComponent
	// BeforeOverlay is the values mapping before the overlay was merged: the
	// fleet defaults in the umbrella layout, the input of the Giant Swarm example.
	BeforeOverlay *yaml.Node
}

type wiringBlock struct {
	node   namedNode
	moveTo string
}

// Transform derives the umbrella values document from the fleet and
// connectivity values. Every top-level key of both inputs must have a rule in
// the config (deny-unknown); an unmapped key is an error, never a silent leak.
func Transform(in Inputs) (*Result, error) {
	config := in.Config
	fleetRoot := rootMapping(in.Fleet)
	connectivityRoot := rootMapping(in.Connectivity)

	components, err := readFleetComponents(config, fleetRoot)
	if err != nil {
		return nil, err
	}
	if err := checkDependencyList(config, components); err != nil {
		return nil, err
	}

	// Toggle defaults are read before any block is mutated: a toggle block
	// (mcps.enabled) can precede the component block that points at it.
	toggles := map[string]bool{}
	for _, component := range components {
		if component.EnabledFrom == "" {
			continue
		}
		node, err := pathGet(fleetRoot, component.EnabledFrom)
		if err != nil {
			return nil, fmt.Errorf("fleet component %q: enabledFrom: %w", component.Chart, err)
		}
		if toggles[component.Chart], err = scalarBool(node, component.EnabledFrom); err != nil {
			return nil, err
		}
	}

	entries := map[string]*componentEntry{}
	for _, dependency := range config.Dependencies {
		entries[dependency.Name] = &componentEntry{enabled: true}
		if dependency.IsExtra() {
			entries[dependency.Name].enabled = *dependency.Enabled
		}
	}

	var kept []namedNode
	blocks := map[string]namedNode{}

	for i := 0; i+1 < len(fleetRoot.Content); i += 2 {
		keyNode, value := fleetRoot.Content[i], fleetRoot.Content[i+1]
		key := keyNode.Value
		rule, ok := config.Keys[key]
		if !ok {
			return nil, fmt.Errorf("fleet key %q has no entry in the curate.yaml keys map (deny-unknown)", key)
		}
		switch rule.Action {
		case ActionKeep:
			kept = append(kept, namedNode{key: cleanKey(keyNode), value: value})
		case ActionDrop, ActionDependencies, ActionWiring:
		case ActionUmbrella:
			return nil, fmt.Errorf("fleet key %q is declared umbrella-only in curate.yaml but the fleet values define it", key)
		case ActionComponent:
			component, ok := fleetComponentByValuesFrom(components, key)
			if !ok {
				return nil, fmt.Errorf("keys.%s: action component, but no fleet component has valuesFrom %q", key, key)
			}
			block, enabled, err := componentBlock(keyNode, value, component, rule, entries[component.Chart], toggles)
			if err != nil {
				return nil, err
			}
			blocks[component.Chart] = block
			entries[component.Chart].enabled = enabled
		case ActionToggle:
			component, ok := fleetComponentByToggleKey(components, key)
			if !ok {
				return nil, fmt.Errorf("keys.%s: action toggle, but no fleet component has enabledFrom under %q", key, key)
			}
			if err := toggleBlock(key, value, component, rule, entries[component.Chart]); err != nil {
				return nil, err
			}
			entries[component.Chart].enabled = toggles[component.Chart]
		}
	}

	var wiring []wiringBlock
	for i := 0; i+1 < len(connectivityRoot.Content); i += 2 {
		keyNode, value := connectivityRoot.Content[i], connectivityRoot.Content[i+1]
		rule, ok := config.Keys[keyNode.Value]
		if !ok {
			return nil, fmt.Errorf("connectivity key %q has no entry in the curate.yaml keys map (deny-unknown)", keyNode.Value)
		}
		switch rule.Action {
		case ActionWiring:
			wiring = append(wiring, wiringBlock{node: namedNode{key: cleanKey(keyNode), value: value}, moveTo: rule.MoveTo})
		case ActionUmbrella:
			return nil, fmt.Errorf("connectivity key %q is declared umbrella-only in curate.yaml but the connectivity values define it", keyNode.Value)
		}
	}
	for key, rule := range config.Keys {
		if rule.Action != ActionWiring {
			continue
		}
		if _, found := mappingGet(connectivityRoot, key); found == nil {
			return nil, fmt.Errorf("keys.%s: action wiring, but the connectivity values have no such key", key)
		}
	}

	out := newMapping()
	for _, node := range kept {
		mappingSet(out, node.key, node.value)
	}
	componentsKey := newScalar("components")
	componentsKey.HeadComment = "Umbrella-owned map: one entry per dependency, keyed by chart name.\n" +
		"`enabled` is the Helm dependency condition. The other keys are wiring the\n" +
		"umbrella templates render for that component; they are never forwarded to\n" +
		"the component chart."
	mappingSet(out, componentsKey, buildComponents(config, entries))
	for _, block := range wiring {
		if block.moveTo == "" {
			mappingSet(out, block.node.key, block.node.value)
		}
	}
	for _, block := range wiring {
		if block.moveTo == "" {
			continue
		}
		if err := pathSet(out, block.moveTo, block.node); err != nil {
			return nil, fmt.Errorf("keys.%s: moveTo: %w", block.node.key.Value, err)
		}
	}
	for _, dependency := range config.Dependencies {
		block, ok := blocks[dependency.Name]
		if !ok {
			block = emptyBlock(dependency.Name)
		}
		mappingSet(out, block.key, block.value)
	}

	if err := mergeOverlay(config, out, in.Contract, "contract"); err != nil {
		return nil, err
	}
	beforeOverlay := cloneNode(out)
	if err := mergeOverlay(config, out, in.Overlay, "overlay"); err != nil {
		return nil, err
	}
	if err := finishOverlays(config, out, in.Contract, in.Overlay); err != nil {
		return nil, err
	}

	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{out}}
	document.HeadComment = fmt.Sprintf("GENERATED by hack/curate.sh from %s %s and %s %s.\n"+
		"Do not edit. Change curate.yaml (dependencies, key rules) or overlay/vanilla.yaml\n"+
		"(vanilla overrides) and run hack/curate.sh again.",
		config.Fleet.Chart, config.Fleet.Version, config.Fleet.ConnectivityChart, config.Fleet.Version)
	return &Result{Document: document, Components: components, BeforeOverlay: beforeOverlay}, nil
}

// pathSet stores node under the dotted path in root. Intermediate mappings
// are created; the last segment becomes the key name, the original key node
// keeps its comments.
func pathSet(root *yaml.Node, path string, node namedNode) error {
	segments := strings.Split(path, ".")
	current := root
	for _, segment := range segments[:len(segments)-1] {
		_, next := mappingGet(current, segment)
		if next == nil {
			next = newMapping()
			mappingSet(current, newScalar(segment), next)
		}
		if next.Kind != yaml.MappingNode {
			return fmt.Errorf("%q is not a mapping", segment)
		}
		current = next
	}
	key := cleanKey(node.key)
	key.Value = segments[len(segments)-1]
	mappingSet(current, key, node.value)
	return nil
}

func readFleetComponents(config *Config, fleetRoot *yaml.Node) ([]FleetComponent, error) {
	for key, rule := range config.Keys {
		if rule.Action != ActionDependencies {
			continue
		}
		_, value := mappingGet(fleetRoot, key)
		if value == nil {
			return nil, fmt.Errorf("fleet values have no %q key (action dependencies)", key)
		}
		return parseFleetComponents(value)
	}
	return nil, fmt.Errorf("no key with action %q", ActionDependencies)
}

// checkDependencyList fails when the fleet component list and the configured
// dependencies disagree, so a new fleet component never slips by unnoticed.
func checkDependencyList(config *Config, components []FleetComponent) error {
	inline := map[string]bool{}
	for _, name := range config.Fleet.InlineComponents {
		inline[name] = true
	}
	for _, component := range components {
		if inline[component.Chart] {
			continue
		}
		dependency, ok := config.Dependency(component.Chart)
		if !ok {
			return fmt.Errorf("fleet component %q (chart %q) is not a dependency in curate.yaml", component.Key, component.Chart)
		}
		if dependency.IsExtra() {
			return fmt.Errorf("dependency %q is declared extra but the fleet ships it as a component", dependency.Name)
		}
	}
	for _, dependency := range config.Dependencies {
		if dependency.IsExtra() {
			continue
		}
		if _, ok := fleetComponentByChart(components, dependency.Name); !ok {
			return fmt.Errorf("dependency %q is not a fleet component; declare a repository to make it an extra dependency", dependency.Name)
		}
	}
	return nil
}

// componentBlock turns a fleet component values block into the dependency's
// block: `enabled` stripped, lifted keys moved to components.<chart>, the rest
// nested under valuesKey when the fleet forwards the block nested.
func componentBlock(keyNode *yaml.Node, value *yaml.Node, component FleetComponent, rule KeyRule, entry *componentEntry, toggles map[string]bool) (namedNode, bool, error) {
	if value.Kind != yaml.MappingNode {
		return namedNode{}, false, fmt.Errorf("fleet block %q is not a mapping", keyNode.Value)
	}
	enabled := true
	if component.EnabledFrom != "" {
		enabled = toggles[component.Chart]
	}
	if _, enabledNode := mappingDelete(value, "enabled"); enabledNode != nil && component.EnabledFrom == "" {
		var err error
		if enabled, err = scalarBool(enabledNode, keyNode.Value+".enabled"); err != nil {
			return namedNode{}, false, err
		}
	}
	if err := lift(value, keyNode.Value, rule.Lift, entry); err != nil {
		return namedNode{}, false, err
	}
	outKey := newScalar(component.Chart)
	outKey.HeadComment = keyNode.HeadComment
	outKey.LineComment = keyNode.LineComment
	if !strings.Contains(outKey.LineComment, "@schema") {
		outKey.LineComment = opaqueBlockAnnotation
	}
	block := value
	if component.ValuesKey != "" {
		block = newMapping()
		inner := newScalar(component.ValuesKey)
		inner.LineComment = fmt.Sprintf("# the %s chart vendors upstream as a subchart of this name; Helm hands it only values under this key", component.Chart)
		mappingSet(block, inner, value)
	}
	return namedNode{key: outKey, value: block}, enabled, nil
}

// toggleBlock consumes a fleet block that carries only a component's on/off
// switch plus lifted umbrella wiring. Any other sub-key is an error.
func toggleBlock(key string, value *yaml.Node, component FleetComponent, rule KeyRule, entry *componentEntry) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("fleet block %q is not a mapping", key)
	}
	if component.EnabledFrom != key+".enabled" {
		return fmt.Errorf("keys.%s: fleet component %q reads its toggle from %q, expected %q", key, component.Chart, component.EnabledFrom, key+".enabled")
	}
	mappingDelete(value, "enabled")
	if err := lift(value, key, rule.Lift, entry); err != nil {
		return err
	}
	if remaining := mappingKeys(value); len(remaining) > 0 {
		return fmt.Errorf("keys.%s: sub-keys %v are neither `enabled` nor listed in lift (deny-unknown)", key, remaining)
	}
	return nil
}

func lift(block *yaml.Node, source string, keys []string, entry *componentEntry) error {
	for _, key := range keys {
		keyNode, value := mappingDelete(block, key)
		if keyNode == nil {
			return fmt.Errorf("keys.%s: lift key %q not found in the fleet block", source, key)
		}
		entry.lifted = append(entry.lifted, namedNode{key: cleanKey(keyNode), value: value})
	}
	return nil
}

func buildComponents(config *Config, entries map[string]*componentEntry) *yaml.Node {
	components := newMapping()
	for _, dependency := range config.Dependencies {
		entry := entries[dependency.Name]
		mapping := newMapping()
		mappingSet(mapping, newScalar("enabled"), newBool(entry.enabled))
		for _, node := range entry.lifted {
			mappingSet(mapping, node.key, node.value)
		}
		mappingSet(components, newScalar(dependency.Name), mapping)
	}
	return components
}

// mergeOverlay merges one overlay document on top of out. Only umbrella-owned
// keys and dependency names are allowed at the top level.
func mergeOverlay(config *Config, out *yaml.Node, overlay *yaml.Node, what string) error {
	if overlay == nil {
		return nil
	}
	allowed := map[string]bool{"components": true}
	for key, rule := range config.Keys {
		if rule.Action == ActionKeep || rule.Action == ActionWiring || rule.Action == ActionUmbrella {
			allowed[key] = true
		}
	}
	for _, dependency := range config.Dependencies {
		allowed[dependency.Name] = true
	}
	overlayRoot := rootMapping(overlay)
	for _, key := range mappingKeys(overlayRoot) {
		if !allowed[key] {
			return fmt.Errorf("%s key %q is neither umbrella-owned nor a dependency name (deny-unknown)", what, key)
		}
	}
	if _, components := mappingGet(overlayRoot, "components"); components != nil {
		if components.Kind != yaml.MappingNode {
			return fmt.Errorf("%s components must be a mapping", what)
		}
		for _, name := range mappingKeys(components) {
			if _, ok := config.Dependency(name); !ok {
				return fmt.Errorf("%s components.%s is not a dependency", what, name)
			}
		}
	}
	deepMerge(out, cloneNode(overlayRoot))
	return nil
}

// finishOverlays runs after both overlays merged: every umbrella-only key
// (action umbrella) must be defined by one of them, the output keeps the order
// kept keys, components, wiring, umbrella keys, dependency blocks, and a
// dependency block that received values is written in block style.
func finishOverlays(config *Config, out *yaml.Node, overlays ...*yaml.Node) error {
	var umbrellaKeys []string
	for _, overlay := range overlays {
		if overlay == nil {
			continue
		}
		for _, key := range mappingKeys(rootMapping(overlay)) {
			if config.Keys[key].Action == ActionUmbrella && !slices.Contains(umbrellaKeys, key) {
				umbrellaKeys = append(umbrellaKeys, key)
			}
		}
	}
	for _, key := range config.SortedKeys() {
		if config.Keys[key].Action == ActionUmbrella && !slices.Contains(umbrellaKeys, key) {
			return fmt.Errorf("keys.%s: action umbrella, but neither overlay defines it", key)
		}
	}
	reorderTopLevel(config, out, umbrellaKeys)
	for _, dependency := range config.Dependencies {
		keyNode, value := mappingGet(out, dependency.Name)
		if value != nil && value.Kind == yaml.MappingNode && len(value.Content) > 0 && value.Style == yaml.FlowStyle {
			value.Style = 0
			keyNode.LineComment = value.LineComment
			value.LineComment = ""
		}
	}
	return nil
}

// reorderTopLevel puts the umbrella-only keys right after the wiring blocks,
// where deepMerge appended them after the dependency blocks.
func reorderTopLevel(config *Config, out *yaml.Node, umbrellaKeys []string) {
	dependencyNames := map[string]bool{}
	for _, dependency := range config.Dependencies {
		dependencyNames[dependency.Name] = true
	}
	var head, umbrella, tail []*yaml.Node
	for i := 0; i+1 < len(out.Content); i += 2 {
		pair := out.Content[i : i+2]
		switch key := out.Content[i].Value; {
		case slices.Contains(umbrellaKeys, key):
			umbrella = append(umbrella, pair...)
		case dependencyNames[key]:
			tail = append(tail, pair...)
		default:
			head = append(head, pair...)
		}
	}
	out.Content = slices.Concat(head, umbrella, tail)
}

// opaqueBlockAnnotation tells helm-values-schema-json (the GS pre-commit hook
// that generates values.schema.json) to leave a component block open: the
// component chart validates it with its own schema.
const opaqueBlockAnnotation = "# @schema skipProperties: true; additionalProperties: true"

// cleanKey copies a key node so the output never aliases the input tree.
func cleanKey(key *yaml.Node) *yaml.Node {
	clone := *key
	return &clone
}

// emptyBlock is the values block of a dependency the fleet ships no values
// for. It exists so the generated schema knows the key.
func emptyBlock(name string) namedNode {
	key := newScalar(name)
	key.HeadComment = fmt.Sprintf("Values forwarded to the %s chart. The fleet ships no defaults for it.", name)
	block := newMapping()
	block.Style = yaml.FlowStyle
	// yaml.v3 renders the line comment of a `key: {}` pair from the value node.
	block.LineComment = opaqueBlockAnnotation
	return namedNode{key: key, value: block}
}
