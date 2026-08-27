package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"gopkg.in/yaml.v3"
)

// Helm is the subset of the helm CLI the generator needs.
type Helm interface {
	Pull(repository, chart, version, destDir string) error
	ResolveVersion(repository, chart, constraint string) (string, error)
	DependencyUpdate(chartDir string) error
	DependencyBuild(chartDir string) error
}

type execHelm struct {
	bin string
}

func (h execHelm) run(stdout bool, args ...string) ([]byte, error) {
	command := exec.Command(h.bin, args...)
	var out, errOut bytes.Buffer
	command.Stdout = &out
	command.Stderr = &errOut
	if !stdout {
		command.Stdout = os.Stderr
	}
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w\n%s", h.bin, strings.Join(args, " "), err, errOut.String())
	}
	return out.Bytes(), nil
}

func (h execHelm) Pull(repository, chart, version, destDir string) error {
	_, err := h.run(false, "pull", strings.TrimSuffix(repository, "/")+"/"+chart, "--version", version, "--untar", "--destination", destDir)
	return err
}

func (h execHelm) ResolveVersion(repository, chart, constraint string) (string, error) {
	out, err := h.run(true, "show", "chart", strings.TrimSuffix(repository, "/")+"/"+chart, "--version", constraint)
	if err != nil {
		return "", err
	}
	var metadata struct {
		Version string `yaml:"version"`
	}
	if err := yaml.Unmarshal(out, &metadata); err != nil {
		return "", fmt.Errorf("parse `helm show chart` output for %s: %w", chart, err)
	}
	if metadata.Version == "" {
		return "", fmt.Errorf("`helm show chart` printed no version for %s", chart)
	}
	return metadata.Version, nil
}

func (h execHelm) DependencyUpdate(chartDir string) error {
	_, err := h.run(false, "dependency", "update", chartDir)
	return err
}

func (h execHelm) DependencyBuild(chartDir string) error {
	_, err := h.run(false, "dependency", "build", chartDir)
	return err
}
