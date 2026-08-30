package hooks

// Node-project pre-push validation (#1159).
//
// `npm run build` exits 1 with `Missing script: "build"` when the script is not
// defined. The gate used to read that exit code as a build failure and block
// every build-less Node project with `Reason: npm build failed`, although
// nothing failed to build — while feature-validate, which does detect the
// absence, passed the same tree. These cover the three distinguishable states:
// the script is absent, the script exists and fails, and package.json cannot be
// read at all.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setupNodeBranchMock is setupCleanBranchMock with the npm phases mapped as
// well. `npm run build` / `npm test` are given the exit-1 Missing-script answer
// npm really gives, so a test that expects them NOT to be run fails loudly if
// the gate runs them anyway.
func setupNodeBranchMock() *mockCmdRunner {
	m := setupCleanBranchMock()
	m.set("npm run build", "npm error Missing script: \"build\"\n", fmt.Errorf("exit status 1"))
	m.set("npm test", "npm error Missing script: \"test\"\n", fmt.Errorf("exit status 1"))
	return m
}

func writeNodeFixture(t *testing.T, pkgJSON string) (string, *mockCmdRunner) {
	t.Helper()
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkgJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".nightgauge", "pipeline"), 0755); err != nil {
		t.Fatal(err)
	}
	return tmpDir, setupNodeBranchMock()
}

func nodeInput(tmpDir string) PrePushInput {
	return PrePushInput{
		IssueNumber:   42,
		WorkDir:       tmpDir,
		TargetBranch:  "main",
		FeatureBranch: "feat/42-test-feature",
	}
}

func ranCommand(m *mockCmdRunner, key string) bool {
	for _, c := range m.calls {
		if c.Name+" "+strings.Join(c.Args, " ") == key {
			return true
		}
	}
	return false
}

func TestEvaluatePrePush_NodeWithoutBuildScriptIsNotApplicable(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name":"x","scripts":{"test":"vitest run"}}`)
	runner.set("npm test", "", nil)

	result := EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	if result.Decision != "allow" {
		t.Errorf("expected decision=allow, got %q (reason: %s)", result.Decision, result.Reason)
	}
	if got := result.ValidationPhases["build"]; got != "not-applicable" {
		t.Errorf("expected build=not-applicable, got %q", got)
	}
	if ranCommand(runner, "npm run build") {
		t.Error("gate ran `npm run build` although package.json defines no build script")
	}
	if got := result.ValidationPhases["test"]; got != "passed" {
		t.Errorf("expected the gate to proceed to npm test, got test=%q", got)
	}
	if !ranCommand(runner, "npm test") {
		t.Error("gate never ran `npm test` after the absent build script")
	}
}

func TestEvaluatePrePush_NodeWithoutTestScriptIsNotApplicable(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name":"x"}`)

	result := EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	if result.Decision != "allow" {
		t.Errorf("expected decision=allow, got %q (reason: %s)", result.Decision, result.Reason)
	}
	for _, phase := range []string{"build", "test", "vet"} {
		if got := result.ValidationPhases[phase]; got != "not-applicable" {
			t.Errorf("expected %s=not-applicable, got %q", phase, got)
		}
	}
	if ranCommand(runner, "npm test") {
		t.Error("gate ran `npm test` although package.json defines no test script")
	}
}

// The other half of the pair: a build script that EXISTS and fails must still
// block. Without this, "stop blocking on a missing build script" could be
// satisfied by never blocking on a build at all.
func TestEvaluatePrePush_NodeWithFailingBuildScriptStillBlocks(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name":"x","scripts":{"build":"tsc -b","test":"vitest run"}}`)
	runner.set("npm run build", "TS2304: Cannot find name 'foo'.\n", fmt.Errorf("exit status 2"))

	result := EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	if result.Decision != "block" {
		t.Fatalf("expected decision=block, got %q", result.Decision)
	}
	if got := result.ValidationPhases["build"]; got != "failed" {
		t.Errorf("expected build=failed, got %q", got)
	}
	if !strings.Contains(result.Reason, "npm build failed") {
		t.Errorf("expected reason to name the build failure, got %q", result.Reason)
	}
}

func TestEvaluatePrePush_NodeWithFailingTestScriptStillBlocks(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name":"x","scripts":{"test":"vitest run"}}`)
	runner.set("npm test", "1 failed\n", fmt.Errorf("exit status 1"))

	result := EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	if result.Decision != "block" {
		t.Fatalf("expected decision=block, got %q", result.Decision)
	}
	if got := result.ValidationPhases["test"]; got != "failed" {
		t.Errorf("expected test=failed, got %q", got)
	}
}

// A package.json the gate cannot parse is neither "no build script" nor "the
// build failed" — it is the gate being unable to determine what applies, which
// fails closed under its own distinct phase value rather than guessing in
// either direction.
func TestEvaluatePrePush_NodeWithUnparseablePackageJSONBlocksAsUnknown(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name": "x", "scripts": {`)

	result := EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	if result.Decision != "block" {
		t.Fatalf("expected decision=block, got %q", result.Decision)
	}
	for _, phase := range []string{"build", "test"} {
		if got := result.ValidationPhases[phase]; got != "unknown" {
			t.Errorf("expected %s=unknown, got %q", phase, got)
		}
	}
	if strings.Contains(result.Reason, "npm build failed") {
		t.Errorf("an unreadable package.json must not be reported as a build failure, got %q", result.Reason)
	}
	if !strings.Contains(result.Reason, "undetermined") {
		t.Errorf("expected the reason to say coverage is undetermined, got %q", result.Reason)
	}
	if ranCommand(runner, "npm run build") {
		t.Error("gate ran `npm run build` with an unparseable package.json")
	}
}

// The not-applicable phase must survive serialisation: a missing build step has
// to be visible in pre-push-<N>.json, not silently green.
func TestPrePushResult_NotApplicablePhaseIsRecordedOnDisk(t *testing.T) {
	tmpDir, runner := writeNodeFixture(t, `{"name":"x","scripts":{"test":"vitest run"}}`)
	runner.set("npm test", "", nil)

	// EvaluatePrePush writes pre-push-<N>.json itself.
	_ = EvaluatePrePush(context.Background(), runner, nodeInput(tmpDir))

	raw, err := os.ReadFile(filepath.Join(tmpDir, ".nightgauge", "pipeline", "pre-push-42.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk struct {
		ValidationPhases map[string]string `json:"validation_phases"`
	}
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if got := onDisk.ValidationPhases["build"]; got != "not-applicable" {
		t.Errorf("expected build=not-applicable in pre-push-42.json, got %q", got)
	}
}
