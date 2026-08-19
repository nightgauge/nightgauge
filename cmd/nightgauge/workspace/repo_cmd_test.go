package workspacecmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest/wsmfixture"
)

// newWorkspace builds a throwaway workspace rooted at a temp dir, chdirs into
// it, and returns the manifest path. Sibling repos are created as real
// directories with a .git entry so the add-path guards see the real thing
// rather than a mock.
func newWorkspace(t *testing.T, body string, siblings ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, ".vscode", "nightgauge-workspace.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range siblings {
		dir := filepath.Join(root, s)
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
	return p
}

// runRepo executes `workspace repo <args...>` and captures its output.
func runRepo(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := repoCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestRepoList(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic)

	out, err := runRepo(t, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, want := range []string{"NAME", "alpha", "beta", "gamma", "secondary", "3", "5"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestRepoListJSON(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic)

	out, err := runRepo(t, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var items []repoListItem
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3", len(items))
	}
	if items[0].Name != "alpha" || items[0].ProjectNumber != 3 || items[0].Path != "." {
		t.Errorf("items[0] = %+v", items[0])
	}
	if items[2].Role != "secondary" {
		t.Errorf("items[2].Role = %q, want secondary", items[2].Role)
	}
}

// TestRepoAddRejectsZeroProject is the footgun this command exists to close: a
// zero project number resolves to project 0 and silently misroutes issues.
func TestRepoAddRejectsZeroProject(t *testing.T) {
	path := newWorkspace(t, wsmfixture.Realistic, "delta")
	before, _ := os.ReadFile(path)

	out, err := runRepo(t, "add", "--name", "delta", "--path", "./delta", "--project", "0")
	if err == nil {
		t.Fatalf("--project 0 was accepted:\n%s", out)
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("unhelpful error for --project 0: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("manifest was modified by a rejected add")
	}
}

func TestRepoAddRejectsNegativeProject(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic, "delta")
	if _, err := runRepo(t, "add", "--name", "delta", "--path", "./delta", "--project", "-3"); err == nil {
		t.Fatal("--project -3 was accepted")
	}
}

func TestRepoAddRejectsDuplicateName(t *testing.T) {
	path := newWorkspace(t, wsmfixture.Realistic, "beta")
	before, _ := os.ReadFile(path)

	_, err := runRepo(t, "add", "--name", "beta", "--path", "./beta", "--project", "7")
	if err == nil {
		t.Fatal("duplicate name was accepted")
	}
	if !strings.Contains(err.Error(), "unique") && !strings.Contains(err.Error(), "already in the manifest") {
		t.Errorf("unhelpful duplicate error: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Error("manifest was modified by a rejected add")
	}
}

func TestRepoAddRejectsMissingPath(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic)
	_, err := runRepo(t, "add", "--name", "delta", "--path", "./nowhere", "--project", "9")
	if err == nil {
		t.Fatal("a non-existent path was accepted")
	}
	if !strings.Contains(err.Error(), "does not resolve to a directory") {
		t.Errorf("unhelpful missing-path error: %v", err)
	}
}

// TestRepoAddRejectsNonGitPath covers a directory that exists but is not a
// checkout — the manifest is a list of repositories, not of folders.
func TestRepoAddRejectsNonGitPath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".vscode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"),
		[]byte(wsmfixture.Realistic), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "notarepo"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	_, err := runRepo(t, "add", "--name", "notarepo", "--path", "./notarepo", "--project", "9")
	if err == nil {
		t.Fatal("a non-git directory was accepted")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("unhelpful non-git error: %v", err)
	}
}

func TestRepoAddRejectsBadRole(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic, "delta")
	_, err := runRepo(t, "add", "--name", "delta", "--path", "./delta", "--project", "9", "--role", "overlord")
	if err == nil {
		t.Fatal("an invalid role was accepted")
	}
	if !strings.Contains(err.Error(), "must be one of") {
		t.Errorf("unhelpful role error: %v", err)
	}
}

func TestRepoAddWritesValidatedEntry(t *testing.T) {
	path := newWorkspace(t, wsmfixture.Realistic, "delta")

	out, err := runRepo(t, "add", "--name", "delta", "--path", "./delta", "--role", "secondary", "--project", "9")
	if err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}
	data, _ := os.ReadFile(path)
	m, err := workspacemanifest.Parse(path, data)
	if err != nil {
		t.Fatalf("written manifest does not parse: %v", err)
	}
	e, ok := m.Find("delta")
	if !ok {
		t.Fatalf("delta not in manifest:\n%s", data)
	}
	if e.Path != "./delta" || e.Role != "secondary" || e.ProjectNumber != 9 {
		t.Errorf("entry = %+v", e)
	}
	if !strings.Contains(string(data), "# NOTE: delta is deliberately NOT listed") {
		t.Error("add destroyed the trailing NOTE block")
	}
}

// TestRepoRemoveRefusesRoutingReference stops the manifest from being left with
// routing pointing at a repository it no longer declares.
func TestRepoRemoveRefusesRoutingReference(t *testing.T) {
	for _, tc := range []struct{ name, wantRef string }{
		{"alpha", "routing.default_repository"},
		{"gamma", "routing.patterns[web].preferred_repo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := newWorkspace(t, wsmfixture.Realistic)
			before, _ := os.ReadFile(path)

			_, err := runRepo(t, "remove", "--name", tc.name)
			if err == nil {
				t.Fatalf("removing routing-referenced %q succeeded", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantRef) {
				t.Errorf("error does not name the reference %q: %v", tc.wantRef, err)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Error("manifest was modified by a refused remove")
			}
		})
	}
}

func TestRepoRemoveForceOverridesRoutingReference(t *testing.T) {
	path := newWorkspace(t, wsmfixture.Realistic)

	out, err := runRepo(t, "remove", "--name", "gamma", "--force")
	if err != nil {
		t.Fatalf("--force remove: %v\n%s", err, out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Errorf("--force removal did not warn about the dangling routing reference:\n%s", out)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "- name: gamma") {
		t.Error("gamma was not removed")
	}
}

// TestRepoRemoveReportsRetainedComment surfaces the comment-ownership decision
// to the operator instead of silently relocating text.
func TestRepoRemoveReportsRetainedComment(t *testing.T) {
	path := newWorkspace(t, wsmfixture.Realistic)

	out, err := runRepo(t, "remove", "--name", "alpha", "--force")
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if !strings.Contains(out, "comment block above alpha was retained") {
		t.Errorf("removal did not report the retained comment:\n%s", out)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# `project_number`") {
		t.Error("the list-level comment was deleted along with the entry")
	}
}

func TestRepoRemoveUnknownName(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic)
	_, err := runRepo(t, "remove", "--name", "nosuchrepo")
	if err == nil {
		t.Fatal("removing an unknown repository succeeded")
	}
	if !strings.Contains(err.Error(), "not in the manifest") {
		t.Errorf("unhelpful unknown-name error: %v", err)
	}
}

func TestRepoRequiresFlags(t *testing.T) {
	newWorkspace(t, wsmfixture.Realistic)
	if _, err := runRepo(t, "add", "--path", "./x", "--project", "1"); err == nil {
		t.Error("add without --name succeeded")
	}
	if _, err := runRepo(t, "add", "--name", "x", "--project", "1"); err == nil {
		t.Error("add without --path succeeded")
	}
	if _, err := runRepo(t, "remove"); err == nil {
		t.Error("remove without --name succeeded")
	}
}

// TestRepoOutsideWorkspace pins that the command says what is wrong rather than
// panicking or writing somewhere unexpected.
func TestRepoOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	prev, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	_, err := runRepo(t, "list")
	if err == nil {
		t.Fatal("list outside a workspace succeeded")
	}
	if !strings.Contains(err.Error(), "no workspace manifest found") {
		t.Errorf("unhelpful out-of-workspace error: %v", err)
	}
}
