package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// writeBaseline writes a minimal auto-fix-baseline-{PR}.json into the given
// workspace. The shape mirrors what Step 2.5 of the auto-fix loop produces.
func writeBaseline(t *testing.T, workspace string, prNumber int, body string) {
	t.Helper()
	dir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "auto-fix-baseline-"+itoa(prNumber)+".json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func itoa(n int) string {
	// Avoid pulling strconv into the call sites just for this helper.
	return strings.TrimSpace(string(jsonNumber(n)))
}

func jsonNumber(n int) []byte {
	b, _ := json.Marshal(n)
	return b
}

// grantHealBaseApproval writes the out-of-band approval file (#4136) so a heal
// PR may proceed past the human-approval gate in tests that exercise creation.
func grantHealBaseApproval(t *testing.T, workspace string, prNumber int) {
	t.Helper()
	dir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "approval-heal-base-"+itoa(prNumber)+".json")
	if err := os.WriteFile(path, []byte(`{"approved": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func baseFailure(workspace string) StageFailure {
	return StageFailure{
		Stage:          state.StagePRMerge,
		GateKind:       gates.KindNoOp,
		PRNumber:       42,
		IssueNumber:    1234,
		Repo:           "nightgauge/nightgauge",
		Workspace:      workspace,
		AttemptOrdinal: 1,
		Reason:         "pipeline-failed-inherited",
		Evidence:       []string{"inherited-only"},
	}
}

func TestPipelineHealBase_Matches_Positive(t *testing.T) {
	a := NewPipelineHealBase("")
	failure := baseFailure(t.TempDir())
	if !a.Matches(failure) {
		t.Fatalf("expected match")
	}
}

func TestPipelineHealBase_Matches_NegativeCases(t *testing.T) {
	a := NewPipelineHealBase("")
	cases := []struct {
		name string
		mut  func(*StageFailure)
	}{
		{"wrong stage", func(f *StageFailure) { f.Stage = state.StagePRCreate }},
		{"not no-op", func(f *StageFailure) { f.GateKind = gates.KindFail }},
		{"no PR", func(f *StageFailure) { f.PRNumber = 0 }},
		{"no issue", func(f *StageFailure) { f.IssueNumber = 0 }},
		{"already attempted", func(f *StageFailure) { f.AttemptOrdinal = 2 }},
		{"no inherited marker", func(f *StageFailure) {
			f.Reason = "PR is not MERGED (state=OPEN)"
			f.Evidence = nil
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := baseFailure(t.TempDir())
			c.mut(&f)
			if a.Matches(f) {
				t.Fatalf("expected no match")
			}
		})
	}
}

func TestPipelineHealBase_Execute_NoBaselineFile(t *testing.T) {
	a := NewPipelineHealBase("")
	res := a.Execute(context.Background(), baseFailure(t.TempDir()))
	if res.FollowUp != FollowUpNoAction {
		t.Fatalf("expected FollowUpNoAction; got %s reason=%s", res.FollowUp, res.Reason)
	}
	if res.Recovered {
		t.Errorf("expected Recovered=false")
	}
}

func TestPipelineHealBase_Execute_MixedBatchRefuses(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"a","classification":"inherited","details":""},
		{"name":"b","classification":"regression","details":""}
	]}`)
	a := NewPipelineHealBase("")
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpNoAction {
		t.Fatalf("expected FollowUpNoAction; got %s", res.FollowUp)
	}
	if !strings.Contains(res.Reason, "regression") {
		t.Errorf("reason should mention regression; got %q", res.Reason)
	}
}

func TestPipelineHealBase_Execute_ThrottleActiveLimit(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file test/fixtures/users.json"}
	]}`)

	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "--state open"):
			// Return >= MaxActivePerRepo (default 1) entries.
			return []byte(`[{"url":"https://github.com/x/y/pull/1","createdAt":"2026-05-19T00:00:00Z"}]`), nil
		case strings.Contains(joined, "pr list"):
			return []byte(`[]`), nil
		}
		return []byte(``), nil
	})

	a := NewPipelineHealBase("")
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s reason=%q", res.FollowUp, res.Reason)
	}
	if !strings.Contains(res.Reason, "active") {
		t.Errorf("reason should mention active throttle; got %q", res.Reason)
	}
}

func TestPipelineHealBase_Execute_Throttle24hLimit(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file test/fixtures/users.json"}
	]}`)

	// Default config: Max24hPerRepo=3. Return 3 recent entries.
	nowISO := time.Now().UTC().Format(time.RFC3339)
	calls := 0
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "pr list") {
			calls++
			if calls == 1 {
				// First call is the active (open) check — return empty.
				return []byte(`[]`), nil
			}
			return []byte(`[
				{"url":"https://github.com/x/y/pull/1","createdAt":"` + nowISO + `"},
				{"url":"https://github.com/x/y/pull/2","createdAt":"` + nowISO + `"},
				{"url":"https://github.com/x/y/pull/3","createdAt":"` + nowISO + `"}
			]`), nil
		}
		return []byte(``), nil
	})

	a := NewPipelineHealBase("")
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s reason=%q", res.FollowUp, res.Reason)
	}
	if !strings.Contains(res.Reason, "24h") {
		t.Errorf("reason should mention 24h throttle; got %q", res.Reason)
	}
}

func TestPipelineHealBase_Execute_NoPatternMatch(t *testing.T) {
	workspace := t.TempDir()
	// Cluster matches no built-in pattern — just an unrelated assertion.
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"some.test","classification":"inherited","details":"AssertionError: expected 1 to equal 2"}
	]}`)

	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "pr list") {
			return []byte(`[]`), nil
		}
		return []byte(``), nil
	})

	a := NewPipelineHealBase("")
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s reason=%q", res.FollowUp, res.Reason)
	}
	if !strings.Contains(res.Reason, "no heal pattern matched") {
		t.Errorf("reason should explain no pattern match; got %q", res.Reason)
	}
}

func TestPipelineHealBase_Execute_DeterministicFix_CreatesPR(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file or directory test/fixtures/users.json"}
	]}`)
	grantHealBaseApproval(t, workspace, 42) // #4136 human-approval gate

	// Workspace root has an empty config — defaults apply, including
	// RequireHumanFirst=true. We simulate "this pattern has merged before"
	// by returning one closed PR with the pattern label.
	var ghCalls []string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		ghCalls = append(ghCalls, joined)
		switch {
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "pipeline-heal:auto") && strings.Contains(joined, "--state open"):
			return []byte(`[]`), nil
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "pipeline-heal:auto") && strings.Contains(joined, "--state all") && strings.Contains(joined, "pattern:missing-fixture"):
			// Past merges with the pattern label — first-occurrence gate
			// does NOT downgrade.
			return []byte(`[{"url":"https://github.com/x/y/pull/9","createdAt":"2026-04-01T00:00:00Z"}]`), nil
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "--state all"):
			// recent-24h throttle check — empty.
			return []byte(`[]`), nil
		case strings.Contains(joined, "pr create"):
			return []byte("https://github.com/nightgauge/nightgauge/pull/999\n"), nil
		case strings.Contains(joined, "pr comment"):
			return []byte(""), nil
		}
		return []byte(``), nil
	})

	// Git operations: succeed with empty output.
	var gitCalls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, strings.Join(args, " "))
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.Recovered {
		t.Errorf("expected Recovered=false even on success — main is not fixed yet")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("expected FollowUpHumanTriageRequired; got %s", res.FollowUp)
	}
	if !strings.Contains(res.Reason, "missing-fixture") {
		t.Errorf("reason should mention pattern slug; got %q", res.Reason)
	}
	// Verify gh pr create was called with --label including pipeline-heal:auto.
	foundCreate := false
	for _, c := range ghCalls {
		if strings.Contains(c, "pr create") {
			foundCreate = true
			if !strings.Contains(c, "pipeline-heal:auto") {
				t.Errorf("expected pipeline-heal:auto label on subsequent occurrence; got: %s", c)
			}
		}
	}
	if !foundCreate {
		t.Errorf("expected gh pr create to be invoked; calls=%v", ghCalls)
	}
	// Verify the fix tree change was committed and pushed.
	foundPush := false
	for _, g := range gitCalls {
		if strings.HasPrefix(g, "push origin pipeline-heal/") {
			foundPush = true
		}
	}
	if !foundPush {
		t.Errorf("expected git push to be invoked; calls=%v", gitCalls)
	}
	// Verify the fixture file was actually written.
	if _, err := os.Stat(filepath.Join(workspace, "test", "fixtures", "users.json")); err != nil {
		t.Errorf("expected fixture file to be written: %v", err)
	}
}

func TestPipelineHealBase_Execute_FirstOccurrenceDowngrades(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file or directory test/fixtures/users.json"}
	]}`)
	grantHealBaseApproval(t, workspace, 42) // #4136 human-approval gate

	var lastCreate string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "pattern:missing-fixture"):
			// No past merges → first occurrence.
			return []byte(`[]`), nil
		case strings.Contains(joined, "pr list"):
			return []byte(`[]`), nil
		case strings.Contains(joined, "pr create"):
			lastCreate = joined
			return []byte("https://github.com/x/y/pull/999\n"), nil
		}
		return []byte(``), nil
	})
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s", res.FollowUp)
	}
	if !strings.Contains(lastCreate, "pipeline-heal:needs-review") {
		t.Errorf("expected needs-review label on first occurrence; create=%s", lastCreate)
	}
	if strings.Contains(lastCreate, "pipeline-heal:auto,") || strings.HasSuffix(lastCreate, "pipeline-heal:auto") {
		t.Errorf("expected pipeline-heal:auto to be swapped; create=%s", lastCreate)
	}
}

func TestPipelineHealBase_Execute_CrossRepoFix(t *testing.T) {
	workspace := t.TempDir()
	// Failure carries a TargetRepo hint, so the heal PR must be created with
	// --repo <target>.
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file test/fixtures/users.json","target_repo":"nightgauge/other-repo"}
	]}`)
	grantHealBaseApproval(t, workspace, 42) // #4136 human-approval gate

	var lastCreate string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "pr list") && strings.Contains(joined, "pattern:missing-fixture"):
			return []byte(`[{"url":"https://x.com/p/9","createdAt":"2026-04-01T00:00:00Z"}]`), nil
		case strings.Contains(joined, "pr list"):
			return []byte(`[]`), nil
		case strings.Contains(joined, "pr create"):
			lastCreate = joined
			return []byte("https://github.com/nightgauge/other-repo/pull/777\n"), nil
		}
		return []byte(``), nil
	})
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s reason=%q", res.FollowUp, res.Reason)
	}
	if !strings.Contains(lastCreate, "--repo nightgauge/other-repo") {
		t.Errorf("expected cross-repo --repo flag; create=%s", lastCreate)
	}
	foundEvidence := false
	for _, e := range res.Evidence {
		if e == "target_repo=nightgauge/other-repo" {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Errorf("expected target_repo evidence; got %v", res.Evidence)
	}
}

func TestPipelineHealBase_Execute_GhCreateFailsBubblesUp(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file test/fixtures/users.json"}
	]}`)
	grantHealBaseApproval(t, workspace, 42) // #4136 human-approval gate

	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "pr create") {
			return nil, errors.New("permission denied: target repo not writable")
		}
		if strings.Contains(joined, "pr list") {
			return []byte(`[]`), nil
		}
		return []byte(``), nil
	})
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s", res.FollowUp)
	}
	if !strings.Contains(res.Reason, "pr create failed") {
		t.Errorf("reason should mention pr create failure; got %q", res.Reason)
	}
}

// #4136 — without an out-of-band approval, the heal action must NOT create a PR
// or push any branch; it returns human-triage with an approval-required reason.
func TestPipelineHealBase_Execute_RequiresApproval(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file or directory test/fixtures/users.json"}
	]}`)
	// NOTE: no grantHealBaseApproval — approval is absent.

	var ghCalls []string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		ghCalls = append(ghCalls, joined)
		// All pr list (throttle) + pr view (label check) calls return empty.
		return []byte(`[]`), nil
	})
	var gitCalls []string
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		gitCalls = append(gitCalls, strings.Join(args, " "))
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))

	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Fatalf("expected human triage; got %s reason=%q", res.FollowUp, res.Reason)
	}
	if !strings.Contains(res.Reason, "requires human approval") {
		t.Errorf("reason should explain approval is required; got %q", res.Reason)
	}
	for _, c := range ghCalls {
		if strings.Contains(c, "pr create") {
			t.Errorf("no PR must be created without approval; gh calls=%v", ghCalls)
		}
	}
	for _, g := range gitCalls {
		if strings.HasPrefix(g, "push ") {
			t.Errorf("no branch must be pushed without approval; git calls=%v", gitCalls)
		}
	}
}

// #4136 — the approval LABEL on the failing PR is a valid (durable) approval
// surface: it lets the heal PR proceed even with no local approval file.
func TestPipelineHealBase_Execute_ApprovalViaLabel(t *testing.T) {
	workspace := t.TempDir()
	writeBaseline(t, workspace, 42, `{"failures":[
		{"name":"users.test","classification":"inherited","details":"ENOENT: no such file or directory test/fixtures/users.json"}
	]}`)
	// No approval file — approval comes from the label on PR #42.

	var createdPR bool
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "pr view") && strings.Contains(joined, "labels"):
			return []byte(`{"labels":[{"name":"pipeline-heal:approved"}]}`), nil
		case strings.Contains(joined, "pr create"):
			createdPR = true
			return []byte("https://github.com/nightgauge/nightgauge/pull/999\n"), nil
		case strings.Contains(joined, "pr list"):
			return []byte(`[]`), nil
		}
		return []byte(``), nil
	})
	stubExecGit(t, func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return []byte(""), nil
	})

	a := NewPipelineHealBase(workspace)
	res := a.Execute(context.Background(), baseFailure(workspace))

	if !createdPR {
		t.Errorf("approval label should let the heal PR proceed; reason=%q", res.Reason)
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("expected human triage after PR creation; got %s", res.FollowUp)
	}
}
