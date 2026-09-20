//go:build canary

package execution

// The #1644 egress fixture. scripts/opencode-egress-check.sh runs
//
//	go test -tags canary ./internal/execution -run TestOpenCodeEgressCheck -count=1 -v
//
// inside a Linux network namespace whose only interface is loopback
// (unshare --user --map-root-user --net), with the whole process tree under
// strace. This file is the Go-level half of that evidence: it dispatches a
// real feature-dev stage through the OpenCode adapter (Manager.RunStage,
// exactly the production path — see PLAN.md's "Premise Correction": the TS
// SDK path refuses OpenCode dispatch pending #1648) against the #1618 stub
// provider, so a broken stub/dispatch fails here as a clear Go failure
// before the strace wrapper ever runs, rather than as an opaque trace-parser
// failure with no attributable cause.
//
// The fixture extends openCodeGitWorktree with a local bare remote (so any
// git push/fetch OpenCode's tools attempt stays on local disk and never
// reaches a forge) and a pre-seeded context file at
// .nightgauge/pipeline/issue-9999.json inside the worktree (docs/CONTEXT_ARCHITECTURE.md's
// issue-{N}.json schema), so the stage needs no `gh` call and no GitHub
// credentials to have a context to read.
//
// AC3 (fake cloud keys produce no cloud-provider connection attempt) is
// covered by construction, not by a separate assertion here: ADR-022 § 8
// strips every inherited OPENCODE_* variable and every catalog-bound
// credential variable (OPENAI_API_KEY, ANTHROPIC_API_KEY, XAI_API_KEY among
// them) before spawn, so a fake key in the parent env never reaches the
// child at all — the CI harness (scripts/opencode-egress-check.sh) exports
// them into the process env the same way the technical notes describe, and
// the trace itself is the evidence there was nothing for them to reach.
//
// AC4 (the Nightgauge plugin loaded from the per-run dir, no npm/Bun
// registry lookup) is likewise covered by construction: Manager.RunStage's
// own handshake verification (opencodeplugin.VerifyLoaded) already fails the
// run closed if the plugin did not load, and the pre-seeded dependency
// archive (ADR-022's "Narrowed AC1" amendment) is what stops the npm install
// this test would otherwise wait on — nothing new is added here to prove
// either half a second time.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// openCodeEgressContextFixture is a minimal, schema-valid issue-9999.json
// (docs/CONTEXT_ARCHITECTURE.md's issue-{N}.json, schema_version 1.4) — the
// stage needs no `gh` call to have a context to read, and the stub's canned
// script never inspects its content, so this is deliberately the smallest
// fixture the schema allows.
const openCodeEgressContextFixture = `{
  "schema_version": "1.4",
  "issue_number": 9999,
  "title": "egress fixture",
  "type": "feature",
  "branch": "feat/9999-egress-fixture",
  "base_branch": "main",
  "requirements": {
    "summary": "egress fixture: not a real issue"
  },
  "labels": []
}`

// openCodeEgressWorktree builds the fixture the #1644 egress check dispatches
// against: an issue-9999 worktree cloned from a local bare remote (so any
// git push/fetch the stage's tools attempt stays on local disk), with the
// stub script's edit target (calc.py) committed and a pre-seeded context
// file written into the worktree's own .nightgauge/pipeline/ directory. It
// returns the workspace root RunStage needs and the worktree's own path (for
// assertions, if any are ever added).
func openCodeEgressWorktree(t *testing.T) (workspace, worktree string) {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	calc := filepath.Join(seed, "calc.py")
	if err := os.WriteFile(calc, []byte("def add(a, b):\n    return a + b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")

	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)

	workspace = t.TempDir()
	parent := filepath.Join(workspace, ".nightgauge", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, parent, "clone", "-q", origin, "nightgauge-issue-9999")
	worktree = filepath.Join(parent, "nightgauge-issue-9999")

	pipelineDir := filepath.Join(worktree, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contextPath := filepath.Join(pipelineDir, "issue-9999.json")
	if err := os.WriteFile(contextPath, []byte(openCodeEgressContextFixture), 0o644); err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal([]byte(openCodeEgressContextFixture), &probe); err != nil {
		t.Fatalf("the pre-seeded context fixture is not valid JSON: %v", err)
	}
	return workspace, worktree
}

// TestOpenCodeEgressCheck is the #1644 egress fixture's Go-level leg (AC1,
// AC3, AC4, AC5): a real feature-dev dispatch through Manager.RunStage
// against the #1618 stub provider, run inside the CI harness's network
// namespace under strace. Assertions here are the same clean-run assertions
// TestOpenCodeCanaryLiveStream already makes (err == nil, exit 0, a non-zero
// token count) — this fixture's own contribution is the local bare remote
// and the pre-seeded context file, not a new assertion shape.
func TestOpenCodeEgressCheck(t *testing.T) {
	realOpenCode(t)
	isolateOpenCodeHome(t)
	t.Setenv(adapters.ExperimentalOpenCodeEnvVar, "1")

	base, _ := startOpenCodeCanaryStubWithPID(t, "tool-edit-stop")
	writeOpenCodeMachineConfig(t, strings.Replace(openCodeMachineConfig, "http://127.0.0.1:1234/v1", base, 1))

	workspace, worktree := openCodeEgressWorktree(t)
	contextFile := filepath.Join(worktree, ".nightgauge", "pipeline", "issue-9999.json")

	opts := openCodeStageOptions(openCodeCanaryModel, nil)
	opts.IssueNumber = 9999
	opts.ContextFile = contextFile
	opts.Timeout = 120 * time.Second

	var result *adapters.RunResult
	var err error
	stderr := captureStderr(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		result, err = NewManager(workspace, adapters.NewOpenCodeAdapter()).RunStage(ctx, opts)
	})
	if err != nil {
		t.Fatalf("RunStage: %v\n%s", err, stderr)
	}
	if result.ExitCode != 0 {
		t.Fatalf("the stage exited %d, want 0:\n%s", result.ExitCode, stderr)
	}
	if len(result.DriftMarkers) > 0 {
		t.Errorf("RunResult carries drift markers from a clean run: %v", result.DriftMarkers)
	}
	problems, acc := checkOpenCodeCanaryStream(result.Stdout)
	for _, p := range problems {
		t.Errorf("%s", p)
	}
	if acc.Total() == 0 {
		t.Errorf("the egress fixture's own stream parsed to zero tokens:\n%s", result.Stdout)
	}

	// AC5's Go-level half is startOpenCodeCanaryStubWithPID's own t.Cleanup
	// (registered above, run unconditionally on this test's return): it
	// signals the stub PID and asserts kill(pid, 0) fails afterward, matching
	// TestOpenCodeCanaryStubIsKilledAndConfirmedDead's pattern. The shell
	// harness re-verifies at the process-tree level for its own spawns (the
	// stub it starts directly, and the `go test` process itself) — nothing
	// further is asserted here.
}
