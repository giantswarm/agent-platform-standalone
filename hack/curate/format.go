package main

import (
	"bytes"
	"strings"
)

// spaceInlineComments rewrites `value # comment` to `value  # comment`.
// yaml.v3 emits one space before an inline comment; chart-testing's yamllint
// rule `comments.min-spaces-from-content` demands two. Lines inside block
// scalars and characters inside quoted scalars are left alone.
func spaceInlineComments(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	blockIndent := -1
	for i, line := range lines {
		if blockIndent >= 0 {
			if strings.TrimSpace(line) == "" || indentOf(line) > blockIndent {
				continue
			}
			blockIndent = -1
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		commentAt := inlineCommentIndex(line)
		content := line
		if commentAt >= 0 {
			content = line[:commentAt]
			if !strings.HasSuffix(content, "  ") {
				lines[i] = strings.TrimRight(content, " ") + "  " + line[commentAt:]
			}
		}
		if startsBlockScalar(content) {
			blockIndent = indentOf(line)
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// inlineCommentIndex returns the index of the `#` that starts an inline
// comment, or -1. A `#` inside a quoted scalar is not a comment.
func inlineCommentIndex(line string) int {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote == '"' && c == '\\':
			i++
		case quote != 0 && c == quote:
			quote = 0
		case quote == 0 && (c == '"' || c == '\''):
			quote = c
		case quote == 0 && c == '#' && i > 0 && line[i-1] == ' ':
			return i
		}
	}
	return -1
}

// startsBlockScalar reports whether the content part of a line ends with a
// block scalar indicator (`key: |`, `- >-`, `key: |2`).
func startsBlockScalar(content string) bool {
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false
	}
	last := fields[len(fields)-1]
	if last[0] != '|' && last[0] != '>' {
		return false
	}
	return strings.Trim(last[1:], "+-0123456789") == ""
}

func indentOf(line string) int {
	return len(line) - len(bytes.TrimLeft([]byte(line), " "))
}
