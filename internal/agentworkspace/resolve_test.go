package agentworkspace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// hermetic pins the machine id a registration sends. HOME and the
// machine-state root are isolated for the whole package by TestMain.
func hermetic(t *testing.T) {
	t.Helper()
	t.Setenv("NIGHTGAUGE_AGENT_ID", "test-machine-uuid")
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// twoRepoWorkspace lays out a manifest naming the workspace and listing two
// member repositories, one per config form the extension reads.
func twoRepoWorkspace(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	manifest := "workspace:\n  name: " + name + "\n" +
		"repositories:\n  - name: api\n    path: api\n  - name: web\n    path: web\n"
	write(t, filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"), manifest)
	write(t, filepath.Join(root, "api", ".nightgauge", "config.yaml"), "github:\n  owner: acme\n  repo: api\n")
	write(t, filepath.Join(root, "web", ".nightgauge", "config.yaml"), "owner: acme\nrepo: web\n")
	return root
}

// registeredBody runs a real registration for the workspace at root, with
// Resolve as its source, and returns the body the platform received.
func registeredBody(t *testing.T, root string) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("register body is not JSON: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"agentId":"9c1f0f2e-1111-4222-8333-444455556666","ttl_seconds":90}`))
	}))
	defer srv.Close()
	client, err := platform.NewClient(platform.Config{BaseURL: srv.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	reg := platform.NewAgentRegistrationService(client, "1.2.3").
		WithWorkspace(func() (platform.WorkspaceDeclaration, error) { return Resolve(root) })
	if _, err := reg.RegisterAgent(context.Background()); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	return body
}

// The acceptance test of #2335: a daemon started on a workspace with two
// repos sends both, and the workspace block when the config names the
// workspace, on the registration's request body.
func TestDaemonRegistrationDeclaresTheWorkspace(t *testing.T) {
	hermetic(t)

	body := registeredBody(t, twoRepoWorkspace(t, "Acme Platform"))
	repos, _ := json.Marshal(body["repos"])
	if string(repos) != `[{"owner":"acme","repo":"api"},{"owner":"acme","repo":"web"}]` {
		t.Errorf("repos = %s, want both of the workspace's repos", repos)
	}
	ws, _ := body["workspace"].(map[string]any)
	if len(ws) != 2 || ws["slug"] != "acme-platform" || ws["display_name"] != "Acme Platform" {
		t.Errorf("workspace = %v, want {slug: acme-platform, display_name: Acme Platform}", body["workspace"])
	}

	// A workspace with no manifest is a single repository, which the config
	// does not name as a workspace: its repo is declared, and no block is
	// sent, as the extension does.
	single := t.TempDir()
	write(t, filepath.Join(single, ".nightgauge", "config.yaml"), "owner: acme\nrepo: solo\n")
	body = registeredBody(t, single)
	if repos, _ := json.Marshal(body["repos"]); string(repos) != `[{"owner":"acme","repo":"solo"}]` {
		t.Errorf("repos = %s, want the single repository", repos)
	}
	if _, ok := body["workspace"]; ok {
		t.Errorf("an unnamed workspace sent a block: %v", body["workspace"])
	}
}

// The repo set follows the extension's rules case by case.
func TestResolve_FollowsTheExtensionsRules(t *testing.T) {
	hermetic(t)
	repo := func(owner, name string) platform.AgentRepo { return platform.AgentRepo{Owner: owner, Repo: name} }

	cases := []struct {
		name  string
		files map[string]string
		want  []platform.AgentRepo
	}{
		{
			name:  "single repo, no manifest: the root's own config",
			files: map[string]string{".nightgauge/config.yaml": "owner: acme\nrepo: solo\n"},
			want:  []platform.AgentRepo{repo("acme", "solo")},
		},
		{
			name:  "the legacy file when config.yaml is absent",
			files: map[string]string{".nightgauge/nightgauge.yaml": "github:\n  owner: acme\n  repo: old\n"},
			want:  []platform.AgentRepo{repo("acme", "old")},
		},
		{
			name: "a github block wins over flat keys, even incomplete",
			files: map[string]string{
				".vscode/nightgauge-workspace.yaml": "workspace:\n  name: W\nrepositories:\n  - name: a\n    path: a\n  - name: b\n    path: b\n",
				"a/.nightgauge/config.yaml":         "github:\n  owner: acme\nowner: acme\nrepo: flat\n",
				"b/.nightgauge/config.yaml":         "github:\n  owner: acme\n  repo: b\n",
			},
			want: []platform.AgentRepo{repo("acme", "b")},
		},
		{
			name: "a falsy github value falls through to the flat keys",
			files: map[string]string{
				".nightgauge/config.yaml": "github: \"\"\nowner: acme\nrepo: flat\n",
			},
			want: []platform.AgentRepo{repo("acme", "flat")},
		},
		{
			name: "a repo with only an owner is not declared",
			files: map[string]string{
				".vscode/nightgauge-workspace.yaml": "workspace:\n  name: W\nrepositories:\n  - name: a\n    path: a\n  - name: b\n    path: b\n",
				"a/.nightgauge/config.yaml":         "owner: acme\n",
				"b/.nightgauge/config.yaml":         "owner: acme\nrepo: b\n",
			},
			want: []platform.AgentRepo{repo("acme", "b")},
		},
		{
			name: "no identified repo: the effective enabled_repos",
			files: map[string]string{
				".nightgauge/config.yaml": "owner: acme\nautonomous:\n  enabled_repos:\n    - acme/one\n    - bare\n    - acme/two\n",
			},
			want: []platform.AgentRepo{repo("acme", "one"), repo("acme", "two")},
		},
		{
			name: "identified repos win over enabled_repos",
			files: map[string]string{
				".nightgauge/config.yaml": "owner: acme\nrepo: solo\nautonomous:\n  enabled_repos:\n    - acme/other\n",
			},
			want: []platform.AgentRepo{repo("acme", "solo")},
		},
		{
			name: "N:1 topology: the extension identifies each project repo by the root's config",
			files: map[string]string{
				".vscode/nightgauge-workspace.yaml": "workspace:\n  name: Shared\n  shared_project_number: 3\nrepositories: []\n",
				".nightgauge/config.yaml":           "owner: acme\nrepo: hub\n",
			},
			want: []platform.AgentRepo{repo("acme", "hub")},
		},
		{
			name: "an invalid manifest is ignored, as the extension ignores it: the root alone",
			files: map[string]string{
				".vscode/nightgauge-workspace.yaml": "workspace:\n  name: W\nrepositories: []\n",
				".nightgauge/config.yaml":           "owner: acme\nrepo: hub\n",
				"a/.nightgauge/config.yaml":         "owner: acme\nrepo: a\n",
			},
			want: []platform.AgentRepo{repo("acme", "hub")},
		},
		{
			name: "an unnamed manifest is invalid too",
			files: map[string]string{
				".vscode/nightgauge-workspace.yaml": "repositories:\n  - name: a\n    path: a\n",
				".nightgauge/config.yaml":           "owner: acme\nrepo: hub\n",
				"a/.nightgauge/config.yaml":         "owner: acme\nrepo: a\n",
			},
			want: []platform.AgentRepo{repo("acme", "hub")},
		},
		{
			name:  "nothing to declare",
			files: map[string]string{},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for p, body := range tc.files {
				write(t, filepath.Join(root, p), body)
			}
			got, err := Resolve(root)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if !reflect.DeepEqual(got.Repos, tc.want) {
				t.Errorf("repos = %v, want %v", got.Repos, tc.want)
			}
		})
	}
}

// The workspace block follows the manifest: sent for a valid one, which
// always names the workspace, and never for an invalid one.
func TestResolve_WorkspaceBlockFollowsAValidManifest(t *testing.T) {
	hermetic(t)
	got, err := Resolve(twoRepoWorkspace(t, "Acme Platform"))
	if err != nil || got.Workspace == nil || got.Workspace.Slug != "acme-platform" {
		t.Fatalf("Resolve = %+v, %v; want the acme-platform block", got.Workspace, err)
	}

	root := t.TempDir()
	write(t, filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"), "workspace:\n  name: Acme\nrepositories: [unclosed\n")
	got, err = Resolve(root)
	if err != nil || got.Workspace != nil {
		t.Errorf("an unparseable manifest gave block %+v, err %v; want none", got.Workspace, err)
	}
}
