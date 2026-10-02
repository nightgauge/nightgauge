package agentworkspace

import (
	"path/filepath"
	"reflect"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/nightgauge/nightgauge/internal/platform"
)

func repos(decl platform.WorkspaceDeclaration) []platform.AgentRepo {
	return decl.Repos
}

// The extension hands the daemon its window's folders, and the daemon
// declares what the extension declares for that window, whichever folder the
// daemon's --workspace names (#2335 review).
func TestResolveWindow_DeclaresWhatTheExtensionDeclaresForTheWindow(t *testing.T) {
	hermetic(t)

	t.Run("a manifest in the first folder, which has no project config", func(t *testing.T) {
		// VS Code window [ws, ws/api, ws/web]. The daemon's --workspace is
		// ws/api, the first folder with a project config; the extension's
		// workspace is ws, where the manifest is.
		ws := twoRepoWorkspace(t, "Acme Platform")
		window := []string{ws, filepath.Join(ws, "api"), filepath.Join(ws, "web")}

		decl, err := ResolveServed(window, filepath.Join(ws, "api"))
		if err != nil {
			t.Fatal(err)
		}
		want := []platform.AgentRepo{{Owner: "acme", Repo: "api"}, {Owner: "acme", Repo: "web"}}
		if !reflect.DeepEqual(repos(decl), want) {
			t.Errorf("repos = %v, want %v", repos(decl), want)
		}
		if decl.Workspace == nil || decl.Workspace.Slug != "acme-platform" {
			t.Errorf("workspace = %+v, want the manifest's acme-platform block", decl.Workspace)
		}

		// Without the window the daemon can only declare its own root.
		own, _ := ResolveServed(nil, filepath.Join(ws, "api"))
		if !reflect.DeepEqual(repos(own), []platform.AgentRepo{{Owner: "acme", Repo: "api"}}) || own.Workspace != nil {
			t.Errorf("without the window: %+v, want the --workspace root alone", own)
		}
	})

	t.Run("a multi-root window with no manifest, every folder configured", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		write(t, filepath.Join(a, ".nightgauge", "config.yaml"), "github:\n  owner: acme\n  repo: api\n")
		write(t, filepath.Join(b, ".nightgauge", "nightgauge.yaml"), "owner: acme\nrepo: web\n")

		decl, _ := ResolveWindow([]string{a, b})
		want := []platform.AgentRepo{{Owner: "acme", Repo: "api"}, {Owner: "acme", Repo: "web"}}
		if !reflect.DeepEqual(repos(decl), want) || decl.Workspace != nil {
			t.Errorf("declared %+v, want both folders and no block", decl)
		}
	})

	t.Run("a multi-root window where one folder has no project config", func(t *testing.T) {
		a, b := t.TempDir(), t.TempDir()
		write(t, filepath.Join(a, ".nightgauge", "config.yaml"), "owner: acme\nrepo: api\n")

		decl, _ := ResolveWindow([]string{a, b})
		if want := []platform.AgentRepo{{Owner: "acme", Repo: "api"}}; !reflect.DeepEqual(repos(decl), want) {
			t.Errorf("repos = %v, want the first folder alone, as the extension's single mode", repos(decl))
		}
	})

	t.Run("an unusable manifest is not a reason to try the folders", func(t *testing.T) {
		// detectWorkspaceType throws on it and falls back to the root alone.
		root, other := t.TempDir(), t.TempDir()
		write(t, filepath.Join(root, ".vscode", "nightgauge-workspace.yaml"), "workspace: [unclosed\n")
		write(t, filepath.Join(root, ".nightgauge", "config.yaml"), "owner: acme\nrepo: root\n")
		write(t, filepath.Join(other, ".nightgauge", "config.yaml"), "owner: acme\nrepo: other\n")

		decl, _ := ResolveWindow([]string{root, other})
		if want := []platform.AgentRepo{{Owner: "acme", Repo: "root"}}; !reflect.DeepEqual(repos(decl), want) {
			t.Errorf("repos = %v, want the root alone", repos(decl))
		}
	})

	t.Run("the root is what the daemon's git.root answers for the first folder", func(t *testing.T) {
		// A repository opened at its top: git.root answers the folder itself.
		top := t.TempDir()
		if _, err := gogit.PlainInit(top, false); err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(top, ".nightgauge", "config.yaml"), "owner: acme\nrepo: mono\n")
		if decl, _ := ResolveWindow([]string{top}); !reflect.DeepEqual(repos(decl), []platform.AgentRepo{{Owner: "acme", Repo: "mono"}}) {
			t.Errorf("repository top: repos = %v, want acme/mono", repos(decl))
		}

		// A subfolder of it: the daemon's git.root does not look upwards
		// (go-git without DetectDotGit), so it fails, and the extension falls
		// back to the folder itself, which declares nothing here. The daemon
		// must answer the same, not the repository it happens to sit in.
		sub := filepath.Join(top, "packages", "app")
		write(t, filepath.Join(sub, "README.md"), "app\n")
		if got := windowRoot(sub); got != sub {
			t.Errorf("windowRoot(%s) = %s, want the folder itself, as git.root answers", sub, got)
		}
		if decl, _ := ResolveWindow([]string{sub}); len(decl.Repos) != 0 {
			t.Errorf("subfolder: repos = %v, want none, as the extension declares", decl.Repos)
		}
	})
}

func TestWindowFolders_ReadsTheExtensionsVariable(t *testing.T) {
	env := func(v string) func(string) string {
		return func(key string) string {
			if key == WindowFoldersEnv {
				return v
			}
			return ""
		}
	}
	if got := WindowFolders(env(`["/w/a","/w/b"]`)); !reflect.DeepEqual(got, []string{"/w/a", "/w/b"}) {
		t.Errorf("WindowFolders = %v, want both folders in order", got)
	}
	for _, bad := range []string{"", "  ", "/w/a", `{"a":1}`, `["/w/a",""]`, `[1]`} {
		if got := WindowFolders(env(bad)); got != nil {
			t.Errorf("WindowFolders(%q) = %v, want nil", bad, got)
		}
	}
}
