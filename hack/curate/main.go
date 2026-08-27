// Command curate generates the agent-platform-standalone umbrella chart from
// the fleet meta-package: Chart.yaml (exact pins), values.yaml (transformed
// component blocks) and Chart.lock.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func main() {
	configPath := flag.String("config", "curate.yaml", "generator input")
	overlayPath := flag.String("overlay", "overlay/vanilla.yaml", "vanilla overlay applied last")
	chartDir := flag.String("chart-dir", "helm/agent-platform-standalone", "chart directory to generate into")
	check := flag.Bool("check", false, "verify the committed files match the generator output and Chart.lock is in sync; write nothing")
	helmBin := flag.String("helm", "helm", "helm binary")
	flag.Parse()

	if err := run(*configPath, *overlayPath, *chartDir, *check, execHelm{bin: *helmBin}); err != nil {
		fmt.Fprintln(os.Stderr, "curate:", err)
		os.Exit(1)
	}
}

func run(configPath, overlayPath, chartDir string, check bool, helm Helm) error {
	config, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	overlay, err := loadOverlay(overlayPath)
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "curate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	fleet, err := pullValues(helm, config.Fleet.Repository, config.Fleet.Chart, config.Fleet.Version, workDir)
	if err != nil {
		return err
	}
	connectivity, err := pullValues(helm, config.Fleet.Repository, config.Fleet.ConnectivityChart, config.Fleet.Version, workDir)
	if err != nil {
		return err
	}

	values, components, err := Transform(Inputs{Config: config, Fleet: fleet, Connectivity: connectivity, Overlay: overlay})
	if err != nil {
		return err
	}

	// Check mode validates the committed pins and never asks the registry for
	// newer versions: a component release must not fail an unrelated PR.
	var pinned map[string]string
	if check {
		if pinned, err = pinnedVersions(filepath.Join(chartDir, "Chart.yaml")); err != nil {
			return err
		}
	}
	resolved := map[string]string{}
	for _, dependency := range config.Dependencies {
		var version string
		if check {
			var ok bool
			if version, ok = pinned[dependency.Name]; !ok {
				return fmt.Errorf("Chart.yaml has no pin for dependency %q; run hack/curate.sh", dependency.Name)
			}
		} else {
			repository := dependency.Repository
			if !dependency.IsExtra() {
				component, _ := fleetComponentByChart(components, dependency.Name)
				repository = component.Repository
			}
			if version, err = helm.ResolveVersion(repository, dependency.Name, dependency.Range); err != nil {
				return fmt.Errorf("resolve %s %s: %w", dependency.Name, dependency.Range, err)
			}
		}
		resolved[dependency.Name] = version
		fmt.Fprintf(os.Stderr, "curate: %-22s %-6s -> %s\n", dependency.Name, dependency.Range, version)
	}

	chart, err := BuildChartYAML(config, components, resolved)
	if err != nil {
		return err
	}
	chartBytes, err := encodeYAML(chart)
	if err != nil {
		return err
	}
	valuesBytes, err := encodeYAML(values)
	if err != nil {
		return err
	}

	outputs := map[string][]byte{
		filepath.Join(chartDir, "Chart.yaml"):  chartBytes,
		filepath.Join(chartDir, "values.yaml"): valuesBytes,
	}
	if check {
		return verify(outputs, chartDir, helm)
	}
	for path, content := range outputs {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "curate: wrote", path)
	}
	return refreshLock(chartDir, helm)
}

// pinnedVersions reads the dependency pins of the committed Chart.yaml.
func pinnedVersions(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var chart struct {
		Dependencies []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(raw, &chart); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	pinned := make(map[string]string, len(chart.Dependencies))
	for _, dependency := range chart.Dependencies {
		pinned[dependency.Name] = dependency.Version
	}
	return pinned, nil
}

func loadOverlay(path string) (*yaml.Node, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return parseYAML(nil, path)
	}
	if err != nil {
		return nil, err
	}
	return parseYAML(raw, path)
}

func pullValues(helm Helm, repository, chart, version, workDir string) (*yaml.Node, error) {
	destDir := filepath.Join(workDir, chart)
	if err := helm.Pull(repository, chart, version, destDir); err != nil {
		return nil, fmt.Errorf("pull %s %s: %w", chart, version, err)
	}
	return loadYAMLFile(filepath.Join(destDir, chart, "values.yaml"))
}

// verify is the CI mode: the committed files must equal the generator output
// and `helm dependency build` must accept the committed Chart.lock.
func verify(outputs map[string][]byte, chartDir string, helm Helm) error {
	var problems []string
	for path, want := range outputs {
		have, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(have, want) {
			stale := path + ".curate"
			if err := os.WriteFile(stale, want, 0o644); err != nil {
				return err
			}
			problems = append(problems, fmt.Sprintf("%s differs from the generator output (see %s); run hack/curate.sh", path, stale))
		}
	}
	if _, err := os.Stat(filepath.Join(chartDir, "Chart.lock")); err != nil {
		problems = append(problems, "Chart.lock is missing; run hack/curate.sh")
	}
	if len(problems) > 0 {
		return errors.New(joinLines(problems))
	}
	if err := helm.DependencyBuild(chartDir); err != nil {
		return fmt.Errorf("Chart.lock is out of sync with Chart.yaml; run hack/curate.sh: %w", err)
	}
	fmt.Fprintln(os.Stderr, "curate: generated files and Chart.lock are up to date")
	return nil
}

// refreshLock runs `helm dependency update` and keeps the committed Chart.lock
// when the resolved pins did not change, so a second run yields no diff.
func refreshLock(chartDir string, helm Helm) error {
	lockPath := filepath.Join(chartDir, "Chart.lock")
	previous, err := os.ReadFile(lockPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := helm.DependencyUpdate(chartDir); err != nil {
		return err
	}
	if previous == nil {
		fmt.Fprintln(os.Stderr, "curate: wrote", lockPath)
		return nil
	}
	current, err := os.ReadFile(lockPath)
	if err != nil {
		return err
	}
	same, err := locksEquivalent(previous, current)
	if err != nil {
		return err
	}
	if same {
		fmt.Fprintln(os.Stderr, "curate: Chart.lock unchanged")
		return os.WriteFile(lockPath, previous, 0o644)
	}
	fmt.Fprintln(os.Stderr, "curate: updated", lockPath)
	return nil
}

func joinLines(lines []string) string {
	var out bytes.Buffer
	for i, line := range lines {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString(line)
	}
	return out.String()
}
