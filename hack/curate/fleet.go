package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FleetComponent is one entry of the fleet meta-package's `components` map.
type FleetComponent struct {
	Key        string
	Chart      string
	Repository string
	ValuesFrom string
	ValuesKey  string
	// Enabled is the entry's own toggle, the single on/off switch. Absent means
	// on, the reading of the fleet's own componentEnabled helper.
	Enabled *bool
	// OmitKeys are the block keys the fleet withholds from the forwarded values
	// because the umbrella owns them, not the component chart. This generator
	// has no equivalent: every one of them must be covered by the lift rule of
	// the block's curate.yaml entry (transform.go checks that).
	OmitKeys []string
}

// IsEnabled reports the component's default toggle.
func (c FleetComponent) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// parseFleetComponents reads the fleet `components` map.
func parseFleetComponents(components *yaml.Node) ([]FleetComponent, error) {
	if components.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("fleet components is not a mapping")
	}
	var result []FleetComponent
	for i := 0; i+1 < len(components.Content); i += 2 {
		key, entry := components.Content[i].Value, components.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("fleet component %q is not a mapping", key)
		}
		component := FleetComponent{Key: key}
		fields := map[string]*string{
			"chart":      &component.Chart,
			"repository": &component.Repository,
			"valuesFrom": &component.ValuesFrom,
			"valuesKey":  &component.ValuesKey,
		}
		for field, target := range fields {
			_, value := mappingGet(entry, field)
			text, err := scalarString(value, fmt.Sprintf("fleet component %q field %s", key, field))
			if err != nil {
				return nil, err
			}
			*target = text
		}
		if _, enabled := mappingGet(entry, "enabled"); enabled != nil {
			value, err := scalarBool(enabled, fmt.Sprintf("fleet component %q field enabled", key))
			if err != nil {
				return nil, err
			}
			component.Enabled = &value
		}
		if _, omitKeys := mappingGet(entry, "omitKeys"); omitKeys != nil {
			if omitKeys.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("fleet component %q field omitKeys is not a sequence", key)
			}
			for _, item := range omitKeys.Content {
				text, err := scalarString(item, fmt.Sprintf("fleet component %q omitKeys item", key))
				if err != nil {
					return nil, err
				}
				component.OmitKeys = append(component.OmitKeys, text)
			}
		}
		if component.Chart == "" || component.Repository == "" {
			return nil, fmt.Errorf("fleet component %q lacks chart or repository", key)
		}
		result = append(result, component)
	}
	return result, nil
}

// fleetComponentByChart returns the fleet component whose chart is name.
func fleetComponentByChart(components []FleetComponent, name string) (FleetComponent, bool) {
	for _, component := range components {
		if component.Chart == name {
			return component, true
		}
	}
	return FleetComponent{}, false
}

// fleetComponentByValuesFrom returns the fleet component whose values block is key.
func fleetComponentByValuesFrom(components []FleetComponent, key string) (FleetComponent, bool) {
	for _, component := range components {
		if component.ValuesFrom == key {
			return component, true
		}
	}
	return FleetComponent{}, false
}
