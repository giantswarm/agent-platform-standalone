package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSpaceInlineComments(t *testing.T) {
	in := `# head comment stays
a: 1 # one space
b: 2   # already wide
c: "x # not a comment" # real
d: 'y # neither' # real
e: "esc \" # still quoted" # real
f: |
  block # content, untouched
  more # content
g: value
h: >-
  folded # content
i: last # tail
url: http://example.com/#anchor
`
	want := `# head comment stays
a: 1  # one space
b: 2   # already wide
c: "x # not a comment"  # real
d: 'y # neither'  # real
e: "esc \" # still quoted"  # real
f: |
  block # content, untouched
  more # content
g: value
h: >-
  folded # content
i: last  # tail
url: http://example.com/#anchor
`
	require.Equal(t, want, string(spaceInlineComments([]byte(in))))
}

func TestSpaceInlineCommentsIsIdempotent(t *testing.T) {
	once := spaceInlineComments([]byte("a: 1 # c\nb:\n  - x # d\n"))
	require.Equal(t, string(once), string(spaceInlineComments(once)))
}
