package main

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"

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
)

type KeyRule struct {
	Action Action `yaml:"action"`
	// Lift lists sub-keys moved out of the block into components.<chart>.
	Lift []string `yaml:"lift"`
	// Chart names the dependency a lift block belongs to. Only action lift has
	// no fleet component to derive it from.
	Chart string `yaml:"chart"`
	// KeepEnabled forwards the block's `enabled` key to the component chart,
	// for the one component whose chart owns a value of that name. A top-level
	// `enabled` in any other component block fails the run.
	KeepEnabled bool `yaml:"keepEnabled"`
}

type Dependency struct {
	Name  string `yaml:"name"`
	Range string `yaml:"range"`
	// Repository is set only for an extra dependency (not in the fleet).
	Repository string `yaml:"repository"`
	// Enabled is the default toggle of an extra dependency.
	Enabled *bool `yaml:"enabled"`
}

func (d Dependency) IsExtra() bool { return d.Repository != "" }

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

type TemplatesConfig struct {
	Rewrite []PathRewrite `yaml:"rewrite"`
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
		if dependency.IsExtra() && dependency.Enabled == nil {
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
		case ActionKeep, ActionDrop, ActionWiring:
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
	return nil
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
