// Command curate generates the agent-platform-standalone umbrella chart from
// the fleet meta-package: Chart.yaml (exact pins), values.yaml (transformed
// component blocks), Chart.lock, the chart's templates and the generated
// examples.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type options struct {
	configPath           string
	contractPath         string
	overlayPath          string
	giantswarmInputsPath string
	chartDir             string
	examplesDir          string
	check                bool
}

func main() {
	var opts options
	flag.StringVar(&opts.configPath, "config", "curate.yaml", "generator input")
	flag.StringVar(&opts.contractPath, "contract", "overlay/contract.yaml", "umbrella contract overlay, applied first and never reverted")
	flag.StringVar(&opts.overlayPath, "overlay", "overlay/vanilla.yaml", "vanilla overlay applied last; examples/giantswarm.yaml reverts it")
	flag.StringVar(&opts.giantswarmInputsPath, "giantswarm-inputs", "overlay/giantswarm.yaml", "per-installation inputs merged into the generated examples/giantswarm.yaml")
	flag.StringVar(&opts.chartDir, "chart-dir", "helm/agent-platform-standalone", "chart directory to generate into")
	flag.StringVar(&opts.examplesDir, "examples-dir", "examples", "directory of the generated examples")
	flag.BoolVar(&opts.check, "check", false, "verify the committed files match the generator output and Chart.lock is in sync; write nothing")
	helmBin := flag.String("helm", "helm", "helm binary")
	flag.Parse()

	if err := run(opts, execHelm{bin: *helmBin}); err != nil {
		fmt.Fprintln(os.Stderr, "curate:", err)
		os.Exit(1)
	}
}

func run(opts options, helm Helm) error {
	config, err := LoadConfig(opts.configPath)
	if err != nil {
		return err
	}
	contract, err := loadOverlay(opts.contractPath)
	if err != nil {
		return err
	}
	overlay, err := loadOverlay(opts.overlayPath)
	if err != nil {
		return err
	}
	giantswarmInputs, err := loadOverlay(opts.giantswarmInputsPath)
	if err != nil {
		return err
	}

	workDir, err := os.MkdirTemp("", "curate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	fleetDir, err := pullChart(helm, config.Fleet.Repository, config.Fleet.Chart, config.Fleet.Version, workDir)
	if err != nil {
		return err
	}
	connectivityDir, err := pullChart(helm, config.Fleet.Repository, config.Fleet.ConnectivityChart, config.Fleet.Version, workDir)
	if err != nil {
		return err
	}
	fleet, err := loadYAMLFile(filepath.Join(fleetDir, "values.yaml"))
	if err != nil {
		return err
	}
	connectivity, err := loadYAMLFile(filepath.Join(connectivityDir, "values.yaml"))
	if err != nil {
		return err
	}

	result, err := Transform(Inputs{Config: config, Fleet: fleet, Connectivity: connectivity, Contract: contract, Overlay: overlay, GiantswarmInputs: giantswarmInputs})
	if err != nil {
		return err
	}
	values, components := result.Document, result.Components
	rewriteValuesComments(values, result.Moves, result.ValueKeys)

	example, err := GiantswarmExample(config, result.BeforeOverlay, overlay, giantswarmInputs)
	if err != nil {
		return err
	}
	rewriteValuesComments(example, result.Moves, result.ValueKeys)

	templates, err := RenderTemplates(config, filepath.Join(connectivityDir, "templates"), result.Moves, result.ValueKeys)
	if err != nil {
		return err
	}

	// The exact pins live in curate.yaml, bumped by Renovate; the generator
	// uses them verbatim (BuildChartYAML rejects a pin outside its range) and
	// never asks the registry for newer versions, so a component release
	// cannot change a curation run or fail an unrelated PR — in generate and
	// check mode alike.
	resolved := map[string]string{}
	for _, dependency := range config.Dependencies {
		resolved[dependency.Name] = dependency.Version
		fmt.Fprintf(os.Stderr, "curate: %-22s %-6s -> %s\n", dependency.Name, dependency.Range, dependency.Version)
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
	exampleBytes, err := encodeYAML(example)
	if err != nil {
		return err
	}

	outputs := map[string][]byte{
		filepath.Join(opts.chartDir, "Chart.yaml"):         chartBytes,
		filepath.Join(opts.chartDir, "values.yaml"):        valuesBytes,
		filepath.Join(opts.examplesDir, "giantswarm.yaml"): exampleBytes,
	}
	for name, content := range templates {
		outputs[filepath.Join(opts.chartDir, "templates", name)] = content
	}
	if opts.check {
		if err := checkNoStaleTemplates(config, opts.chartDir, templates); err != nil {
			return err
		}
		return verify(outputs, opts.chartDir, helm)
	}
	for path, content := range outputs {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
		// Drop the diff sidecar a failed --check left behind for this file.
		if err := os.Remove(path + ".curate"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "curate: wrote Chart.yaml, values.yaml, examples/giantswarm.yaml and %d templates\n", len(templates))
	if err := removeStaleTemplates(config, opts.chartDir, templates); err != nil {
		return err
	}
	return refreshLock(opts.chartDir, helm)
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

// pullChart pulls a chart and returns the directory it was unpacked into.
func pullChart(helm Helm, repository, chart, version, workDir string) (string, error) {
	destDir := filepath.Join(workDir, chart)
	if err := helm.Pull(repository, chart, version, destDir); err != nil {
		return "", fmt.Errorf("pull %s %s: %w", chart, version, err)
	}
	return filepath.Join(destDir, chart), nil
}

// templateFiles lists the files under the chart's templates directory.
func templateFiles(chartDir string) ([]string, error) {
	root := filepath.Join(chartDir, "templates")
	var names []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		// A *.curate sidecar is verify()'s own diff artifact, never a template.
		if entry.IsDir() || strings.HasSuffix(path, ".curate") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	return names, err
}

// staleTemplates lists committed templates the generator no longer produces and
// curate.yaml does not declare as its own.
func staleTemplates(config *Config, chartDir string, templates TemplateSet) ([]string, error) {
	names, err := templateFiles(chartDir)
	if err != nil {
		return nil, err
	}
	var stale []string
	for _, name := range names {
		if _, generated := templates[name]; generated || slices.Contains(config.Templates.Extra, name) {
			continue
		}
		stale = append(stale, name)
	}
	sort.Strings(stale)
	return stale, nil
}

// removeStaleTemplates deletes a template the source chart stopped shipping, so
// a fleet removal reaches this chart without a hand edit.
func removeStaleTemplates(config *Config, chartDir string, templates TemplateSet) error {
	stale, err := staleTemplates(config, chartDir, templates)
	if err != nil {
		return err
	}
	for _, name := range stale {
		path := filepath.Join(chartDir, "templates", name)
		if err := os.Remove(path); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "curate: removed", path)
	}
	return nil
}

func checkNoStaleTemplates(config *Config, chartDir string, templates TemplateSet) error {
	stale, err := staleTemplates(config, chartDir, templates)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		return fmt.Errorf("templates %v are neither generated nor listed in curate.yaml templates.extra; run hack/curate.sh", stale)
	}
	return nil
}

// verify is the CI mode: the committed files must equal the generator output
// and `helm dependency build` must accept the committed Chart.lock.
func verify(outputs map[string][]byte, chartDir string, helm Helm) error {
	var problems []string
	for path, want := range outputs {
		have, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			problems = append(problems, fmt.Sprintf("%s is missing; run hack/curate.sh", path))
			continue
		}
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
