package hooks

import "testing"

// stageBash builds a PreToolUse Bash payload for the stage gate.
func stageBash(command string) []byte {
	return makeGateInput("Bash", BashToolInput{Command: command})
}

func TestStageGate_BlockedForgeMutationsInAnalysisStages(t *testing.T) {
	stages := []string{"issue-pickup", "feature-planning", "feature-validate"}
	blocked := []string{
		"gh pr create --title x --body y --base main",
		"gh pr merge 4143 --squash",
		"gh pr ready 12",
		"gh issue close 99",
		"glab mr create --title x",
		"glab mr merge 7",
		"nightgauge forge pr create --title x --head feat/1 --base main",
		"nightgauge forge pr merge 5 --squash",
		`GH_TOKEN=abc gh pr merge 4143 --squash`,    // env-prefixed still caught
		`bash -c "gh pr create --title x --base main"`, // wrapper-expanded still caught
	}
	for _, stage := range stages {
		for _, cmd := range blocked {
			got := evaluateStageGate(stageBash(cmd), stage)
			if got.Decision != "block" {
				t.Errorf("[%s] expected block for %q, got %q", stage, cmd, got.Decision)
			}
			if got.Reason == "" {
				t.Errorf("[%s] block for %q must carry a reason", stage, cmd)
			}
		}
	}
}

func TestStageGate_AllowsLegitimateAnalysisStageGitAndForge(t *testing.T) {
	// Operations the analysis stages legitimately perform — must NOT be blocked.
	cases := []struct {
		stage string
		cmd   string
	}{
		// issue-pickup pushes the new feature branch + self-assigns.
		{"issue-pickup", "git push -u origin feat/1-photo-upload"},
		{"issue-pickup", "git checkout -b feat/1-photo-upload"},
		{"issue-pickup", "nightgauge forge issue edit 1 --add-assignee @me"},
		{"issue-pickup", "gh issue view 1 --json title"},
		{"issue-pickup", "gh pr list --state open"},
		{"issue-pickup", "git status"},
		// feature-validate legitimately commits + pushes validated work.
		{"feature-validate", "git commit -m 'feat(#1): implement'"},
		{"feature-validate", "git push origin HEAD"},
		{"feature-validate", "git add -A"},
		// read-only forge across stages.
		{"feature-planning", "gh pr view 5"},
		{"feature-validate", "gh pr checks 5"},
	}
	for _, c := range cases {
		got := evaluateStageGate(stageBash(c.cmd), c.stage)
		if got.Decision != "allow" {
			t.Errorf("[%s] expected allow for %q, got block: %s", c.stage, c.cmd, got.Reason)
		}
	}
}

func TestStageGate_CommitFenceTiering(t *testing.T) {
	// git commit/merge/rebase are blocked for the pure-analysis stages...
	for _, stage := range []string{"issue-pickup", "feature-planning"} {
		for _, cmd := range []string{
			"git commit -m wip",
			"git merge origin/main",
			"git rebase main",
			"git cherry-pick abc123",
			"git revert abc123",
		} {
			if got := evaluateStageGate(stageBash(cmd), stage); got.Decision != "block" {
				t.Errorf("[%s] expected block for %q, got allow", stage, cmd)
			}
		}
	}
	// ...but NOT for feature-validate, which legitimately commits.
	if got := evaluateStageGate(stageBash("git commit -m wip"), "feature-validate"); got.Decision != "allow" {
		t.Errorf("feature-validate git commit must be allowed (it commits validated work); got block: %s", got.Reason)
	}
}

func TestStageGate_NonFencedStagesAndContextsAllowEverything(t *testing.T) {
	// Implementation stages and non-pipeline (empty stage) contexts are no-ops:
	// pr-create/pr-merge/feature-dev legitimately mutate git/forge.
	cases := []struct {
		stage string
		cmd   string
	}{
		{"pr-create", "gh pr create --title x --base main"},
		{"pr-merge", "gh pr merge 5 --squash"},
		{"feature-dev", "git commit -m 'feat: x'"},
		{"feature-dev", "gh pr create --title x"},
		{"", "gh pr merge 5 --squash"},        // interactive / non-pipeline
		{"some-future-stage", "gh pr merge 5"}, // unknown stage → no-op
	}
	for _, c := range cases {
		if got := evaluateStageGate(stageBash(c.cmd), c.stage); got.Decision != "allow" {
			t.Errorf("[stage=%q] expected allow for %q, got block: %s", c.stage, c.cmd, got.Reason)
		}
	}
}

func TestStageGate_AdminMergeBypassBlockedInEveryPipelineStage(t *testing.T) {
	// #186: `gh pr merge --admin` was improvised by a pr-merge agent to bypass
	// branch protection. The ban applies to EVERY pipeline stage — including
	// the implementation stages the fences otherwise exempt.
	stages := []string{
		"issue-pickup", "feature-planning", "feature-dev",
		"feature-validate", "pr-create", "pr-merge", "some-future-stage",
	}
	blocked := []string{
		"gh pr merge 276 --admin",
		"gh pr merge 276 --squash --admin --delete-branch",
		"gh pr merge 276 --auto",
		"glab mr merge 7 --auto",
		`GH_TOKEN=abc gh pr merge 276 --admin`,        // env-prefixed still caught
		`bash -c "gh pr merge 276 --squash --admin"`,  // wrapper-expanded still caught
	}
	for _, stage := range stages {
		for _, cmd := range blocked {
			got := evaluateStageGate(stageBash(cmd), stage)
			if got.Decision != "block" {
				t.Errorf("[%s] expected block for %q, got %q", stage, cmd, got.Decision)
			}
		}
	}

	// Normal merges in pr-merge stay allowed; prohibition prose in an echo is
	// not an invocation; interactive (empty stage) sessions are untouched.
	allowed := []struct {
		stage string
		cmd   string
	}{
		{"pr-merge", "gh pr merge 276 --squash --delete-branch"},
		{"pr-merge", `echo "never use --admin to merge"`},
		{"", "gh pr merge 276 --admin"}, // non-pipeline context → no-op
	}
	for _, c := range allowed {
		if got := evaluateStageGate(stageBash(c.cmd), c.stage); got.Decision != "allow" {
			t.Errorf("[stage=%q] expected allow for %q, got block: %s", c.stage, c.cmd, got.Reason)
		}
	}
}

func TestStageGate_NonBashAndMalformedAllow(t *testing.T) {
	// Non-Bash tool → allow.
	if got := evaluateStageGate(makeGateInput("Edit", FileToolInput{FilePath: "x.go"}), "issue-pickup"); got.Decision != "allow" {
		t.Errorf("non-Bash tool must be allowed; got %q", got.Decision)
	}
	// Malformed JSON → fail open.
	if got := evaluateStageGate([]byte("{not json"), "issue-pickup"); got.Decision != "allow" {
		t.Errorf("malformed input must fail open; got %q", got.Decision)
	}
	// Read-only verbs on pr resource are allowed even in fenced stages.
	for _, cmd := range []string{"gh pr view 1", "gh pr list", "gh pr checks 1", "gh pr comment 1 --body hi", "gh issue view 1", "gh issue edit 1 --add-label x"} {
		if got := evaluateStageGate(stageBash(cmd), "issue-pickup"); got.Decision != "allow" {
			t.Errorf("read-only/edit forge op %q must be allowed; got block: %s", cmd, got.Reason)
		}
	}
}
