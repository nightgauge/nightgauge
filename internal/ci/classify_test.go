package ci

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyForCIPRHeavy(t *testing.T) {
	for _, path := range PRHeavyPaths {
		got := ClassifyForCI([]string{"docs/x.md", path})
		if !got.PRHeavy || !got.RunHeavy {
			t.Errorf("%s: PRHeavy=%v RunHeavy=%v, want both true", path, got.PRHeavy, got.RunHeavy)
		}
		if !strings.Contains(got.Reason, path) {
			t.Errorf("%s: reason %q does not name the path", path, got.Reason)
		}
	}
	for _, files := range [][]string{
		{"scripts/ci-local.sh"},
		{"docs/x.md"},
		{"internal/ci/classify.go"},
		{"other/scripts/install-npm-cli.sh"},
		nil,
	} {
		if got := ClassifyForCI(files); got.PRHeavy {
			t.Errorf("%v: PRHeavy = true, want false", files)
		}
	}
	if !FailOpenResult("x").PRHeavy {
		t.Error("FailOpenResult: PRHeavy = false, want true")
	}
}

// A workflow edit selects the workflow-reading tests; nothing else does (#3483).
func TestClassifyForCIWorkflowTests(t *testing.T) {
	for _, tc := range []struct {
		files []string
		want  bool
	}{
		{[]string{".github/workflows/ci.yml"}, true},
		{[]string{"docs/x.md", ".github/workflows/release.yml"}, true},
		{[]string{"internal/x.go"}, false},
		{[]string{"docs/GUIDE.md"}, false},
		{[]string{".github/dependabot.yml"}, false},
		{[]string{".github/workflows-archive/x.yml"}, false},
		{[]string{"sub/.github/workflows/ci.yml"}, false},
		{nil, false},
	} {
		if got := ClassifyForCI(tc.files).WorkflowTests; got != tc.want {
			t.Errorf("%v: WorkflowTests = %v, want %v", tc.files, got, tc.want)
		}
	}
	if !FailOpenResult("x").WorkflowTests {
		t.Error("FailOpenResult: WorkflowTests = false, want true")
	}
	for _, pkg := range WorkflowReadingTestPackages {
		if _, err := os.Stat(filepath.Join("..", "..", pkg)); err != nil {
			t.Errorf("WorkflowReadingTestPackages names %s: %v", pkg, err)
		}
	}
}

func TestClassifyForCI(t *testing.T) {
	tests := []struct {
		name      string
		files     []string
		wantClass string
		wantHeavy bool
	}{
		{"docs only fast-tracks", []string{"docs/x.md", "README.md"}, "docs_only", false},
		{"empty fast-tracks", nil, "empty", false},
		{"source runs full", []string{"internal/x.go"}, "source", true},
		{"mixed runs full", []string{"docs/x.md", "internal/x.go"}, "mixed", true},
		// Config is deliberately NOT fast-tracked for CI: a .github/ or
		// package.json edit can need build+test, and the CI files themselves are
		// config — skipping would let an untested CI change merge.
		{"config runs full", []string{".github/workflows/ci.yml"}, "config_only", true},
		{"package.json runs full", []string{"package.json"}, "config_only", true},
		// The vscode-host job carries the demo drift guard (#2109): it must run
		// whenever the extension or the IPC protocol changes.
		{"extension change runs full", []string{"packages/nightgauge/src/services/IpcClient.ts"}, "source", true},
		{"IPC protocol change runs full", []string{"internal/ipc/server.go"}, "source", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyForCI(tt.files)
			if got.ChangeClass != tt.wantClass {
				t.Errorf("ChangeClass = %q, want %q", got.ChangeClass, tt.wantClass)
			}
			if got.RunHeavy != tt.wantHeavy {
				t.Errorf("RunHeavy = %v, want %v", got.RunHeavy, tt.wantHeavy)
			}
			// Every heavy job flag mirrors RunHeavy, and RunHeavy is their OR.
			for _, k := range HeavyJobKeys {
				if got.Jobs[k] != tt.wantHeavy {
					t.Errorf("Jobs[%q] = %v, want %v", k, got.Jobs[k], tt.wantHeavy)
				}
			}
		})
	}
}

// Every package whose tests read this repository's workflows is in
// WorkflowReadingTestPackages, so the list cannot fall behind (#3483).
func TestWorkflowReadingTestPackagesComplete(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range WorkflowReadingTestPackages {
		listed[strings.TrimSuffix(strings.TrimPrefix(p, "./"), "/")] = true
	}
	needles := []string{`"../../.github/workflows"`, `repoRoot, ".github", "workflows"`, "ListWorkflowSteps(repoRoot", "ListWorkflowSteps(root"}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && path != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, n := range needles {
			if strings.Contains(string(b), n) {
				rel, _ := filepath.Rel(root, filepath.Dir(path))
				if !listed[filepath.ToSlash(rel)] {
					t.Errorf("%s reads the repository workflows but %s is not in WorkflowReadingTestPackages", path, rel)
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
