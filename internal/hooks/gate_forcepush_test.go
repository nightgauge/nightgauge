package hooks

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

// forcePushFixture builds a clone whose current branch is feat/x tracking
// origin/feat/x, a detached clone, and a clone on main.
func forcePushFixture(t *testing.T) (repo, detached, onMain string) {
	t.Helper()
	root := t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	run(root, "init", "-q", "--bare", "-b", "main", remote)
	run(root, "init", "-q", "-b", "main", seed)
	run(seed, "commit", "-q", "--allow-empty", "-m", "init")
	run(seed, "remote", "add", "origin", remote)
	run(seed, "push", "-q", "origin", "main", "main:feat/x")
	repo = filepath.Join(root, "repo")
	run(root, "clone", "-q", remote, repo)
	run(repo, "checkout", "-q", "-b", "feat/x", "--track", "origin/feat/x")
	detached = filepath.Join(root, "detached")
	run(root, "clone", "-q", remote, detached)
	run(detached, "checkout", "-q", "--detach", "HEAD")
	onMain = filepath.Join(root, "onmain")
	run(root, "clone", "-q", remote, onMain)
	return repo, detached, onMain
}

func gateWithCwd(t *testing.T, cmd, cwd string) GateDecision {
	t.Helper()
	ti, _ := json.Marshal(BashToolInput{Command: cmd})
	data, _ := json.Marshal(GateInput{ToolName: "Bash", ToolInput: ti, Cwd: cwd})
	return EvaluateGate(data, config.SanitizationModeBlock)
}

func TestForcePushGate(t *testing.T) {
	t.Setenv(skipWorkflowGateEnv, "")
	repo, detached, onMain := forcePushFixture(t)
	cases := []struct {
		name, cmd, cwd, want string
	}{
		// Allowed: force push to a non-default branch (rulesets are the guard).
		{"lease, upstream feat/x", "git push --force-with-lease", repo, "allow"},
		{"lease with remote", "git push --force-with-lease origin", repo, "allow"},
		{"if-includes", "git push --force-with-lease --force-if-includes", repo, "allow"},
		{"bare --force explicit branch", "git push --force origin feat/x", repo, "allow"},
		{"-f explicit branch", "git push -f origin feat/42", repo, "allow"},
		{"-fu cluster", "git push -fu origin feat/42", repo, "allow"},
		{"+refspec branch", "git push origin +feat/x", repo, "allow"},
		{"HEAD refspec on feature", "git push -f origin HEAD", repo, "allow"},
		{"HEAD:feature", "git push --force-with-lease origin HEAD:refs/heads/feat/y", repo, "allow"},
		{"bash -c wrapper", "bash -c 'git push --force-with-lease'", repo, "allow"},
		{"env wrapper", "env GIT_TRACE=0 git push -f origin feat/x", repo, "allow"},
		{"cd && push", "cd " + repo + " && git push --force-with-lease", onMain, "allow"},
		{"git -C push", "git -C " + repo + " push --force-with-lease", onMain, "allow"},
		{"detached, no refspec", "git push -f origin", detached, "allow"},

		// Blocked: every form that targets main/master.
		{"-f main", "git push -f origin main", repo, "block"},
		{"lease HEAD:main", "git push --force-with-lease origin HEAD:main", repo, "block"},
		{"+main", "git push origin +main", repo, "block"},
		{"+master", "git push origin +master", repo, "block"},
		{"-f refs/heads/main", "git push -f origin HEAD:refs/heads/main", repo, "block"},
		{"bare -f on main", "git push -f", onMain, "block"},
		{"bare lease on main", "git push --force-with-lease origin", onMain, "block"},
		{"HEAD refspec on main", "git push -f origin HEAD", onMain, "block"},
		{"bash -c -f main", "bash -c 'git push -f origin main'", repo, "block"},
		{"env -f main", "env A=1 git push --force origin main", repo, "block"},
		{"bash -c bare -f on main", "bash -c 'git push -f'", onMain, "block"},
		{"env bare -f on main", "env GIT_TRACE=0 git push -f", onMain, "block"},
		{"cd main && push -f", "cd " + onMain + " && git push -f", repo, "block"},
		{"git -C main push -f", "git -C " + onMain + " push -f", repo, "block"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gateWithCwd(t, tc.cmd, tc.cwd)
			if got.Decision != tc.want {
				t.Errorf("%q in %s: got %s (%s), want %s", tc.cmd, tc.cwd, got.Decision, got.Reason, tc.want)
			}
		})
	}
}
