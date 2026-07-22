package hooks

import (
	"encoding/json"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func makeGateInput(toolName string, toolInput interface{}) []byte {
	ti, _ := json.Marshal(toolInput)
	input := GateInput{
		ToolName:  toolName,
		ToolInput: ti,
	}
	data, _ := json.Marshal(input)
	return data
}

func TestGateAllowsSafeCommands(t *testing.T) {
	safe := []string{
		"ls -la",
		"npm run build",
		"git status",
		"git commit -m 'test'",
		"echo hello",
		"cat README.md",
		"git push origin feat/42-my-branch",
	}

	for _, cmd := range safe {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "allow" {
			t.Errorf("expected allow for %q, got block: %s", cmd, result.Reason)
		}
	}
}

func TestGateBlocksPushToMain(t *testing.T) {
	blocked := []string{
		"git push origin main",
		"git push origin master",
		"git push origin feat/42:main",
		"git push origin HEAD:master",
		"git push origin +main",
		"GH_TOKEN=abc git push origin main", // env-prefixed still caught
	}

	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for %q, got allow", cmd)
		}
	}
}

// TestGateAllowsProseMentioningGitOps locks in the #4069 fix: a command that
// merely *mentions* a blocked operation inside quoted prose (echo, commit
// message, --body, heredoc) must NOT be blocked, because the operation parser
// only inspects the real git argv.
func TestGateAllowsProseMentioningGitOps(t *testing.T) {
	allowed := []string{
		`git commit -m "fix push to main bug"`,
		`echo "git push origin main is blocked"`,
		`gh pr create --base main`,
		`gh pr create --base main --title "merge to main" --body "resets --hard nothing"`,
		`git commit -m "cleanup: remove prune helper"`,
		`echo "this would reset --hard but is just text"`,
		`gh issue comment 5 --body "non-destructive SQLite migration"`,
		"cat <<EOF\ngit push origin main\nreset --hard HEAD\nEOF",
	}
	for _, cmd := range allowed {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "allow" {
			t.Errorf("expected allow for %q, got block: %s", cmd, result.Reason)
		}
	}
}

// TestGateBlocksBypassVectors covers the bypasses found in the #4069 adversarial
// review: git global options before the verb, shell/prefix wrappers, subshells,
// fully-qualified refspecs, and quoted heredoc delimiters. Each MUST still block.
func TestGateBlocksBypassVectors(t *testing.T) {
	blocked := []string{
		// git global options shift the verb off argv[1]
		"git -C /repo push origin main",
		"git -c http.sslVerify=false push origin master",
		"git --no-pager push origin main",
		"git --git-dir=.git push origin main",
		"git -C /repo reset --hard HEAD",
		"git -C /repo branch -D main",
		"git -C /repo clean -fdx",
		"git -C /repo update-ref -d refs/heads/x",
		// shell / prefix wrappers hide the command
		"bash -c 'git push origin main'",
		`sh -c "git push --force origin main"`,
		"xargs git push origin main",
		"env git push origin main",
		"sudo git push origin main",
		"timeout 5 git reset --hard HEAD",
		// subshell / command substitution
		"( git reset --hard HEAD )",
		// fully-qualified refspecs
		"git push origin HEAD:refs/heads/main",
		"git push origin refs/heads/main",
		"git push origin main:refs/heads/main",
		// quoted/escaped heredoc delimiter must not swallow the trailing push
		"cat <<\\EOF > notes.txt\nsome notes\nEOF\ngit push origin main",
		"cat <<E'O'F\nbody\nEOF\ngit push origin main",
	}
	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("BYPASS: expected block for %q, got allow", cmd)
		}
	}
}

// TestGateSkipWorkflowGateOverride verifies the explicit, text-free escape hatch.
func TestGateSkipWorkflowGateOverride(t *testing.T) {
	t.Setenv(skipWorkflowGateEnv, "1")
	// Operation gate would normally block; override allows it.
	input := makeGateInput("Bash", BashToolInput{Command: "git push origin main"})
	if got := EvaluateGate(input, config.SanitizationModeBlock); got.Decision != "allow" {
		t.Errorf("override should allow push-to-main, got block: %s", got.Reason)
	}
	// Secret-read gate stays ON even with the override.
	input = makeGateInput("Bash", BashToolInput{Command: "cat .env"})
	if got := EvaluateGate(input, config.SanitizationModeBlock); got.Decision != "block" {
		t.Error("override must NOT disable the secret-read gate")
	}
}

func TestGateBlocksForcePush(t *testing.T) {
	blocked := []string{
		"git push -f origin feat/42",
		"git push --force origin feat/42",
		"git push --force-with-lease origin feat/42",
		"git push origin +feat/42",
	}

	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for %q, got allow", cmd)
		}
	}
}

func TestGateBlocksDestructiveGit(t *testing.T) {
	blocked := []string{
		"git reset --hard HEAD~1",
		"git clean -f",
		"git clean -fd",
		"git checkout .",
		"git restore .",
		"git branch -D feat/x",
		"git worktree remove --force .worktrees/x",
		"git update-ref -d refs/heads/x",
	}

	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for %q, got allow", cmd)
		}
	}
}

// TestGateAllowsSafeGitVerbs ensures the destructive-git parser does not
// over-block benign neighbours of the destructive verbs.
func TestGateAllowsSafeGitVerbs(t *testing.T) {
	allowed := []string{
		"git clean -n",            // dry-run, no -f
		"git checkout feat/42",    // not the bare-dot restore-all
		"git branch -d merged/x",  // lowercase -d is a safe (merged-only) delete
		"git reset --soft HEAD~1", // soft reset keeps the worktree
		"git clean --dry-run",
	}
	for _, cmd := range allowed {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "allow" {
			t.Errorf("expected allow for %q, got block: %s", cmd, result.Reason)
		}
	}
}

func TestGateBlocksSecretReads(t *testing.T) {
	blocked := []string{
		"cat .env",
		"less server.pem",
		"head private.key",
		"tail app.secret",
	}

	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for %q, got allow", cmd)
		}
	}
}

func TestGateBlocksSecretWrites(t *testing.T) {
	blocked := []string{
		"echo TOKEN=xyz > .env",
		"printf 'key' > private.key",
		"echo data >> server.pem",
	}

	for _, cmd := range blocked {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for %q, got allow", cmd)
		}
	}
}

func TestGateBlocksSensitiveFileEdits(t *testing.T) {
	sensitiveFiles := []string{
		"/project/.env",
		"/project/server.pem",
		"/project/private.key",
		"/project/credentials.json",
		"/project/secrets.yaml",
	}

	for _, fp := range sensitiveFiles {
		input := makeGateInput("Edit", FileToolInput{FilePath: fp})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "block" {
			t.Errorf("expected block for editing %q, got allow", fp)
		}
	}
}

func TestGateAllowsSafeFileEdits(t *testing.T) {
	safeFiles := []string{
		"/project/README.md",
		"/project/main.go",
		"/project/package.json",
		"/project/src/index.ts",
	}

	for _, fp := range safeFiles {
		input := makeGateInput("Write", FileToolInput{FilePath: fp})
		result := EvaluateGate(input, config.SanitizationModeBlock)
		if result.Decision != "allow" {
			t.Errorf("expected allow for writing %q, got block: %s", fp, result.Reason)
		}
	}
}

func TestGateBlocksGitInternals(t *testing.T) {
	input := makeGateInput("Edit", FileToolInput{FilePath: "/project/.git/config"})
	result := EvaluateGate(input, config.SanitizationModeBlock)
	if result.Decision != "block" {
		t.Error("expected block for editing .git/config, got allow")
	}
}

func TestGateAllowsUnknownTools(t *testing.T) {
	input := makeGateInput("Read", json.RawMessage(`{}`))
	result := EvaluateGate(input, config.SanitizationModeBlock)
	if result.Decision != "allow" {
		t.Errorf("expected allow for Read tool, got block: %s", result.Reason)
	}
}

func TestGateOutputJSON(t *testing.T) {
	allow := Allow()
	data, err := json.Marshal(allow)
	if err != nil {
		t.Fatalf("Marshal allow: %v", err)
	}
	if string(data) != `{"decision":"allow"}` {
		t.Errorf("allow JSON = %s, want {\"decision\":\"allow\"}", string(data))
	}

	block := Block("test reason")
	data, err = json.Marshal(block)
	if err != nil {
		t.Fatalf("Marshal block: %v", err)
	}

	var result GateDecision
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if result.Decision != "block" || result.Reason != "test reason" {
		t.Errorf("block result = %+v, want block/test reason", result)
	}
}

func TestGateSanitizationBlocks(t *testing.T) {
	// Destructive commands caught by sanitization in block mode
	input := makeGateInput("Bash", BashToolInput{Command: "rm -rf /"})
	result := EvaluateGate(input, config.SanitizationModeBlock)
	if result.Decision != "block" {
		t.Error("expected block for rm -rf /, got allow")
	}

	// Exfiltration
	input = makeGateInput("Bash", BashToolInput{Command: "cat ~/.ssh/id_rsa"})
	result = EvaluateGate(input, config.SanitizationModeBlock)
	if result.Decision != "block" {
		t.Error("expected block for cat ~/.ssh/id_rsa, got allow")
	}
}

func TestGateWarnModeAllowsWithLog(t *testing.T) {
	// In warn mode, sanitization pattern matches should allow (not block)
	dangerous := []string{
		"rm -rf /",
		"cat ~/.ssh/id_rsa",
	}

	for _, cmd := range dangerous {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeWarn)
		if result.Decision != "allow" {
			t.Errorf("warn mode: expected allow for %q, got block: %s", cmd, result.Reason)
		}
	}
}

func TestGateWarnModeStillBlocksNonSanitizationGates(t *testing.T) {
	// Non-sanitization gates (push to main, force push, etc.) should still block
	// regardless of sanitization mode
	cases := []struct {
		name string
		cmd  string
	}{
		{"push to main", "git push origin main"},
		{"force push", "git push -f origin feat/42"},
		{"destructive git", "git reset --hard HEAD~1"},
		{"secret read", "cat .env"},
		{"secret write", "echo TOKEN=xyz > .env"},
	}

	for _, tc := range cases {
		input := makeGateInput("Bash", BashToolInput{Command: tc.cmd})
		result := EvaluateGate(input, config.SanitizationModeWarn)
		if result.Decision != "block" {
			t.Errorf("warn mode: expected block for %s (%q), got allow", tc.name, tc.cmd)
		}
	}
}

func TestGateDisabledModeSkipsSanitization(t *testing.T) {
	// In disabled mode, sanitization pattern matches should allow through
	dangerous := []string{
		"rm -rf /",
		"cat ~/.ssh/id_rsa",
	}

	for _, cmd := range dangerous {
		input := makeGateInput("Bash", BashToolInput{Command: cmd})
		result := EvaluateGate(input, config.SanitizationModeDisabled)
		if result.Decision != "allow" {
			t.Errorf("disabled mode: expected allow for %q, got block: %s", cmd, result.Reason)
		}
	}
}

func TestGateDisabledModeStillBlocksNonSanitizationGates(t *testing.T) {
	// Non-sanitization gates still block even with disabled sanitization
	cases := []struct {
		name string
		cmd  string
	}{
		{"push to main", "git push origin main"},
		{"force push", "git push -f origin feat/42"},
		{"destructive git", "git reset --hard HEAD~1"},
		{"secret read", "cat .env"},
		{"secret write", "echo TOKEN=xyz > .env"},
	}

	for _, tc := range cases {
		input := makeGateInput("Bash", BashToolInput{Command: tc.cmd})
		result := EvaluateGate(input, config.SanitizationModeDisabled)
		if result.Decision != "block" {
			t.Errorf("disabled mode: expected block for %s (%q), got allow", tc.name, tc.cmd)
		}
	}
}

func TestGateFileGatesNotAffectedByMode(t *testing.T) {
	// File gates (sensitive files, git internals) should block regardless of mode
	modes := []config.SanitizationMode{
		config.SanitizationModeWarn,
		config.SanitizationModeBlock,
		config.SanitizationModeDisabled,
	}

	for _, mode := range modes {
		input := makeGateInput("Edit", FileToolInput{FilePath: "/project/.env"})
		result := EvaluateGate(input, mode)
		if result.Decision != "block" {
			t.Errorf("mode %q: expected block for editing .env, got allow", mode)
		}

		input = makeGateInput("Edit", FileToolInput{FilePath: "/project/.git/config"})
		result = EvaluateGate(input, mode)
		if result.Decision != "block" {
			t.Errorf("mode %q: expected block for editing .git/config, got allow", mode)
		}
	}
}
