package gates

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// stubExecGitForGate swaps the package-level execGitForGate for the duration
// of the test. Mirrors stubExecGh's pattern.
func stubExecGitForGate(t *testing.T, fn func(ctx context.Context, dir string, args ...string) ([]byte, error)) {
	t.Helper()
	prev := execGitForGate
	execGitForGate = fn
	t.Cleanup(func() { execGitForGate = prev })
}

func TestPrMergeGate_Pass_StateMerged(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"MERGED","number":100}`), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when PR is MERGED; reason=%q", gr.Reason)
	}
}

// TestPrMergeGate_SkillSaidSuccessButPrIsOpen is the headline scenario:
// the pr-merge skill exited 0 but the PR is still OPEN (a #1819-class bug).
// The reason must include "state=OPEN" so the TS shim can substring-match.
func TestPrMergeGate_SkillSaidSuccessButPrIsOpen(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN","number":100}`), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when PR is OPEN")
	}
	combined := gr.Reason + " " + strings.Join(gr.Evidence, " ")
	if !strings.Contains(combined, "state=OPEN") {
		t.Errorf("evidence/reason must contain state=OPEN; got reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
}

// TestPrMergeGate_BehindEmitsMergeStateEvidence locks the #4071 wiring contract:
// a non-merged BEHIND PR must surface mergeStateStatus into the gate evidence so
// the branch-out-of-date recovery action can match and rebase it (instead of the
// generic skill-no-op recovery that would just re-punt). Without this token the
// rebase-before-merge machinery is unreachable in production.
func TestPrMergeGate_BehindEmitsMergeStateEvidence(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN","number":100,"mergeStateStatus":"BEHIND","mergeable":"MERGEABLE","reviewDecision":"APPROVED"}`), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when PR is BEHIND/OPEN")
	}
	combined := strings.ToLower(gr.Reason + " " + strings.Join(gr.Evidence, " "))
	if !strings.Contains(combined, "behind") {
		t.Errorf("gate must surface mergeStateStatus=BEHIND so branch-out-of-date can match; got reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
}

func TestPrMergeGate_Fail_ContextMissing(t *testing.T) {
	gr := PrMergeGate{}.Verify(context.Background(), 42, t.TempDir())
	if gr.Passed {
		t.Fatalf("expected fail when context missing")
	}
}

func TestPrMergeGate_Fail_NoPrNumber(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{})
	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when pr_number absent")
	}
}

func TestPrMergeGate_Fail_GhRetryThenSucceedFails(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("network down")
	})
	stubExecGitForGate(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		// Local git fallback finds no merge commit either.
		return []byte("e440f2ca feat(#3350): unrelated commit\n"), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when gh fails on every retry and git fallback finds nothing")
	}
}

// TestPrMergeGate_RateLimit_LocalGitFallback_PR is the headline regression test
// for #3372: gh pr view returns the GitHub GraphQL rate-limit string in stdout
// (rather than erroring), the gate must detect that and fall back to local git
// to find the merge commit. The merge commit's subject carries `(#PR)` from
// the squash-merge convention.
func TestPrMergeGate_RateLimit_LocalGitFallback_PR(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("GraphQL: API rate limit already exceeded\n"), nil
	})
	stubExecGitForGate(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte(
			"b45e67b3 refactor(#42): extract Notifier interface (#100)\n" +
				"3942b26b feat(#3357): unrelated commit\n",
		), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when gh rate-limited and local git shows the merge; reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
	combined := gr.Reason + " " + strings.Join(gr.Evidence, " ")
	if !strings.Contains(combined, "local git") {
		t.Errorf("expected reason to mention the local-git fallback path; got reason=%q", gr.Reason)
	}
}

// When gh returns an error AND the local git log shows the issue number on a
// recent commit, the gate accepts that as proof of merge.
func TestPrMergeGate_GhError_LocalGitFallback_IssueNumber(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("net/http: TLS handshake timeout")
	})
	stubExecGitForGate(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		// Note: PR number `(#100)` is absent — only the issue number `#42`
		// appears in the subject. Gate must still accept this.
		return []byte("b45e67b3 refactor(#42): extract Notifier interface\n"), nil
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass when gh errored and local git shows the issue ref; reason=%q evidence=%v",
			gr.Reason, gr.Evidence)
	}
}

// Local git fallback failing (e.g., no remote-tracking ref) must NOT silently
// pass — it should surface the real problem so the operator can act.
func TestPrMergeGate_RateLimit_LocalGitFallback_GitFails(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte("API rate limit exceeded for installation\n"), nil
	})
	stubExecGitForGate(t, func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return nil, errors.New("fatal: ambiguous argument 'origin/main': unknown revision")
	})

	gr := PrMergeGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when both gh and local git fail")
	}
}
