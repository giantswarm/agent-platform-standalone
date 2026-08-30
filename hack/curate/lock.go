package main

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// locksEquivalent reports whether two Chart.lock files pin the same
// dependencies. The `generated` timestamp is ignored so an unchanged BOM keeps
// the committed file byte for byte.
func locksEquivalent(a, b []byte) (bool, error) {
	normalize := func(raw []byte) (string, error) {
		var lock map[string]any
		if err := yaml.Unmarshal(raw, &lock); err != nil {
			return "", fmt.Errorf("parse Chart.lock: %w", err)
		}
		delete(lock, "generated")
		out, err := yaml.Marshal(lock)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	left, err := normalize(a)
	if err != nil {
		return false, err
	}
	right, err := normalize(b)
	if err != nil {
		return false, err
	}
	return left == right, nil
}
