package main

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Inputs are the documents the transform reads.
type Inputs struct {
	Config       *Config
	Fleet        *yaml.Node
	Connectivity *yaml.Node
	// Contract is overlay/contract.yaml: the umbrella's input contract (new
	// global.* keys, umbrella-only keys, wiring the umbrella templates need
	// inside component blocks). Always applied; the generated Giant Swarm
	// example never reverts it.
	Contract *yaml.Node
	// Overlay is overlay/vanilla.yaml: the fleet defaults a vanilla cluster
	// turns off. The generated Giant Swarm example reverts every leaf of it.
	Overlay *yaml.Node
	// GiantswarmInputs is overlay/giantswarm.yaml. It never merges into the
	// values document (GiantswarmExample consumes it), but it obeys the same
	// key rules, so a typo fails here instead of leaking into the example.
	GiantswarmInputs *yaml.Node
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
	// BeforeOverlay is the values mapping after the contract but before the
	// vanilla overlay merged: the fleet defaults in the umbrella layout, the
	// input of the generated Giant Swarm example.
	BeforeOverlay *yaml.Node
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
		case ActionUmbrella:
			return nil, fmt.Errorf("fleet key %q is declared umbrella-only in curate.yaml but the fleet values define it", key)
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
		switch rule.Action {
		case ActionWiring:
			wiring = append(wiring, namedNode{key: cleanKey(keyNode), value: value})
			if rule.MoveTo != "" {
				moves = append(moves, PathMove{From: []string{keyNode.Value}, To: strings.Split(rule.MoveTo, ".")})
			}
			continue
		case ActionUmbrella:
			return nil, fmt.Errorf("connectivity key %q is declared umbrella-only in curate.yaml but the connectivity values define it", keyNode.Value)
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
	componentsKey.HeadComment = "Umbrella-owned map: one entry per dependency, keyed by chart name, plus\n" +
		"the components the umbrella renders itself (no dependency; their whole\n" +
		"block comes from overlay/contract.yaml). `enabled` is the Helm dependency\n" +
		"condition, or the component's own switch. The other keys are wiring the\n" +
		"umbrella templates render for that component; they are never forwarded to\n" +
		"the component chart."
	mappingSet(out, componentsKey, buildComponents(config, entries))
	for _, node := range wiring {
		if config.Keys[node.key.Value].MoveTo == "" {
			mappingSet(out, node.key, node.value)
		}
	}
	// The relocations run after the kept and wiring keys are placed, so a
	// moveTo path below one of them always finds its parent; the config
	// validation pins the first segment to an umbrella-owned key, so the
	// dependency blocks placed next can never replace a relocated value.
	for _, node := range wiring {
		moveTo := config.Keys[node.key.Value].MoveTo
		if moveTo == "" {
			continue
		}
		if err := pathSet(out, moveTo, node.key, node.value); err != nil {
			return nil, fmt.Errorf("keys.%s: moveTo: %w", node.key.Value, err)
		}
	}
	for _, dependency := range config.Dependencies {
		block, ok := blocks[dependency.Name]
		if !ok {
			block = emptyBlock(dependency.Name)
		}
		mappingSet(out, block.key, block.value)
	}

	if err := mergeOverlay(config, out, in.Contract, "contract", keepEnabledCharts); err != nil {
		return nil, err
	}
	beforeOverlay := cloneNode(out)
	if err := checkVanillaPaths(config, out, in.Overlay); err != nil {
		return nil, err
	}
	if err := mergeOverlay(config, out, in.Overlay, "overlay", keepEnabledCharts); err != nil {
		return nil, err
	}
	if err := validateOverlay(config, in.GiantswarmInputs, "giantswarm inputs", keepEnabledCharts); err != nil {
		return nil, err
	}
	if err := finishOverlays(config, out); err != nil {
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
	return &Result{Document: document, Components: components, Moves: moves, ValueKeys: mappingKeys(out), BeforeOverlay: beforeOverlay}, nil
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

// mergeOverlay merges one overlay document on top of out.
func mergeOverlay(config *Config, out *yaml.Node, overlay *yaml.Node, what string, keepEnabledCharts map[string]bool) error {
	if overlay == nil {
		return nil
	}
	if err := validateOverlay(config, overlay, what, keepEnabledCharts); err != nil {
		return err
	}
	// The overlay documents are read again by the Giant Swarm example
	// generator, so the merge must not alias their nodes into the output.
	deepMerge(out, cloneNode(rootMapping(overlay)))
	return nil
}

// validateOverlay allows only umbrella-owned keys and dependency names at an
// overlay's top level, and rejects a component block that smuggles back the
// top-level `enabled` toggle the transform rejects in the fleet values (the
// overlays merge after that guard ran).
func validateOverlay(config *Config, overlay *yaml.Node, what string, keepEnabledCharts map[string]bool) error {
	if overlay == nil {
		return nil
	}
	overlayRoot := rootMapping(overlay)
	for _, key := range mappingKeys(overlayRoot) {
		if _, isDependency := config.Dependency(key); !isDependency && !config.UmbrellaOwned(key) {
			return fmt.Errorf("%s key %q is neither umbrella-owned nor a dependency name (deny-unknown)", what, key)
		}
	}
	for _, dependency := range config.Dependencies {
		_, block := mappingGet(overlayRoot, dependency.Name)
		if block == nil || block.Kind != yaml.MappingNode || keepEnabledCharts[dependency.Name] {
			continue
		}
		if _, enabled := mappingGet(block, "enabled"); enabled != nil {
			return fmt.Errorf("%s block %q carries a top-level `enabled`, which components.%s.enabled replaced; "+
				"set components.%s.enabled in the %s instead", what, dependency.Name, dependency.Name, dependency.Name, what)
		}
	}
	if _, components := mappingGet(overlayRoot, "components"); components != nil {
		if components.Kind != yaml.MappingNode {
			return fmt.Errorf("%s components must be a mapping", what)
		}
		for _, name := range mappingKeys(components) {
			if _, ok := config.Dependency(name); !ok && !config.IsUmbrellaComponent(name) {
				return fmt.Errorf("%s components.%s is not a dependency (nor an umbrella component declared in curate.yaml umbrellaComponents)", what, name)
			}
		}
	}
	return nil
}

// checkVanillaPaths fails when a vanilla-overlay leaf under an umbrella-owned
// top-level key names a path the fleet-derived values (contract included) do
// not carry: the vanilla overlay only flips existing defaults, so a fleet
// rename would otherwise orphan the override and silently turn the default
// back on. Dependency blocks are exempt — they are forwarded opaquely to
// component charts whose defaults this generator cannot see. A key or
// components entry validateOverlay rejects is skipped here, keeping its
// sharper message.
func checkVanillaPaths(config *Config, out *yaml.Node, overlay *yaml.Node) error {
	if overlay == nil {
		return nil
	}
	overlayRoot := rootMapping(overlay)
	for i := 0; i+1 < len(overlayRoot.Content); i += 2 {
		key, value := overlayRoot.Content[i], overlayRoot.Content[i+1]
		if !config.UmbrellaOwned(key.Value) {
			continue
		}
		if key.Value == "components" && value.Kind == yaml.MappingNode {
			_, existing := mappingGet(out, "components")
			for j := 0; j+1 < len(value.Content); j += 2 {
				name, block := value.Content[j], value.Content[j+1]
				if _, isDependency := config.Dependency(name.Value); !isDependency && !config.IsUmbrellaComponent(name.Value) {
					continue
				}
				_, existingEntry := mappingGet(existing, name.Value)
				if err := checkOverlayLeaves("components."+name.Value, block, existingEntry); err != nil {
					return err
				}
			}
			continue
		}
		_, existing := mappingGet(out, key.Value)
		if err := checkOverlayLeaves(key.Value, value, existing); err != nil {
			return err
		}
	}
	return nil
}

func checkOverlayLeaves(path string, value, existing *yaml.Node) error {
	if existing == nil {
		return fmt.Errorf("overlay path %q does not exist in the fleet-derived values; "+
			"the vanilla overlay only flips existing defaults, new inputs belong in overlay/contract.yaml", path)
	}
	if value.Kind != yaml.MappingNode || len(value.Content) == 0 {
		return nil
	}
	if existing.Kind != yaml.MappingNode {
		return fmt.Errorf("overlay path %q is a mapping but the fleet-derived value is not", path)
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		key, child := value.Content[i], value.Content[i+1]
		_, next := mappingGet(existing, key.Value)
		if err := checkOverlayLeaves(path+"."+key.Value, child, next); err != nil {
			return err
		}
	}
	return nil
}

// finishOverlays runs after the overlays merged: every umbrella-only key
// (action umbrella) must have been defined by one of them — the source charts
// are forbidden from defining it, so presence in the output is presence in an
// overlay — every umbrella component must carry its toggle, and the output
// keeps the order kept keys, components, wiring, umbrella keys, dependency
// blocks.
func finishOverlays(config *Config, out *yaml.Node) error {
	var umbrellaKeys []string
	for _, key := range config.SortedKeys() {
		if config.Keys[key].Action != ActionUmbrella {
			continue
		}
		if _, value := mappingGet(out, key); value == nil {
			return fmt.Errorf("keys.%s: action umbrella, but neither overlay defines it", key)
		}
		umbrellaKeys = append(umbrellaKeys, key)
	}
	if err := checkUmbrellaComponents(config, out); err != nil {
		return err
	}
	reorderTopLevel(config, out, umbrellaKeys)
	return nil
}

// checkUmbrellaComponents fails when an umbrella component (curate.yaml
// umbrellaComponents) has no components.<name> block with a boolean `enabled`:
// the block is the overlays' to define, and a component without its switch
// would be read by templates that can never turn it off.
func checkUmbrellaComponents(config *Config, out *yaml.Node) error {
	_, components := mappingGet(out, "components")
	for _, name := range config.UmbrellaComponents {
		_, block := mappingGet(components, name)
		if block == nil || block.Kind != yaml.MappingNode {
			return fmt.Errorf("umbrellaComponents: components.%s must be defined as a mapping by overlay/contract.yaml", name)
		}
		_, enabled := mappingGet(block, "enabled")
		if enabled == nil {
			return fmt.Errorf("umbrellaComponents: components.%s.enabled must be defined by overlay/contract.yaml (the switch of a component without a dependency)", name)
		}
		if _, err := scalarBool(enabled, fmt.Sprintf("components.%s.enabled", name)); err != nil {
			return fmt.Errorf("umbrellaComponents: %w", err)
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
