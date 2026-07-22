package workspacecmd

import (
	"os"
	"path/filepath"
	"testing"
)

// ── #3859: splitKV quote stripping ───────────────────────────────────────────

func TestSplitKV_StripsSurroundingQuotes(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{"unquoted", `name: Acmesvc Product`, "name", "Acmesvc Product", true},
		{"double_quoted", `name: "Acmesvc Product"`, "name", "Acmesvc Product", true},
		{"single_quoted", `name: 'Acmesvc Product'`, "name", "Acmesvc Product", true},
		{"double_quoted_empty", `name: ""`, "name", "", true},
		{"embedded_quote_kept", `name: He said "hi"`, "name", `He said "hi"`, true},
		{"unbalanced_leading", `name: "Acmesvc`, "name", `"Acmesvc`, true},
		{"unbalanced_trailing", `name: Acmesvc"`, "name", `Acmesvc"`, true},
		{"mismatched_pair", `name: "Acmesvc'`, "name", `"Acmesvc'`, true},
		{"key_only", `repositories:`, "repositories", "", true},
		{"no_separator", `- list item`, "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kv, ok := splitKV(tc.line)
			if ok != tc.wantOK {
				t.Fatalf("splitKV(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if kv[0] != tc.wantKey {
				t.Errorf("key = %q, want %q", kv[0], tc.wantKey)
			}
			if kv[1] != tc.wantVal {
				t.Errorf("value = %q, want %q", kv[1], tc.wantVal)
			}
		})
	}
}

func TestStripQuotes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`hello`, "hello"},
		{`""`, ""},
		{`''`, ""},
		{`"`, `"`}, // single char — too short to be a pair
		{``, ``},   // empty
		{`"unbalanced`, `"unbalanced`},
		{`unbalanced"`, `unbalanced"`},
		{`"mismatch'`, `"mismatch'`},
		{`a "quoted" middle`, `a "quoted" middle`},
	}
	for _, tc := range cases {
		if got := stripQuotes(tc.in); got != tc.want {
			t.Errorf("stripQuotes(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// parseSyncWorkspaceYAML must strip quotes from workspace.name and repo
// name/path so display_name has no embedded quotes (#3859 AC #3).
func TestParseSyncWorkspaceYAML_QuotedName(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "nightgauge-workspace.yaml")
	content := `workspace:
  name: "Acmesvc Product"
  description: shared backlog

repositories:
  - name: "acmesvc-tracker"
    path: acmesvc-tracker
  - name: acmesvc-api
    path: 'acmesvc-api'
`
	if err := os.WriteFile(yamlPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ws, err := parseSyncWorkspaceYAML(yamlPath)
	if err != nil {
		t.Fatalf("parseSyncWorkspaceYAML: %v", err)
	}

	if ws.Name != "Acmesvc Product" {
		t.Errorf("workspace.Name = %q, want %q (quotes stripped)", ws.Name, "Acmesvc Product")
	}
	if len(ws.Repositories) != 2 {
		t.Fatalf("got %d repositories, want 2", len(ws.Repositories))
	}
	if ws.Repositories[0].Name != "acmesvc-tracker" {
		t.Errorf("repo[0].Name = %q, want %q", ws.Repositories[0].Name, "acmesvc-tracker")
	}
	if ws.Repositories[1].Path != "acmesvc-api" {
		t.Errorf("repo[1].Path = %q, want %q", ws.Repositories[1].Path, "acmesvc-api")
	}
}

// ── #3859: assembleRepos includes github:-block-only member configs ───────────

// writeMemberConfig writes a member repo's .nightgauge/config.yaml under
// wsRoot/relPath and returns nothing — the directory tree is created.
func writeMemberConfig(t *testing.T, wsRoot, relPath, yaml string) {
	t.Helper()
	cfgDir := filepath.Join(wsRoot, relPath, ".nightgauge")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestAssembleRepos_IncludesGitHubBlockMember(t *testing.T) {
	wsRoot := t.TempDir()

	// Member 1: canonical top-level owner/repo.
	writeMemberConfig(t, wsRoot, "acmesvc-tracker", `owner: Acme-Community
repo: acmesvc-tracker
project:
  number: 1`)

	// Member 2: legacy github:-block-only config (no top-level repo:). Before
	// the #3859 loader heal this resolved an empty DefaultRepo and was skipped.
	writeMemberConfig(t, wsRoot, "acmesvc-api", `github:
  owner: Acme-Community
  repo: acmesvc-api
project:
  number: 1`)

	entries := []syncRepoEntry{
		{Name: "acmesvc-tracker", Path: "acmesvc-tracker"},
		{Name: "acmesvc-api", Path: "acmesvc-api"},
	}

	repos := assembleRepos(wsRoot, entries)

	if len(repos) != 2 {
		t.Fatalf("assembleRepos returned %d repos, want 2 (github:-block member must be included): %+v", len(repos), repos)
	}
	want := map[string]bool{"acmesvc-tracker": true, "acmesvc-api": true}
	for _, r := range repos {
		if r.Owner != "Acme-Community" {
			t.Errorf("repo %q owner = %q, want %q", r.Repo, r.Owner, "Acme-Community")
		}
		if !want[r.Repo] {
			t.Errorf("unexpected repo %q in payload", r.Repo)
		}
		delete(want, r.Repo)
	}
	if len(want) != 0 {
		t.Errorf("missing repos from payload: %v", want)
	}
}
