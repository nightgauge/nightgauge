package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sshShapedOrigin makes origin a real ssh:// remote with GITHUB_TOKEN set,
// the topology #1921 was observed on: a checkout cloned over SSH, where go-git
// rejects the token's *http.BasicAuth as "invalid auth method" before it
// connects. GIT_SSH_COMMAND is a fake ssh that runs git's remote command
// locally, so the git CLI reaches the fixture's bare repository without a
// network. (A url.insteadOf rewrite would not do: go-git honours it too, and
// then never takes the SSH path.)
func sshShapedOrigin(t *testing.T, r liveRunRepo) {
	t.Helper()
	fakeSSH := filepath.Join(t.TempDir(), "fake-ssh")
	// git runs `<cmd> <host> "<git-receive-pack '<path>'>"` for a command it
	// does not recognise as ssh or plink, so $2 is the remote command.
	if err := os.WriteFile(fakeSSH, []byte("#!/bin/sh\nexec sh -c \"$2\"\n"), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("GIT_SSH_COMMAND", fakeSSH)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	gitExecTest(t, r.primary, "remote", "set-url", "origin", "ssh://fixture.invalid"+r.origin)
	t.Setenv("GITHUB_TOKEN", "ghp_fixture_token_not_real")
}

func TestBranchDeleteRemote_DeletesThroughAnSSHRemoteWithATokenSet(t *testing.T) {
	const branch = "feat/123-stale"
	r := setupLiveRunRepo(t)
	sshShapedOrigin(t, r)

	absent, err := r.service(t, r.primary).BranchDeleteRemote(branch)
	if err != nil {
		t.Fatalf("BranchDeleteRemote: %v", err)
	}
	if absent {
		t.Error("alreadyAbsent = true for a branch origin carried until this call")
	}
	if r.remoteRefExists(t, branch) {
		t.Errorf("origin still carries refs/heads/%s", branch)
	}
}

func TestBranchDeleteRemote_ReportsAConfirmedAbsenceAsAlreadyAbsent(t *testing.T) {
	r := setupLiveRunRepo(t)

	// wip/999-operator exists locally but was never pushed.
	absent, err := r.service(t, r.primary).BranchDeleteRemote("wip/999-operator")
	if err != nil {
		t.Fatalf("BranchDeleteRemote: %v", err)
	}
	if !absent {
		t.Error("alreadyAbsent = false for a branch origin never carried")
	}
}

// A delete the remote refuses while the ref is still there is the case the
// old caller logged as "likely already deleted". It must come back as the
// remote's own error, with the ref intact.
func TestBranchDeleteRemote_ReturnsARefusalAsItselfAndKeepsTheRef(t *testing.T) {
	const branch = "feat/123-stale"
	r := setupLiveRunRepo(t)
	hook := filepath.Join(r.origin, "hooks", "pre-receive")
	script := "#!/bin/sh\necho 'protected branch: deletion refused' >&2\nexit 1\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatalf("write pre-receive hook: %v", err)
	}

	absent, err := r.service(t, r.primary).BranchDeleteRemote(branch)
	if err == nil {
		t.Fatal("BranchDeleteRemote returned nil for a delete the remote refused")
	}
	if absent {
		t.Error("alreadyAbsent = true for a branch that is still on origin")
	}
	if !strings.Contains(err.Error(), "deletion refused") {
		t.Errorf("error does not carry the remote's reason: %v", err)
	}
	if !r.remoteRefExists(t, branch) {
		t.Errorf("fixture is not discriminating: origin lost refs/heads/%s anyway", branch)
	}
}

func TestBranchDeleteRemote_RefusesOptionLikeNames(t *testing.T) {
	r := setupLiveRunRepo(t)
	if _, err := r.service(t, r.primary).BranchDeleteRemote("--delete"); err == nil {
		t.Fatal("BranchDeleteRemote accepted an option-like branch name")
	}
}
