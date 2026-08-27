package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// FleetComponent is one entry of the fleet meta-package's `components` map.
type FleetComponent struct {
	Key         string
	Chart       string
	Repository  string
	ValuesFrom  string
	ValuesKey   string
	EnabledFrom string
}

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
			"chart":       &component.Chart,
			"repository":  &component.Repository,
			"valuesFrom":  &component.ValuesFrom,
			"valuesKey":   &component.ValuesKey,
			"enabledFrom": &component.EnabledFrom,
		}
		for field, target := range fields {
			_, value := mappingGet(entry, field)
			text, err := scalarString(value, fmt.Sprintf("fleet component %q field %s", key, field))
			if err != nil {
				return nil, err
			}
			*target = text
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

// fleetComponentByToggleKey returns the fleet component whose enabledFrom path
// starts with key.
func fleetComponentByToggleKey(components []FleetComponent, key string) (FleetComponent, bool) {
	for _, component := range components {
		first, _, _ := strings.Cut(component.EnabledFrom, ".")
		if component.EnabledFrom != "" && first == key {
			return component, true
		}
	}
	return FleetComponent{}, false
}
