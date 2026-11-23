// Package workspacetest builds a multi-repository workspace layout for tests.
//
// # Why this exists (#3275)
//
// Code that resolves the working directory's workspace behaves differently
// when the checkout sits inside a directory holding a workspace manifest and
// two or more configured sibling repositories. Tests for that case need the
// same layout; this package is its one copy.
package workspacetest

import (
	"os"
	"path/filepath"
	"testing"
)

// TwoRepos writes a workspace manifest naming repositories a and b, and a
// config for each (owner o), under t.TempDir(). It returns the workspace root
// and the path of repository a. It does not change the working directory.
func TwoRepos(t testing.TB) (wsRoot, repoA string) {
	t.Helper()
	wsRoot = t.TempDir()
	files := map[string]string{
		".vscode/nightgauge-workspace.yaml": "repositories:\n  - name: a\n    path: a\n  - name: b\n    path: b\n",
		"a/.nightgauge/config.yaml":         "owner: o\ndefaultRepo: a\n",
		"b/.nightgauge/config.yaml":         "owner: o\ndefaultRepo: b\n",
	}
	for name, body := range files {
		path := filepath.Join(wsRoot, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return wsRoot, filepath.Join(wsRoot, "a")
}
