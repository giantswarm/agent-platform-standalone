package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const lockA = `dependencies:
- name: muster
  repository: oci://example/charts
  version: 5.5.3
digest: sha256:aaa
generated: "2026-08-27T14:06:23.756256077+02:00"
`

func TestLocksEquivalentIgnoresGenerated(t *testing.T) {
	lockB := lockA[:len(lockA)-len("generated: \"2026-08-27T14:06:23.756256077+02:00\"\n")] + "generated: \"2027-01-01T00:00:00Z\"\n"
	same, err := locksEquivalent([]byte(lockA), []byte(lockB))
	require.NoError(t, err)
	require.True(t, same)
}

func TestLocksEquivalentDetectsPinChange(t *testing.T) {
	changed := []byte(lockA)
	changed = []byte(string(changed[:len(changed)-0]))
	changed = []byte(replaceOnce(string(changed), "version: 5.5.3", "version: 5.6.0"))
	same, err := locksEquivalent([]byte(lockA), changed)
	require.NoError(t, err)
	require.False(t, same)
}

func TestLocksEquivalentRejectsGarbage(t *testing.T) {
	_, err := locksEquivalent([]byte(lockA), []byte("::not yaml"))
	require.Error(t, err)
}

func replaceOnce(s, old, new string) string {
	for i := 0; i+len(old) <= len(s); i++ {
		if s[i:i+len(old)] == old {
			return s[:i] + new + s[i+len(old):]
		}
	}
	return s
}
