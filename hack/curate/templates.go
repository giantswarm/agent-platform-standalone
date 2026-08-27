package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// PathMove records where a fleet values path went in the umbrella layout. The
// transform derives one per lifted key, per renamed component block and per
// dropped block, so the template rewrite follows the values rules instead of a
// hand-kept list.
type PathMove struct {
	From    []string
	To      []string
	Dropped bool
}

// String renders the fleet path, for error messages.
func (m PathMove) String() string { return ".Values." + strings.Join(m.From, ".") }

var (
	// valuesReferenceRe matches a statically resolvable values path. The
	// dynamic forms (index $.Values (first .)) carry no path to rewrite and
	// are left alone.
	valuesReferenceRe = regexp.MustCompile(`(\$root\.Values|\$\.Values|\.ctx\.Values|\.Values)((?:\.[A-Za-z_][A-Za-z0-9_]*)*)`)
	// helperCallRe matches a named template call or definition.
	helperCallRe = regexp.MustCompile(`\b(include|define|template)\s+"([^"]+)"`)
	// identifierRe matches a path segment a Go template can dereference with a dot.
	identifierRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// TemplateSet is the generated template tree, keyed by path relative to the
// chart's templates directory.
type TemplateSet map[string][]byte

// RenderTemplates copies the connectivity chart's templates into the umbrella
// layout: helper names take the chart-name prefix, and every values path the
// transform moved is rewritten. A path this chart does not carry fails the run.
func RenderTemplates(config *Config, sourceDir string, moves []PathMove, valueKeys []string) (TemplateSet, error) {
	sources, err := readTemplateFiles(sourceDir)
	if err != nil {
		return nil, err
	}
	// An extra file is the umbrella's own: never copied over, never deleted.
	for _, name := range config.Templates.Extra {
		delete(sources, name)
	}
	explicit, err := explicitMoves(config)
	if err != nil {
		return nil, err
	}
	// An explicit rule wins over a derived one, and a longer path wins over a
	// shorter one, so a lifted key is resolved before its component block.
	ordered := append(explicit, moves...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].From) > len(ordered[j].From) })

	renames := helperRenames(config.ChartName(), sources)
	out := make(TemplateSet, len(sources))
	for name, content := range sources {
		text := renameHelpers(string(content), renames)
		text, err := rewriteValuePaths(text, name, ordered, valueKeys)
		if err != nil {
			return nil, err
		}
		text = rewriteProsePaths(text, ordered, valueKeys)
		out[name] = []byte(text)
	}
	return out, nil
}

// readTemplateFiles reads the source chart's templates directory.
func readTemplateFiles(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = content
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read templates from %s: %w", dir, err)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no templates found in %s", dir)
	}
	return files, nil
}

// explicitMoves converts the curate.yaml template rewrites into moves.
func explicitMoves(config *Config) ([]PathMove, error) {
	var moves []PathMove
	for _, rewrite := range config.Templates.Rewrite {
		from, err := valuesPathSegments(rewrite.From)
		if err != nil {
			return nil, fmt.Errorf("templates.rewrite: from: %w", err)
		}
		to, err := valuesPathSegments(rewrite.To)
		if err != nil {
			return nil, fmt.Errorf("templates.rewrite: to: %w", err)
		}
		moves = append(moves, PathMove{From: from, To: to})
	}
	return moves, nil
}

func valuesPathSegments(path string) ([]string, error) {
	trimmed, found := strings.CutPrefix(path, ".Values.")
	if !found || trimmed == "" {
		return nil, fmt.Errorf("%q must start with .Values. and name a key", path)
	}
	return strings.Split(trimmed, "."), nil
}

// helperRenames maps every helper the source chart defines to its umbrella
// name: the chart name, then the helper's own name without the fleet prefix.
func helperRenames(chartName string, sources map[string][]byte) map[string]string {
	renames := map[string]string{}
	for _, content := range sources {
		for _, match := range helperCallRe.FindAllStringSubmatch(string(content), -1) {
			if match[1] != "define" {
				continue
			}
			name := match[2]
			renames[name] = chartName + "." + strings.TrimPrefix(name, "agent-platform.")
		}
	}
	return renames
}

func renameHelpers(text string, renames map[string]string) string {
	return helperCallRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := helperCallRe.FindStringSubmatch(match)
		renamed, ok := renames[parts[2]]
		if !ok {
			return match
		}
		return strings.Replace(match, `"`+parts[2]+`"`, `"`+renamed+`"`, 1)
	})
}

// rewriteValuePaths resolves every values reference through the move list. A
// reference to a dropped block, or to a key the umbrella values do not carry,
// fails the run: the copy must not read a value that no longer exists.
func rewriteValuePaths(text, name string, moves []PathMove, valueKeys []string) (string, error) {
	var failure error
	result := valuesReferenceRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := valuesReferenceRe.FindStringSubmatch(match)
		prefix, suffix := parts[1], parts[2]
		if suffix == "" {
			return match
		}
		segments := strings.Split(strings.TrimPrefix(suffix, "."), ".")
		for _, move := range moves {
			if !isPrefix(move.From, segments) {
				continue
			}
			if move.Dropped {
				if failure == nil {
					failure = fmt.Errorf("templates/%s reads %s, which this chart does not carry; add a lift rule or a templates.rewrite entry in curate.yaml", name, move)
				}
				return match
			}
			segments = append(slices.Clone(move.To), segments[len(move.From):]...)
			break
		}
		if !slices.Contains(valueKeys, segments[0]) && failure == nil {
			failure = fmt.Errorf("templates/%s reads .Values.%s, and the generated values have no %q key", name, strings.Join(segments, "."), segments[0])
		}
		return renderValuesPath(prefix, segments)
	})
	if failure != nil {
		return "", failure
	}
	return result, nil
}

// rewriteProsePaths moves a path named in a comment or a message string, for a
// block whose own key is gone from the umbrella values. Such a mention can only
// be wrong here, and these strings are what a failed render tells the operator.
// A path whose top-level key the umbrella still carries is left as the source
// chart wrote it: the same text can name a real key there.
func rewriteProsePaths(text string, moves []PathMove, valueKeys []string) string {
	for _, move := range moves {
		if move.Dropped || len(move.From) != 1 || slices.Contains(valueKeys, move.From[0]) {
			continue
		}
		from := move.From[0]
		to := strings.Join(move.To, ".")
		for _, suffix := range []string{".", " "} {
			text = strings.ReplaceAll(text, from+suffix, to+suffix)
		}
	}
	return text
}

func isPrefix(prefix, segments []string) bool {
	return len(prefix) <= len(segments) && slices.Equal(prefix, segments[:len(prefix)])
}

// renderValuesPath writes a values path back as Go template syntax. A segment
// a dot cannot reach (a hyphenated chart name) takes the index form.
func renderValuesPath(prefix string, segments []string) string {
	path := prefix
	for _, segment := range segments {
		if identifierRe.MatchString(segment) {
			path += "." + segment
			continue
		}
		path = fmt.Sprintf(`(index %s %q)`, path, segment)
	}
	return path
}
