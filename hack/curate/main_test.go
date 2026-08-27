package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeHelm serves the fixtures from memory and records the lock it "writes".
type fakeHelm struct {
	values    map[string]string
	templates map[string]map[string]string
	versions map[string]string
	lock     string
	resolves int
	updates  int
	builds   int
}

func (h *fakeHelm) Pull(_, chart, _, destDir string) error {
	values, ok := h.values[chart]
	if !ok {
		return fmt.Errorf("no fixture for chart %q", chart)
	}
	dir := filepath.Join(destDir, chart)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "values.yaml"), []byte(values), 0o644); err != nil {
		return err
	}
	for name, content := range h.templates[chart] {
		path := filepath.Join(dir, "templates", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (h *fakeHelm) ResolveVersion(_, chart, _ string) (string, error) {
	h.resolves++
	version, ok := h.versions[chart]
	if !ok {
		return "", fmt.Errorf("no version fixture for %q", chart)
	}
	return version, nil
}

func (h *fakeHelm) DependencyUpdate(chartDir string) error {
	h.updates++
	return os.WriteFile(filepath.Join(chartDir, "Chart.lock"), fmt.Appendf(nil, h.lock, h.updates), 0o644)
}

func (h *fakeHelm) DependencyBuild(string) error {
	h.builds++
	return nil
}

func newFakeHelm() *fakeHelm {
	return &fakeHelm{
		values: map[string]string{
			"agent-platform":              fixtureFleet,
			"agent-platform-connectivity": fixtureConnectivity,
		},
		templates: map[string]map[string]string{
			// Copied: a test that drops a template must not mutate the fixture.
			"agent-platform-connectivity": maps.Clone(fixtureTemplates),
		},
		versions: map[string]string{
			"muster": "5.5.3", "kagent": "0.1.37", "agent-platform-mcps": "0.6.7", "agent-sandbox": "0.2.23", "backstage": "0.195.2",
		},
		lock: "dependencies:\n- name: muster\n  repository: oci://example/charts\n  version: 5.5.3\ndigest: sha256:abc\ngenerated: \"2026-08-27T00:00:0%dZ\"\n",
	}
}

func setupRepo(t *testing.T) (configPath, overlayPath, chartDir string) {
	t.Helper()
	root := t.TempDir()
	configPath = filepath.Join(root, "curate.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte(fixtureConfig), 0o644))
	overlayPath = filepath.Join(root, "overlay", "vanilla.yaml")
	chartDir = filepath.Join(root, "helm", "chart")
	require.NoError(t, os.MkdirAll(chartDir, 0o755))
	return configPath, overlayPath, chartDir
}

func TestRunIsIdempotentAndCheckPasses(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()

	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	first := snapshot(t, chartDir)
	require.Contains(t, first["Chart.lock"], "generated: \"2026-08-27T00:00:01Z\"")

	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	second := snapshot(t, chartDir)
	require.Equal(t, first, second, "a second run changes nothing, including the Chart.lock timestamp")
	require.Equal(t, 2, helm.updates)

	helm.versions["muster"] = "5.9.9"
	resolvesBefore := helm.resolves
	require.NoError(t, run(configPath, overlayPath, chartDir, true, helm))
	require.Equal(t, 1, helm.builds, "check mode runs helm dependency build, never update")
	require.Equal(t, 2, helm.updates)
	require.Equal(t, resolvesBefore, helm.resolves, "check mode validates the committed pins; a newer upstream release does not fail it")
}

func TestRunCheckRejectsPinOutsideRange(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))

	chartPath := filepath.Join(chartDir, "Chart.yaml")
	chart, err := os.ReadFile(chartPath)
	require.NoError(t, err)
	edited := replaceOnce(string(chart), `version: "5.5.3"`, `version: "6.0.0"`)
	require.NotEqual(t, string(chart), edited)
	require.NoError(t, os.WriteFile(chartPath, []byte(edited), 0o644))

	require.ErrorContains(t, run(configPath, overlayPath, chartDir, true, helm), `dependency "muster" resolved to 6.0.0, outside range 5.x`)
}

func TestRunCheckDetectsStaleValues(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))

	valuesPath := filepath.Join(chartDir, "values.yaml")
	stale, err := os.ReadFile(valuesPath)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(valuesPath, append(stale, []byte("handEdited: true\n")...), 0o644))

	err = run(configPath, overlayPath, chartDir, true, helm)
	require.ErrorContains(t, err, "values.yaml differs from the generator output")
	require.FileExists(t, valuesPath+".curate")
	require.Equal(t, 0, helm.builds, "a stale file fails before the lock check")
}

func TestRunCheckDetectsMissingLock(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	require.NoError(t, os.Remove(filepath.Join(chartDir, "Chart.lock")))
	require.ErrorContains(t, run(configPath, overlayPath, chartDir, true, helm), "Chart.lock is missing")
}

func TestRunWritesUpdatedLockWhenPinsChange(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))

	helm.lock = "dependencies:\n- name: muster\n  repository: oci://example/charts\n  version: 5.6.0\ndigest: sha256:def\ngenerated: \"2026-09-01T00:00:0%dZ\"\n"
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	lock, err := os.ReadFile(filepath.Join(chartDir, "Chart.lock"))
	require.NoError(t, err)
	require.Contains(t, string(lock), "version: 5.6.0")
}

func snapshot(t *testing.T, chartDir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"Chart.yaml", "values.yaml", "Chart.lock", "templates/_helpers.tpl", "templates/netpol.yaml"} {
		content, err := os.ReadFile(filepath.Join(chartDir, name))
		require.NoError(t, err)
		files[name] = string(content)
	}
	return files
}

func TestRunCheckDetectsHandEditedTemplate(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))

	path := filepath.Join(chartDir, "templates", "netpol.yaml")
	edited, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(edited, []byte("handEdited: true\n")...), 0o644))

	require.ErrorContains(t, run(configPath, overlayPath, chartDir, true, helm), "netpol.yaml differs from the generator output")
}

// A template the source chart stops shipping is deleted, so a fleet removal
// needs no hand edit here; check mode reports it instead of deleting.
func TestRunRemovesTemplateTheSourceChartDropped(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	require.FileExists(t, filepath.Join(chartDir, "templates", "netpol.yaml"))

	delete(helm.templates["agent-platform-connectivity"], "netpol.yaml")
	require.ErrorContains(t, run(configPath, overlayPath, chartDir, true, helm), "templates [netpol.yaml] are neither generated nor listed")

	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	require.NoFileExists(t, filepath.Join(chartDir, "templates", "netpol.yaml"))
}

// The umbrella's own file survives every run.
func TestRunKeepsExtraTemplate(t *testing.T) {
	configPath, overlayPath, chartDir := setupRepo(t)
	helm := newFakeHelm()
	notes := filepath.Join(chartDir, "templates", "NOTES.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(notes), 0o755))
	require.NoError(t, os.WriteFile(notes, []byte("the umbrella's own notes\n"), 0o644))

	require.NoError(t, run(configPath, overlayPath, chartDir, false, helm))
	content, err := os.ReadFile(notes)
	require.NoError(t, err)
	require.Equal(t, "the umbrella's own notes\n", string(content))
	require.NoError(t, run(configPath, overlayPath, chartDir, true, helm))
}
