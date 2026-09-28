package gates

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

const boundaryManifest = ".github/publication-boundary.yaml"

// initBoundaryRepo builds a repo whose `main` baseline tracks the allowlist,
// then checks out a feature branch the way a pipeline worktree does.
func initBoundaryRepo(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	gittest.Run(t, ws, "init", "-b", "main")
	writeRepoFile(t, ws, boundaryManifest, "allow: []\n")
	writeRepoFile(t, ws, "README.md", "base\n")
	gittest.Run(t, ws, "add", ".")
	gittest.Run(t, ws, "commit", "-m", "base")
	gittest.Run(t, ws, "checkout", "-b", "feat/1-x")
	return ws
}

func writeRepoFile(t *testing.T, ws, rel, content string) {
	t.Helper()
	full := filepath.Join(ws, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCheckProtectedPaths(t *testing.T) {
	cases := []struct {
		name  string
		act   func(t *testing.T, ws string)
		wantX bool // want the check to fail
	}{
		{"unrelated change passes", func(t *testing.T, ws string) {
			writeRepoFile(t, ws, "README.md", "changed\n")
			gittest.Run(t, ws, "commit", "-am", "docs")
		}, false},
		{"uncommitted edit fails", func(t *testing.T, ws string) {
			writeRepoFile(t, ws, boundaryManifest, "allow: [\"**\"]\n")
		}, true},
		{"committed edit fails", func(t *testing.T, ws string) {
			writeRepoFile(t, ws, boundaryManifest, "allow: [\"**\"]\n")
			gittest.Run(t, ws, "commit", "-am", "widen")
		}, true},
		{"staged deletion fails", func(t *testing.T, ws string) {
			gittest.Run(t, ws, "rm", "-q", boundaryManifest)
		}, true},
		{"edit hidden by assume-unchanged fails", func(t *testing.T, ws string) {
			gittest.Run(t, ws, "update-index", "--assume-unchanged", boundaryManifest)
			writeRepoFile(t, ws, boundaryManifest, "allow: [\"**\"]\n")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := initBoundaryRepo(t)
			tc.act(t, ws)
			res := CheckProtectedPaths(ws)
			if res.Passed == tc.wantX {
				t.Fatalf("Passed=%v, want %v (reason %q)", res.Passed, !tc.wantX, res.Reason)
			}
			if tc.wantX {
				if res.Kind != KindFail || len(res.Evidence) != 1 || res.Evidence[0] != boundaryManifest {
					t.Fatalf("got kind %q evidence %v", res.Kind, res.Evidence)
				}
				if !strings.Contains(res.Reason, "#1970") {
					t.Fatalf("reason does not cite the control: %q", res.Reason)
				}
			}
		})
	}
}

func TestCheckProtectedPaths_NotARepoPasses(t *testing.T) {
	if res := CheckProtectedPaths(t.TempDir()); !res.Passed {
		t.Fatalf("non-repo workspace failed: %q", res.Reason)
	}
}
