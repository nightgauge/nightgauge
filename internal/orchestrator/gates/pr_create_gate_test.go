package gates

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// stubExecGh swaps the package-level execGh for the duration of the test.
// Returns the prior value as a restore handle.
func stubExecGh(t *testing.T, fn func(ctx context.Context, args ...string) ([]byte, error)) {
	t.Helper()
	prev := execGh
	execGh = fn
	t.Cleanup(func() { execGh = prev })
}

func TestPrCreateGate_Pass(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
		"pr_url":    "https://github.com/o/r/pull/100",
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN","number":100}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q evidence=%v", gr.Reason, gr.Evidence)
	}
}

func TestPrCreateGate_Fail_PrClosed(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"CLOSED","number":100}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when PR state is CLOSED")
	}
}

// TestPrCreateGate_SkillSaidSuccessButNoPR covers the canonical
// "skill reported success but no pr_number was recorded" scenario.
func TestPrCreateGate_SkillSaidSuccessButNoPR(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_url": "https://github.com/o/r/pull/0",
	})
	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when pr_number is 0")
	}
}

func TestPrCreateGate_Retries_OnTransientGhFailure(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	calls := 0
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		calls++
		if calls < 3 {
			return nil, errors.New("transient")
		}
		return []byte(`{"state":"OPEN","number":100}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if !gr.Passed {
		t.Fatalf("expected pass after retry; got reason=%q", gr.Reason)
	}
	if calls != 3 {
		t.Errorf("expected 3 gh attempts, got %d", calls)
	}
}

func TestPrCreateGate_Fail_GhFailsAllRetries(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("permanent")
	})

	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when gh fails on every retry")
	}
}

// TestPrCreateGate_PinsRepoFromPRURL reproduces the multi-repo false-negative
// (#3885): in a multi-repo workspace the scheduler's CWD is the umbrella repo,
// so a bare lookup resolves against the wrong repo. The REST primary path must
// target the repo derived from the recorded pr_url so the check hits the repo
// where the PR actually lives — for REST that means the slug is embedded in the
// `repos/{owner}/{repo}/pulls/{N}` endpoint, not a `--repo` flag.
func TestPrCreateGate_PinsRepoFromPRURL(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-45.json"), map[string]any{
		"pr_number": 67,
		"pr_url":    "https://github.com/nightgauge/acmeapp-platform/pull/67",
	})

	var gotArgs []string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		gotArgs = args
		return []byte(`{"state":"open","number":67}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 45, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}

	// REST primary: the endpoint must embed the slug + number.
	wantEndpoint := "repos/nightgauge/acmeapp-platform/pulls/67"
	found := false
	for _, a := range gotArgs {
		if a == wantEndpoint {
			found = true
		}
	}
	if !found {
		t.Errorf("expected REST endpoint %q in args, got %v", wantEndpoint, gotArgs)
	}
}

// TestPrCreateGate_VerifiesOverREST confirms the primary path is REST (the core
// rate-limit bucket), NOT `gh pr view` (GraphQL) — the root cause of the #99
// false-failure was the gate consuming the exhausted GraphQL bucket.
func TestPrCreateGate_VerifiesOverREST(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-99.json"), map[string]any{
		"pr_number": 112,
		"pr_url":    "https://github.com/nightgauge/acmeapp-platform/pull/112",
	})

	var firstVerb string
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if firstVerb == "" && len(args) > 0 {
			firstVerb = args[0]
		}
		return []byte(`{"state":"open","number":112}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 99, ws)
	if !gr.Passed {
		t.Fatalf("expected pass; reason=%q", gr.Reason)
	}
	if firstVerb != "api" {
		t.Errorf("expected REST (`gh api ...`) as the primary verification, got first verb %q", firstVerb)
	}
}

// TestPrCreateGate_BothTransportsRateLimited_InconclusivePass is the #99
// regression: when BOTH REST and GraphQL are rate-limited, a PR the skill
// recorded must NOT be hard-failed. The gate trusts the recorded pr_number and
// passes (KindOK) — an environmental rate-limit is not a skill/code defect.
func TestPrCreateGate_BothTransportsRateLimited_InconclusivePass(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-99.json"), map[string]any{
		"pr_number": 112,
		"pr_url":    "https://github.com/nightgauge/acmeapp-platform/pull/112",
	})
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "api" {
			return nil, errors.New("HTTP 403: API rate limit exceeded")
		}
		return nil, errors.New("GraphQL: API rate limit exceeded")
	})

	gr := PrCreateGate{}.Verify(context.Background(), 99, ws)
	if !gr.Passed {
		t.Fatalf("expected inconclusive PASS when both transports are rate-limited; reason=%q", gr.Reason)
	}
	if gr.Kind != KindOK {
		t.Errorf("expected KindOK, got %q", gr.Kind)
	}
}

// TestPrCreateGate_RestRateLimited_FallsBackToGraphQL verifies that a healthy
// GraphQL transport still confirms the PR when REST is rate-limited.
func TestPrCreateGate_RestRateLimited_FallsBackToGraphQL(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-99.json"), map[string]any{
		"pr_number": 112,
		"pr_url":    "https://github.com/nightgauge/acmeapp-platform/pull/112",
	})
	graphqlCalled := false
	stubExecGh(t, func(_ context.Context, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "api" {
			return nil, errors.New("HTTP 403: API rate limit exceeded")
		}
		graphqlCalled = true
		return []byte(`{"state":"OPEN","number":112}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 99, ws)
	if !gr.Passed {
		t.Fatalf("expected pass via GraphQL fallback; reason=%q", gr.Reason)
	}
	if !graphqlCalled {
		t.Errorf("expected GraphQL fallback to be attempted when REST is rate-limited")
	}
}

// TestPrCreateGate_RestReportsAbsent_Fail confirms that a definitive REST 404
// (the PR genuinely does not exist) is a genuine gate failure (KindFail) — NOT
// masked by the rate-limit inconclusive path, and NOT KindNoOp (which would
// trip the synthetic skill-no-op regression guard).
func TestPrCreateGate_RestReportsAbsent_Fail(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-99.json"), map[string]any{
		"pr_number": 112,
		"pr_url":    "https://github.com/nightgauge/acmeapp-platform/pull/112",
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return nil, errors.New("gh: Not Found (HTTP 404)")
	})

	gr := PrCreateGate{}.Verify(context.Background(), 99, ws)
	if gr.Passed {
		t.Fatalf("expected fail when the PR does not exist (REST 404)")
	}
	if gr.Kind != KindFail {
		t.Errorf("expected KindFail for a definitively absent PR, got %q", gr.Kind)
	}
}

func TestRepoSlugFromPRURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/nightgauge/acmeapp-platform/pull/67": "nightgauge/acmeapp-platform",
		"https://github.com/o/r/pull/1":                          "o/r",
		"":                                                       "",
		"not a url":                                              "",
		"https://gitlab.com/group/proj/-/merge_requests/3": "", // non-github → CWD fallback
	}
	for url, want := range cases {
		if got := repoSlugFromPRURL(url); got != want {
			t.Errorf("repoSlugFromPRURL(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestPrCreateGate_Fail_PrNumberMismatch(t *testing.T) {
	ws := t.TempDir()
	writeJSON(t, filepath.Join(ws, ".nightgauge", "pipeline", "pr-42.json"), map[string]any{
		"pr_number": 100,
	})
	stubExecGh(t, func(_ context.Context, _ ...string) ([]byte, error) {
		return []byte(`{"state":"OPEN","number":999}`), nil
	})

	gr := PrCreateGate{}.Verify(context.Background(), 42, ws)
	if gr.Passed {
		t.Fatalf("expected fail when gh returns a different PR number")
	}
}
