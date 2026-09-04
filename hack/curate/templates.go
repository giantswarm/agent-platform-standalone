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

	"gopkg.in/yaml.v3"
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
	ordered, err := orderedMoves(config, moves)
	if err != nil {
		return nil, err
	}

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
	if err := applyPatches(config, out); err != nil {
		return nil, err
	}
	return out, nil
}

// orderedMoves is the move list the rewrites resolve against: an explicit rule
// wins over a derived one, and a longer path wins over a shorter one, so a
// lifted key is resolved before its component block.
func orderedMoves(config *Config, moves []PathMove) ([]PathMove, error) {
	explicit, err := explicitMoves(config)
	if err != nil {
		return nil, err
	}
	ordered := append(explicit, slices.Clone(moves)...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].From) > len(ordered[j].From) })
	return ordered, nil
}

// applyPatches edits the generated templates where the upstream copy is wrong
// for the standalone layout. Find must occur exactly once, so an upstream
// change that moves the text fails the run instead of dropping the fix.
func applyPatches(config *Config, out TemplateSet) error {
	for _, patch := range config.Templates.Patch {
		content, ok := out[patch.File]
		if !ok {
			return fmt.Errorf("templates.patch: %q is not a generated template", patch.File)
		}
		text := string(content)
		if count := strings.Count(text, patch.Find); count != 1 {
			return fmt.Errorf("templates.patch: the find text occurs %d times in %s, expected exactly once; "+
				"update the patch to the current upstream template", count, patch.File)
		}
		out[patch.File] = []byte(strings.Replace(text, patch.Find, patch.Replace, 1))
	}
	return nil
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

// prosePathRe matches a dotted path named in prose (comments, fail messages,
// values comments): the strings a failed render shows the operator. The
// optional backticks are captured so a quoted literal identifier (a helper
// name, for one) can be recognized and left alone.
var prosePathRe = regexp.MustCompile("`?[A-Za-z_][A-Za-z0-9_-]*(?:\\.[A-Za-z_][A-Za-z0-9_-]*)+`?")

// proseTLDs guards single-tail tokens that are domains, not values paths
// (kagent.dev, agentgateway.dev), from the nesting moves.
var proseTLDs = map[string]bool{"ai": true, "cloud": true, "com": true, "dev": true, "io": true, "net": true, "org": true, "sh": true}

// rewriteProsePaths moves every values path named in prose through the same
// move list the code rewrite uses, so fail messages and comments keep naming
// keys this chart actually reads. Tokens are matched whole: a longer
// identifier that merely ends in a moved key, a path nested under a key that
// did not move, and a domain name are left as written.
func rewriteProsePaths(text string, moves []PathMove, valueKeys []string) string {
	return rewriteProsePathsKeeping(text, moves, valueKeys, nil)
}

// rewriteProsePathsKeeping is rewriteProsePaths with a veto: keep, when set,
// sees the path as written and its moved form and returns true to leave the
// token alone. The values comment rewrite uses it for a path that names a key
// of the block the comment sits in (see blockRelativePath).
func rewriteProsePathsKeeping(text string, moves []PathMove, valueKeys []string, keep func(token, moved []string) bool) string {
	text = prosePathRe.ReplaceAllStringFunc(text, func(token string) string {
		if strings.Contains(token, "`") {
			return token // a backtick-quoted literal identifier, e.g. a helper name
		}
		segments := strings.Split(token, ".")
		for _, move := range moves {
			if move.Dropped || !isPrefix(move.From, segments) {
				continue
			}
			rest := segments[len(move.From):]
			if slices.Equal(move.From, move.To) {
				return token
			}
			if len(rest) == 1 && proseTLDs[rest[0]] {
				return token
			}
			moved := append(slices.Clone(move.To), rest...)
			if keep != nil && keep(segments, moved) {
				return token
			}
			return strings.Join(moved, ".")
		}
		return token
	})
	// A bare mention (followed by a space or a closing dot, not quoted, not
	// part of a longer identifier) of a block whose key is gone from the
	// umbrella values can only be wrong here; one whose key survives can name
	// a real key there.
	for _, move := range moves {
		if move.Dropped || len(move.From) != 1 || slices.Contains(valueKeys, move.From[0]) {
			continue
		}
		from, to := move.From[0], strings.Join(move.To, ".")
		for _, suffix := range []string{".", " "} {
			re := regexp.MustCompile(`[.\w-]?` + regexp.QuoteMeta(from+suffix))
			text = re.ReplaceAllStringFunc(text, func(match string) string {
				if match != from+suffix {
					return match // part of a longer identifier or a dotted path
				}
				return to + suffix
			})
		}
	}
	return text
}

// rewriteValuesComments applies the prose rewrite to every comment of the
// generated values document, so a copied comment (the postgres wiring notes,
// for one) names the keys of this chart's layout. The explicit template
// rewrites are template-domain and do not apply here.
//
// A component block's comments name the block's own keys as relative paths
// (model-manager's kagent.namespace), and such a path can start with a
// top-level fleet key that moved. The rewrite therefore carries the blocks a
// comment sits in — a key's head comment sits in the block it opens — and
// blockRelativePath vetoes a move whose result the document does not carry
// while the path as written resolves in one of those blocks.
func rewriteValuesComments(document *yaml.Node, moves []PathMove, valueKeys []string) {
	ordered := slices.Clone(moves)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i].From) > len(ordered[j].From) })
	root := document
	if root.Kind == yaml.DocumentNode && len(root.Content) > 0 {
		root = root.Content[0]
	}
	rewrite := func(node *yaml.Node, scopes []*yaml.Node) {
		keep := func(token, moved []string) bool { return blockRelativePath(root, scopes, token, moved) }
		node.HeadComment = rewriteProsePathsKeeping(node.HeadComment, ordered, valueKeys, keep)
		node.LineComment = rewriteProsePathsKeeping(node.LineComment, ordered, valueKeys, keep)
		node.FootComment = rewriteProsePathsKeeping(node.FootComment, ordered, valueKeys, keep)
	}
	var walk func(node *yaml.Node, scopes []*yaml.Node)
	walk = func(node *yaml.Node, scopes []*yaml.Node) {
		rewrite(node, scopes)
		if node.Kind != yaml.MappingNode {
			for _, child := range node.Content {
				walk(child, scopes)
			}
			return
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i], node.Content[i+1]
			inner := scopes
			if value.Kind == yaml.MappingNode {
				inner = append(slices.Clone(scopes), value)
			}
			rewrite(key, inner)
			walk(value, inner)
		}
	}
	walk(document, nil)
}

// blockRelativePath reports whether a prose path names a key of one of the
// blocks the comment sits in rather than the top-level fleet key it starts
// with: its moved form resolves nowhere in the document, while the path as
// written resolves inside an enclosing block. The document root is not a
// scope — a path that resolves there is the moved form's own domain.
func blockRelativePath(root *yaml.Node, scopes []*yaml.Node, token, moved []string) bool {
	if pathExists(root, moved) {
		return false
	}
	for _, scope := range scopes {
		if pathExists(scope, token) {
			return true
		}
	}
	return false
}

// pathExists reports whether the dotted path resolves to a node under mapping.
func pathExists(mapping *yaml.Node, segments []string) bool {
	_, err := pathGet(mapping, strings.Join(segments, "."))
	return err == nil
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
