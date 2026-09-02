package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Action names a rule for one top-level key of the fleet (or connectivity)
// values.
type Action string

const (
	// ActionKeep copies the fleet block verbatim under the same key.
	ActionKeep Action = "keep"
	// ActionDrop removes the key (fleet-only mechanics such as gitops).
	ActionDrop Action = "drop"
	// ActionDependencies marks the fleet `components` map, the source of the
	// dependency list.
	ActionDependencies Action = "dependencies"
	// ActionWiring copies the block from the connectivity chart's values. The
	// umbrella's own templates read these keys.
	ActionWiring Action = "wiring"
	// ActionComponent turns a fleet component values block into the block of
	// the matching dependency (renamed to the chart name, nested under
	// valuesKey when the fleet forwards it nested).
	ActionComponent Action = "component"
	// ActionLift handles a fleet block that carries no component values at all:
	// every sub-key is umbrella wiring and must be listed in Lift.
	ActionLift Action = "lift"
	// ActionUmbrella declares a top-level key the umbrella owns and neither
	// source chart knows. An overlay must define it; a fleet or connectivity
	// block under the same key fails the run.
	ActionUmbrella Action = "umbrella"
)

type KeyRule struct {
	Action Action `yaml:"action"`
	// Lift lists sub-keys moved out of the block into components.<chart>.
	Lift []string `yaml:"lift"`
	// MoveTo relocates a wiring block under a dotted path of the output, for
	// example global.networkPolicy. The template rewrite follows the move.
	MoveTo string `yaml:"moveTo"`
	// Chart names the dependency a lift block belongs to. Only action lift has
	// no fleet component to derive it from.
	Chart string `yaml:"chart"`
	// KeepEnabled forwards the block's `enabled` key to the component chart,
	// for the one component whose chart owns a value of that name. A top-level
	// `enabled` in any other component block fails the run.
	KeepEnabled bool `yaml:"keepEnabled"`
	// AllowShadow declares that the key also appears in the source chart the
	// rule does not read (a wiring key in the fleet values, any other key in
	// the connectivity values) and that this shadow copy is discarded
	// deliberately. Without it a cross-source copy fails the run, so drift
	// between the two copies never goes unnoticed.
	AllowShadow bool `yaml:"allowShadow"`
}

type Dependency struct {
	Name  string `yaml:"name"`
	Range string `yaml:"range"`
	// Version is the exact pin written into Chart.yaml, bumped by Renovate
	// (the `# registry:` hint above each pin in curate.yaml feeds the
	// org-wide renovate-presets customManager). The generator uses it
	// verbatim and never asks the registry, so a component release cannot
	// change a curation run.
	Version string `yaml:"version"`
	// Repository is set only for an extra dependency (not in the fleet).
	Repository string `yaml:"repository"`
	// Enabled is the default toggle of an extra dependency.
	Enabled *bool `yaml:"enabled"`
	// Condition replaces the generated Helm condition
	// (components.<name>.enabled) for an extra dependency that is one part of
	// an umbrella component — a control plane split into a CRD chart and a
	// controller chart, switched together. The path has the form
	// components.<umbrellaComponent>[.<key>...].enabled; overlay/contract.yaml
	// defines it, and the dependency gets no components.<name> entry of its
	// own (an overlay entry under its name fails the run).
	Condition string `yaml:"condition"`
}

func (d Dependency) IsExtra() bool { return d.Repository != "" }

// HasOwnToggle reports whether the dependency is switched by its generated
// components.<name>.enabled entry (no condition override).
func (d Dependency) HasOwnToggle() bool { return d.Condition == "" }

// ConditionComponent returns the umbrella component a condition override
// belongs to: the second segment of components.<name>....enabled.
func (d Dependency) ConditionComponent() string {
	match := conditionRe.FindStringSubmatch(d.Condition)
	if match == nil {
		return ""
	}
	return match[1]
}

type FleetConfig struct {
	Repository        string   `yaml:"repository"`
	Chart             string   `yaml:"chart"`
	ConnectivityChart string   `yaml:"connectivityChart"`
	Version           string   `yaml:"version"`
	InlineComponents  []string `yaml:"inlineComponents"`
}

// PathRewrite moves a values path in the copied templates. It covers the one
// case the values rules cannot derive: a path whose key exists in both layouts
// while the template must read a different one.
type PathRewrite struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// TemplatePatch edits one generated template where the upstream copy is wrong
// for the standalone layout. Find must occur exactly once in the file AFTER
// all rewrites, so an upstream change that moves the text fails the run
// instead of silently dropping the fix.
type TemplatePatch struct {
	File    string `yaml:"file"`
	Find    string `yaml:"find"`
	Replace string `yaml:"replace"`
}

type TemplatesConfig struct {
	Rewrite []PathRewrite   `yaml:"rewrite"`
	Patch   []TemplatePatch `yaml:"patch"`
	// Extra names files under the chart's templates directory that this
	// generator does not produce and must not delete.
	Extra []string `yaml:"extra"`
}

type Config struct {
	Fleet        FleetConfig        `yaml:"fleet"`
	Chart        yaml.Node          `yaml:"chart"`
	Dependencies []Dependency       `yaml:"dependencies"`
	Keys         map[string]KeyRule `yaml:"keys"`
	Templates    TemplatesConfig    `yaml:"templates"`
	// Annotations injects a `# @schema` line comment onto a key of the
	// generated values.yaml, keyed by its dotted path in the GENERATED layout.
	// The schema pre-commit hook turns them into schema constraints the
	// curated defaults alone would get wrong (frozen array-item shapes,
	// missing enums). A path the generated values do not carry fails the run.
	Annotations map[string]string `yaml:"annotations"`
	// UmbrellaComponents names the components this chart renders itself, with
	// no Helm dependency behind them: hand-authored templates (Templates.Extra)
	// read components.<name>, and overlay/contract.yaml defines the whole
	// block, the `enabled` default included. The generator admits the name
	// into the components map (an overlay entry for any other non-dependency
	// name fails) and requires the toggle; it derives nothing else.
	UmbrellaComponents []string `yaml:"umbrellaComponents"`
}

// ChartName is the umbrella chart's name, the prefix of every copied helper.
func (c *Config) ChartName() string {
	_, name := mappingGet(&c.Chart, "name")
	if name == nil {
		return ""
	}
	return name.Value
}

var (
	exactVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	majorRangeRe   = regexp.MustCompile(`^(\d+)\.x$`)
	// An umbrella component is addressed as .Values.components.<name> by the
	// hand-authored templates, so the name must be a plain Go template
	// identifier (a dependency name may carry hyphens; it is reached through
	// index).
	identifierNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*$`)
	// A dependency condition override points into an umbrella component's
	// contract block and ends in an `enabled` leaf.
	conditionRe = regexp.MustCompile(`^components\.([A-Za-z][A-Za-z0-9]*)((?:\.[A-Za-z][A-Za-z0-9]*)*)\.enabled$`)
)

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config Config
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &config, nil
}

func (c *Config) Validate() error {
	if c.Fleet.Repository == "" || c.Fleet.Chart == "" || c.Fleet.ConnectivityChart == "" {
		return fmt.Errorf("fleet.repository, fleet.chart and fleet.connectivityChart are required")
	}
	if !exactVersionRe.MatchString(c.Fleet.Version) {
		return fmt.Errorf("fleet.version %q must be an exact version (the pin is the reproducibility anchor)", c.Fleet.Version)
	}
	if c.Chart.Kind != yaml.MappingNode {
		return fmt.Errorf("chart must be a mapping")
	}
	if slices.Contains(mappingKeys(&c.Chart), "dependencies") {
		return fmt.Errorf("chart.dependencies is generated; remove it from the config")
	}
	if len(c.Dependencies) == 0 {
		return fmt.Errorf("dependencies must not be empty")
	}
	seen := map[string]bool{}
	for _, dependency := range c.Dependencies {
		if dependency.Name == "" {
			return fmt.Errorf("dependency without name")
		}
		if seen[dependency.Name] {
			return fmt.Errorf("dependency %q listed twice", dependency.Name)
		}
		seen[dependency.Name] = true
		if !majorRangeRe.MatchString(dependency.Range) {
			return fmt.Errorf("dependency %q: range %q must have the form <major>.x", dependency.Name, dependency.Range)
		}
		if !exactVersionRe.MatchString(dependency.Version) {
			return fmt.Errorf("dependency %q: version %q must be an exact version (the pin Renovate bumps)", dependency.Name, dependency.Version)
		}
		if dependency.Condition != "" {
			if !dependency.IsExtra() {
				return fmt.Errorf("dependency %q: condition is only valid on an extra dependency (a fleet dependency is switched by the fleet's components.%s.enabled)", dependency.Name, dependency.Name)
			}
			if dependency.Enabled != nil {
				return fmt.Errorf("dependency %q: enabled and condition are exclusive; the umbrella component's contract block carries the toggle %s", dependency.Name, dependency.Condition)
			}
			component := dependency.ConditionComponent()
			if component == "" {
				return fmt.Errorf("dependency %q: condition %q must have the form components.<umbrellaComponent>[.<key>...].enabled", dependency.Name, dependency.Condition)
			}
			if !slices.Contains(c.UmbrellaComponents, component) {
				return fmt.Errorf("dependency %q: condition %q points at components.%s, which is not declared in umbrellaComponents", dependency.Name, dependency.Condition, component)
			}
		} else if dependency.IsExtra() && dependency.Enabled == nil {
			return fmt.Errorf("extra dependency %q must set enabled", dependency.Name)
		}
		if !dependency.IsExtra() && dependency.Enabled != nil {
			return fmt.Errorf("fleet dependency %q must not set enabled (the fleet toggle default is used)", dependency.Name)
		}
	}
	if len(c.Keys) == 0 {
		return fmt.Errorf("keys must not be empty")
	}
	dependenciesKeys := 0
	for key, rule := range c.Keys {
		if rule.Chart != "" {
			if rule.Action != ActionLift {
				return fmt.Errorf("keys.%s: chart is only valid with action lift", key)
			}
			if _, ok := c.Dependency(rule.Chart); !ok {
				return fmt.Errorf("keys.%s: chart %q is not a dependency", key, rule.Chart)
			}
		}
		if rule.KeepEnabled && rule.Action != ActionComponent {
			return fmt.Errorf("keys.%s: keepEnabled is only valid with action component", key)
		}
		switch rule.Action {
		case ActionKeep, ActionDrop, ActionWiring, ActionUmbrella:
			if len(rule.Lift) > 0 {
				return fmt.Errorf("keys.%s: lift is only valid with action component or lift", key)
			}
		case ActionComponent:
		case ActionLift:
			if rule.Chart == "" {
				return fmt.Errorf("keys.%s: action lift must name the dependency it wires (chart)", key)
			}
			if len(rule.Lift) == 0 {
				return fmt.Errorf("keys.%s: action lift must list the keys it lifts", key)
			}
		case ActionDependencies:
			dependenciesKeys++
		default:
			return fmt.Errorf("keys.%s: unknown action %q", key, rule.Action)
		}
	}
	if dependenciesKeys != 1 {
		return fmt.Errorf("exactly one key must have action %q", ActionDependencies)
	}
	// The moveTo guard reads other keys' actions, so it runs after every
	// action validated.
	for _, key := range c.SortedKeys() {
		rule := c.Keys[key]
		if rule.MoveTo == "" {
			continue
		}
		if rule.Action != ActionWiring {
			return fmt.Errorf("keys.%s: moveTo is only valid with action wiring", key)
		}
		if !strings.Contains(rule.MoveTo, ".") {
			return fmt.Errorf("keys.%s: moveTo %q must be a dotted path below a top-level key", key, rule.MoveTo)
		}
		// A dependency block is placed after the relocation and would replace
		// it wholesale; any other non-umbrella-owned first segment would
		// resurrect a key the transform dropped or never carries.
		first := strings.SplitN(rule.MoveTo, ".", 2)[0]
		if _, isDependency := c.Dependency(first); isDependency {
			return fmt.Errorf("keys.%s: moveTo %q starts at dependency %q, whose block would replace the relocated values", key, rule.MoveTo, first)
		}
		if !c.UmbrellaOwned(first) {
			return fmt.Errorf("keys.%s: moveTo %q must start at an umbrella-owned top-level key (components, or a key with action keep, wiring or umbrella)", key, rule.MoveTo)
		}
	}
	for i, patch := range c.Templates.Patch {
		if patch.File == "" || patch.Find == "" {
			return fmt.Errorf("templates.patch[%d]: file and find are required", i)
		}
		if patch.Find == patch.Replace {
			return fmt.Errorf("templates.patch[%d] (%s): find and replace are identical", i, patch.File)
		}
	}
	for path, annotation := range c.Annotations {
		if annotation == "" {
			return fmt.Errorf("annotations.%s: the annotation must not be empty", path)
		}
	}
	seenComponents := map[string]bool{}
	for _, name := range c.UmbrellaComponents {
		if !identifierNameRe.MatchString(name) {
			return fmt.Errorf("umbrellaComponents: %q must be a plain identifier (letters and digits, the templates read .Values.components.<name>)", name)
		}
		if seenComponents[name] {
			return fmt.Errorf("umbrellaComponents: %q listed twice", name)
		}
		seenComponents[name] = true
		if _, isDependency := c.Dependency(name); isDependency {
			return fmt.Errorf("umbrellaComponents: %q is a dependency; a dependency's components entry is generated, not overlay-defined", name)
		}
	}
	return nil
}

// IsUmbrellaComponent reports whether name is a component the umbrella
// renders itself (curate.yaml umbrellaComponents), with no dependency.
func (c *Config) IsUmbrellaComponent(name string) bool {
	return slices.Contains(c.UmbrellaComponents, name)
}

// UmbrellaOwned reports whether key is a top-level key the umbrella owns: the
// components map, or a key whose rule keeps, copies (wiring) or declares
// (umbrella) it. The overlay guards and the moveTo validation share this
// predicate so they cannot drift apart.
func (c *Config) UmbrellaOwned(key string) bool {
	if key == "components" {
		return true
	}
	rule := c.Keys[key]
	return rule.Action == ActionKeep || rule.Action == ActionWiring || rule.Action == ActionUmbrella
}

// Dependency returns the configured dependency named name.
func (c *Config) Dependency(name string) (Dependency, bool) {
	for _, dependency := range c.Dependencies {
		if dependency.Name == name {
			return dependency, true
		}
	}
	return Dependency{}, false
}

// RangeMajor returns the major version a <major>.x range admits.
func RangeMajor(constraint string) string {
	return majorRangeRe.FindStringSubmatch(constraint)[1]
}

// SortedKeys returns the configured key names in a stable order.
func (c *Config) SortedKeys() []string {
	keys := make([]string, 0, len(c.Keys))
	for key := range c.Keys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
