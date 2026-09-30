package hometest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// stateOverrideNeedles are the ways a test file points the machine-state root
// somewhere: the two environment variables and layout's name for the first.
// Any of them in a package's tests means that package overrides STATE.
var stateOverrideNeedles = []string{"NIGHTGAUGE_STATE_HOME", "XDG_STATE_HOME", "EnvStateHome"}

// isolateNeedle is the call that isolates HOME for a whole test binary.
// Assembled from two pieces so a file that merely quotes it is not mistaken
// for one that calls it.
var isolateNeedle = "hometest." + "Isolate("

// TestEveryStateOverrideIsolatesHome is the ratchet for #2311. A package whose
// tests override STATE but leave HOME real can spawn a CLI that sees the
// developer's legacy ~/.nightgauge and an empty temp STATE: the machine-state
// migration then moves the legacy data into the temp STATE, and test cleanup
// deletes it. That happened during a local gate run, and nothing was
// recoverable. So any package that overrides STATE must isolate HOME in its
// TestMain with Isolate, which moves HOME and STATE together.
//
// Like the gittest spawn ban, the check is textual: the rule it pins is
// "mentions a STATE override, so calls Isolate", and a grep-shaped rule is the
// one the issue states.
func TestEveryStateOverrideIsolatesHome(t *testing.T) {
	offenders, err := stateOverridesWithoutIsolate(moduleRoot(t))
	if err != nil {
		t.Fatalf("walk module: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("%d test package(s) override the machine-state root without isolating HOME:\n  %s\n\n"+
			"A STATE override (%s) with the real HOME lets a spawned CLI migrate the developer's "+
			"~/.nightgauge into a temp directory that cleanup then deletes (#2311). Call "+
			"hometest.Isolate() from the package's TestMain; it sets HOME and NIGHTGAUGE_STATE_HOME together.",
			len(offenders), strings.Join(offenders, "\n  "), strings.Join(stateOverrideNeedles, ", "))
	}
}

// TestStateOverrideLintCatchesAPackage proves the lint reports a package that
// sets a STATE override and never calls Isolate, and accepts it once it does.
func TestStateOverrideLintCatchesAPackage(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "internal", "leaky")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	leaky := "package leaky\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tt.Setenv(\"NIGHTGAUGE_STATE_HOME\", t.TempDir())\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "x_test.go"), []byte(leaky), 0o644); err != nil {
		t.Fatal(err)
	}
	offenders, err := stateOverridesWithoutIsolate(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("internal", "leaky")
	if len(offenders) != 1 || offenders[0] != want {
		t.Fatalf("offenders = %q, want [%q]", offenders, want)
	}

	main := "package leaky\n\nfunc TestMain(m *testing.M) {\n\tcleanup := " + isolateNeedle + ")\n\tcode := m.Run()\n\tcleanup()\n\tos.Exit(code)\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "main_test.go"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	if offenders, err = stateOverridesWithoutIsolate(root); err != nil || len(offenders) != 0 {
		t.Fatalf("with Isolate: offenders = %q, err = %v; want none", offenders, err)
	}
}

// stateOverridesWithoutIsolate walks every Go test file under root and
// returns, sorted and relative to root, each directory where a test file
// mentions a STATE override but no test file calls Isolate. This package is
// exempt: it defines Isolate and names the variables it sets.
func stateOverridesWithoutIsolate(root string) ([]string, error) {
	overrides := map[string]bool{}
	isolated := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if gittest.TolerateConcurrentScratchWrite(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "testdata", ".worktrees", "dist", "out":
				return filepath.SkipDir
			}
			if gittest.IsScratchOutputDir(info.Name()) {
				return filepath.SkipDir
			}
			// A nested checkout is not this module's source (see the gittest
			// spawn ban for the same rule).
			if path != root {
				if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		if rel == filepath.Join("internal", "hometest") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		if strings.Contains(text, isolateNeedle) {
			isolated[rel] = true
		}
		for _, n := range stateOverrideNeedles {
			if strings.Contains(text, n) {
				overrides[rel] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var out []string
	for dir := range overrides {
		if !isolated[dir] {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
