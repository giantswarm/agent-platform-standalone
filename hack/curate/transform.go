package main

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Inputs are the documents the transform reads.
type Inputs struct {
	Config       *Config
	Fleet        *yaml.Node
	Connectivity *yaml.Node
	Overlay      *yaml.Node
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
	// Moves records every values path that changed place, for the template
	// rewrite. ValueKeys are the top-level keys the generated values carry.
	Moves     []PathMove
	ValueKeys []string
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

	// The toggle default of a fleet dependency is the fleet's own
	// components.<name>.enabled, the single on/off switch. An extra dependency
	// has no fleet entry, so curate.yaml carries its default.
	entries := map[string]*componentEntry{}
	for _, dependency := range config.Dependencies {
		entry := &componentEntry{enabled: true}
		if dependency.IsExtra() {
			entry.enabled = *dependency.Enabled
		} else if component, ok := fleetComponentByChart(components, dependency.Name); ok {
			entry.enabled = component.IsEnabled()
		}
		entries[dependency.Name] = entry
	}

	var kept []namedNode
	var moves []PathMove
	blocks := map[string]namedNode{}
	keepEnabledCharts := map[string]bool{}

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
		case ActionDependencies:
		case ActionWiring:
			// The wiring copy comes from the connectivity values; a fleet copy
			// is a shadow that would otherwise drift apart unnoticed.
			if !rule.AllowShadow {
				return nil, fmt.Errorf("fleet values carry %q, whose wiring block is copied from the connectivity chart; "+
					"set allowShadow: true on keys.%s to discard the fleet copy deliberately (deny-unknown)", key, key)
			}
		case ActionDrop:
			moves = append(moves, PathMove{From: []string{key}, Dropped: true})
		case ActionComponent:
			component, ok := fleetComponentByValuesFrom(components, key)
			if !ok {
				return nil, fmt.Errorf("keys.%s: action component, but no fleet component has valuesFrom %q", key, key)
			}
			block, err := componentBlock(keyNode, value, component, rule, entries[component.Chart])
			if err != nil {
				return nil, err
			}
			blocks[component.Chart] = block
			moves = append(moves, componentMoves(key, component, rule)...)
			if rule.KeepEnabled {
				keepEnabledCharts[component.Chart] = true
			}
		case ActionLift:
			if err := liftBlock(key, value, rule, entries[rule.Chart]); err != nil {
				return nil, err
			}
			moves = append(moves, liftMoves(key, rule)...)
		}
	}

	var wiring []namedNode
	for i := 0; i+1 < len(connectivityRoot.Content); i += 2 {
		keyNode, value := connectivityRoot.Content[i], connectivityRoot.Content[i+1]
		rule, ok := config.Keys[keyNode.Value]
		if !ok {
			return nil, fmt.Errorf("connectivity key %q has no entry in the curate.yaml keys map (deny-unknown)", keyNode.Value)
		}
		if rule.Action == ActionWiring {
			wiring = append(wiring, namedNode{key: cleanKey(keyNode), value: value})
			continue
		}
		// Every other rule reads the fleet values; a connectivity copy is a
		// shadow that would otherwise be discarded silently, drift included.
		if !rule.AllowShadow {
			return nil, fmt.Errorf("connectivity values carry %q, whose rule (action %s) reads the fleet values; "+
				"set allowShadow: true on keys.%s to discard the connectivity copy deliberately (deny-unknown)", keyNode.Value, rule.Action, keyNode.Value)
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
	for _, node := range wiring {
		mappingSet(out, node.key, node.value)
	}
	for _, dependency := range config.Dependencies {
		block, ok := blocks[dependency.Name]
		if !ok {
			block = emptyBlock(dependency.Name)
		}
		mappingSet(out, block.key, block.value)
	}

	if err := applyOverlay(config, out, in.Overlay, keepEnabledCharts); err != nil {
		return nil, err
	}
	if err := applyAnnotations(config, out); err != nil {
		return nil, err
	}

	document := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{out}}
	document.HeadComment = fmt.Sprintf("GENERATED by hack/curate.sh from %s %s and %s %s.\n"+
		"Do not edit. Change curate.yaml (dependencies, key rules) or overlay/vanilla.yaml\n"+
		"(vanilla overrides) and run hack/curate.sh again.",
		config.Fleet.Chart, config.Fleet.Version, config.Fleet.ConnectivityChart, config.Fleet.Version)
	return &Result{Document: document, Components: components, Moves: moves, ValueKeys: mappingKeys(out)}, nil
}

// componentMoves records where a component block's values went: each lifted key
// into components.<chart>, the rest under the chart name and, when the fleet
// forwards the block nested, under the wrapper's subchart key.
func componentMoves(key string, component FleetComponent, rule KeyRule) []PathMove {
	moves := make([]PathMove, 0, len(rule.Lift)+1)
	for _, lifted := range rule.Lift {
		moves = append(moves, PathMove{From: []string{key, lifted}, To: []string{"components", component.Chart, lifted}})
	}
	to := []string{component.Chart}
	if component.ValuesKey != "" {
		to = append(to, component.ValuesKey)
	}
	return append(moves, PathMove{From: []string{key}, To: to})
}

// liftMoves records where a lift block's keys went: all of them into
// components.<chart>, which is also where the block's toggle lives.
func liftMoves(key string, rule KeyRule) []PathMove {
	moves := make([]PathMove, 0, len(rule.Lift)+1)
	for _, lifted := range rule.Lift {
		moves = append(moves, PathMove{From: []string{key, lifted}, To: []string{"components", rule.Chart, lifted}})
	}
	return append(moves, PathMove{From: []string{key}, To: []string{"components", rule.Chart}})
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
// block: lifted keys moved to components.<chart>, the rest nested under
// valuesKey when the fleet forwards the block nested.
func componentBlock(keyNode *yaml.Node, value *yaml.Node, component FleetComponent, rule KeyRule, entry *componentEntry) (namedNode, error) {
	if value.Kind != yaml.MappingNode {
		return namedNode{}, fmt.Errorf("fleet block %q is not a mapping", keyNode.Value)
	}
	if !rule.KeepEnabled {
		if err := rejectBlockToggle(keyNode.Value, value, component.Chart); err != nil {
			return namedNode{}, err
		}
	}
	if err := lift(value, keyNode.Value, rule.Lift, entry); err != nil {
		return namedNode{}, err
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
	return namedNode{key: outKey, value: block}, nil
}

// liftBlock consumes a fleet block that carries no component values: every
// sub-key is umbrella wiring and moves to components.<chart>. Any other
// sub-key is an error.
func liftBlock(key string, value *yaml.Node, rule KeyRule, entry *componentEntry) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("fleet block %q is not a mapping", key)
	}
	if err := rejectBlockToggle(key, value, rule.Chart); err != nil {
		return err
	}
	if err := lift(value, key, rule.Lift, entry); err != nil {
		return err
	}
	if remaining := mappingKeys(value); len(remaining) > 0 {
		return fmt.Errorf("keys.%s: sub-keys %v are not listed in lift (deny-unknown)", key, remaining)
	}
	return nil
}

// rejectBlockToggle fails on a fleet block that carries a top-level `enabled`.
// components.<chart>.enabled is the single on/off switch, and these blocks
// accept unknown keys, so a second toggle here would be read by nothing while
// it looks authoritative.
func rejectBlockToggle(key string, block *yaml.Node, chart string) error {
	if _, enabled := mappingGet(block, "enabled"); enabled == nil {
		return nil
	}
	return fmt.Errorf("fleet block %q carries a top-level `enabled`, which components.%s.enabled replaced; "+
		"set keepEnabled on keys.%s when the %s chart owns a value of that name", key, chart, key, chart)
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

// applyOverlay merges overlay/vanilla.yaml on top. Only umbrella-owned keys
// and dependency names are allowed at the top level, and a component block
// must not smuggle back the top-level `enabled` toggle the transform rejects
// in the fleet values (the overlay merges after that guard ran).
func applyOverlay(config *Config, out *yaml.Node, overlay *yaml.Node, keepEnabledCharts map[string]bool) error {
	if overlay == nil {
		return nil
	}
	allowed := map[string]bool{"components": true}
	for key, rule := range config.Keys {
		if rule.Action == ActionKeep || rule.Action == ActionWiring {
			allowed[key] = true
		}
	}
	for _, dependency := range config.Dependencies {
		allowed[dependency.Name] = true
	}
	overlayRoot := rootMapping(overlay)
	for _, key := range mappingKeys(overlayRoot) {
		if !allowed[key] {
			return fmt.Errorf("overlay key %q is neither umbrella-owned nor a dependency name (deny-unknown)", key)
		}
	}
	for _, dependency := range config.Dependencies {
		_, block := mappingGet(overlayRoot, dependency.Name)
		if block == nil || block.Kind != yaml.MappingNode || keepEnabledCharts[dependency.Name] {
			continue
		}
		if _, enabled := mappingGet(block, "enabled"); enabled != nil {
			return fmt.Errorf("overlay block %q carries a top-level `enabled`, which components.%s.enabled replaced; "+
				"set components.%s.enabled in the overlay instead", dependency.Name, dependency.Name, dependency.Name)
		}
	}
	if _, components := mappingGet(overlayRoot, "components"); components != nil {
		if components.Kind != yaml.MappingNode {
			return fmt.Errorf("overlay components must be a mapping")
		}
		for _, name := range mappingKeys(components) {
			if _, ok := config.Dependency(name); !ok {
				return fmt.Errorf("overlay components.%s is not a dependency", name)
			}
		}
	}
	deepMerge(out, overlayRoot)
	return nil
}

// applyAnnotations injects the configured `# @schema` line comments into the
// generated values. A missing path or a key that already carries a different
// line comment fails the run, so a fleet layout change never silently drops
// or fights a schema constraint.
func applyAnnotations(config *Config, out *yaml.Node) error {
	paths := make([]string, 0, len(config.Annotations))
	for path := range config.Annotations {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		key, value, err := pathGetPair(out, path)
		if err != nil {
			return fmt.Errorf("annotations.%s: %w", path, err)
		}
		// yaml.v3 renders a block value's line comment from the key node and a
		// scalar or flow value's from the value node (see emptyBlock).
		node := key
		if value.Kind == yaml.ScalarNode || value.Style == yaml.FlowStyle {
			node = value
		}
		annotation := "# @schema " + config.Annotations[path]
		if node.LineComment != "" && node.LineComment != annotation {
			return fmt.Errorf("annotations.%s: the key already carries the line comment %q", path, node.LineComment)
		}
		node.LineComment = annotation
	}
	return nil
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
