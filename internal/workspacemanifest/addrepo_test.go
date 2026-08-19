package workspacemanifest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/workspacemanifest/wsmfixture"
)

// newWorkspace lays out a workspace root holding the manifest, plus sibling
// directories for the named repos. Siblings get a real .git entry so the
// add-path guards see the thing they actually check for.
func newWorkspace(t *testing.T, siblings ...string) *Manifest {
	t.Helper()
	parent := t.TempDir()
	root := filepath.Join(parent, "alpha")
	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".vscode", "nightgauge-workspace.yaml")
	if err := os.WriteFile(p, []byte(wsmfixture.Realistic), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range siblings {
		if err := os.MkdirAll(filepath.Join(parent, s, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return m
}

func fixedProject(n int) func(string) (int, error) {
	return func(string) (int, error) { return n, nil }
}

func TestAddRepo_AppendsAValidEntry(t *testing.T) {
	m := newWorkspace(t, "delta")

	got, err := AddRepo(m, Entry{Name: "delta", Path: "../delta", Role: "primary"}, fixedProject(9))
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if got.ProjectNumber != 9 {
		t.Errorf("ProjectNumber = %d, want the resolver's 9", got.ProjectNumber)
	}

	reread, err := Load(m.Path())
	if err != nil {
		t.Fatalf("re-Load: %v", err)
	}
	e, ok := reread.Find("delta")
	if !ok {
		t.Fatal("delta is not in the manifest after AddRepo")
	}
	if e.ProjectNumber != 9 || e.Path != "../delta" {
		t.Errorf("persisted entry = %+v, want path ../delta project 9", e)
	}
}

// The resolver is consulted ONLY when no project number was supplied.
func TestAddRepo_ExplicitProjectSkipsTheResolver(t *testing.T) {
	m := newWorkspace(t, "delta")
	called := false
	_, err := AddRepo(m, Entry{Name: "delta", Path: "../delta", ProjectNumber: 42},
		func(string) (int, error) { called = true; return 9, nil })
	if err != nil {
		t.Fatalf("AddRepo: %v", err)
	}
	if called {
		t.Error("the resolver must not be consulted when a project number was supplied")
	}
}

// project_number: 0 is the misroute the manifest's own comment block warns
// about — it must be unreachable from every path, including a resolver that
// hands back zero without erroring.
func TestAddRepo_RefusesAZeroProject(t *testing.T) {
	for _, tc := range []struct {
		name    string
		resolve func(string) (int, error)
	}{
		{"resolver returns zero", fixedProject(0)},
		{"resolver returns negative", fixedProject(-1)},
		{"resolver errors", func(string) (int, error) { return 0, errors.New("no board") }},
		{"no resolver at all", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := newWorkspace(t, "delta")
			_, err := AddRepo(m, Entry{Name: "delta", Path: "../delta"}, tc.resolve)
			if err == nil {
				t.Fatal("a zero project number must be refused")
			}
			reread, lerr := Load(m.Path())
			if lerr != nil {
				t.Fatalf("re-Load: %v", lerr)
			}
			if _, ok := reread.Find("delta"); ok {
				t.Error("a refused add must leave the manifest untouched")
			}
		})
	}
}

func TestAddRepo_RefusesADuplicateName(t *testing.T) {
	m := newWorkspace(t, "beta")
	_, err := AddRepo(m, Entry{Name: "beta", Path: "../beta"}, fixedProject(9))
	if err == nil || !strings.Contains(err.Error(), "already in the manifest") {
		t.Fatalf("error = %v, want a duplicate-name refusal", err)
	}
}

func TestAddRepo_RefusesAPathThatIsNotAGitCheckout(t *testing.T) {
	m := newWorkspace(t) // no siblings created
	_, err := AddRepo(m, Entry{Name: "delta", Path: "../delta"}, fixedProject(9))
	if err == nil {
		t.Fatal("a path that does not exist must be refused")
	}

	// Directory exists but carries no .git — still refused.
	parent := filepath.Dir(m.WorkspaceRoot())
	if mkErr := os.MkdirAll(filepath.Join(parent, "epsilon"), 0o755); mkErr != nil {
		t.Fatal(mkErr)
	}
	_, err = AddRepo(m, Entry{Name: "epsilon", Path: "../epsilon"}, fixedProject(9))
	if err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("error = %v, want a not-a-git-repository refusal", err)
	}
}

func TestAddRepo_RefusesAnInvalidRole(t *testing.T) {
	m := newWorkspace(t, "delta")
	_, err := AddRepo(m, Entry{Name: "delta", Path: "../delta", Role: "overlord"}, fixedProject(9))
	if err == nil || !strings.Contains(err.Error(), "role must be one of") {
		t.Fatalf("error = %v, want a role refusal", err)
	}
}

func TestDeriveEntry_FindsASiblingCheckout(t *testing.T) {
	m := newWorkspace(t, "delta")

	for _, spec := range []string{"acme/delta", "delta"} {
		e, err := DeriveEntry(m, spec, "primary")
		if err != nil {
			t.Fatalf("spec %q: DeriveEntry: %v", spec, err)
		}
		if e.Name != "delta" {
			t.Errorf("spec %q: Name = %q, want delta", spec, e.Name)
		}
		if e.Path != filepath.Join("..", "delta") {
			t.Errorf("spec %q: Path = %q, want ../delta", spec, e.Path)
		}
	}
}

// A manifest can legitimately omit the repository it lives in.
func TestDeriveEntry_HandlesTheWorkspaceRootItself(t *testing.T) {
	m := newWorkspace(t)
	e, err := DeriveEntry(m, "acme/alpha", "primary")
	if err != nil {
		t.Fatalf("DeriveEntry: %v", err)
	}
	if e.Path != "." {
		t.Errorf("Path = %q, want \".\" for the workspace root", e.Path)
	}
}

// A card naming a repository that has since been moved or deleted must fail
// loudly rather than writing an entry that points nowhere.
func TestDeriveEntry_RefusesARepoThatIsNotPresent(t *testing.T) {
	m := newWorkspace(t)
	if _, err := DeriveEntry(m, "acme/vanished", "primary"); err == nil {
		t.Fatal("a repository with no checkout in the workspace must be refused")
	}
}
