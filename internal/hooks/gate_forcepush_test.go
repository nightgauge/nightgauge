package hooks

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// forcePushFixture builds a clone whose current branch is feat/x tracking
// origin/feat/x, a detached clone, and a clone on main.
func forcePushFixture(t *testing.T) (repo, detached, onMain string) {
	t.Helper()
	root := t.TempDir()
	run := func(dir string, args ...string) { gittest.Run(t, dir, args...) }
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
	return gateWithPolicy(t, cmd, cwd, mainProtected)
}

func gateWithPolicy(t *testing.T, cmd, cwd string, policy *config.PushGateConfig) GateDecision {
	t.Helper()
	ti, _ := json.Marshal(BashToolInput{Command: cmd})
	data, _ := json.Marshal(GateInput{ToolName: "Bash", ToolInput: ti, Cwd: cwd})
	return EvaluateGateWithPushGate(data, config.SanitizationModeBlock, policy)
}

// TestBlockForcePushGate covers hooks.push_gate.block_force_push (#2124).
func TestBlockForcePushGate(t *testing.T) {
	t.Setenv(skipWorkflowGateEnv, "")
	repo, detached, onMain := forcePushFixture(t)
	anyBranch := &config.PushGateConfig{BlockForcePush: true}
	both := &config.PushGateConfig{BlockForcePush: true, ProtectedBranches: []string{"main"}}
	cases := []struct {
		name, cmd, cwd string
		policy         *config.PushGateConfig
		want           string
	}{
		// block_force_push alone: every forced push, any branch.
		{"any: -f feature", "git push -f origin feat/x", repo, anyBranch, "block"},
		{"any: lease bare", "git push --force-with-lease", repo, anyBranch, "block"},
		{"any: +refspec", "git push origin +feat/x", repo, anyBranch, "block"},
		{"any: detached", "git push -f origin", detached, anyBranch, "block"},
		{"any: bash -c", "bash -c 'git push -f origin feat/x'", repo, anyBranch, "block"},
		{"any: env", "env A=1 git push --force origin feat/x", repo, anyBranch, "block"},
		{"any: git -C", "git -C " + repo + " push -f", onMain, anyBranch, "block"},
		{"any: plain push main", "git push origin main", repo, anyBranch, "allow"},
		{"any: plain push feature", "git push origin feat/x", repo, anyBranch, "allow"},
		// block_force_push with protected_branches: only protected targets.
		{"both: -f feature", "git push -f origin feat/x", repo, both, "allow"},
		{"both: -f main", "git push -f origin main", repo, both, "block"},
		{"both: bare -f on main", "git push -f", onMain, both, "block"},
		{"both: plain push main", "git push origin main", repo, both, "block"},
		{"both: master unprotected", "git push -f origin master", repo, both, "allow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gateWithPolicy(t, tc.cmd, tc.cwd, tc.policy)
			if got.Decision != tc.want {
				t.Errorf("%q in %s: got %s (%s), want %s", tc.cmd, tc.cwd, got.Decision, got.Reason, tc.want)
			}
		})
	}
}

// TestProtectedBranchesCustomList checks a non-main list generalizes.
func TestProtectedBranchesCustomList(t *testing.T) {
	t.Setenv(skipWorkflowGateEnv, "")
	repo, _, onMain := forcePushFixture(t)
	release := &config.PushGateConfig{ProtectedBranches: []string{"release"}}
	for _, tc := range []struct{ cmd, cwd, want string }{
		{"git push origin release", repo, "block"},
		{"git push origin HEAD:refs/heads/release", repo, "block"},
		{"git push origin main", repo, "allow"},
		{"git push -f", onMain, "allow"},
		{"git push -f origin feat/x", repo, "allow"},
	} {
		if got := gateWithPolicy(t, tc.cmd, tc.cwd, release); got.Decision != tc.want {
			t.Errorf("%q: got %s (%s), want %s", tc.cmd, got.Decision, got.Reason, tc.want)
		}
	}
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

		// Blocked (protected_branches [main, master]): every form that targets them.
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
