package stages

import (
	"strings"
	"testing"
)

const (
	toolsBefore3582 = "tools: Read, Grep, Glob, Bash(npx -w *), Bash(npm run *), Bash(go test *), Bash(git diff *), Bash(nightgauge *)"
	toolsAfter3582  = "tools: Read, Grep, Glob, Bash(npx -w *), Bash(npm run *), Bash(go test *), Bash(git diff *), Bash(nightgauge layout path *), Bash(nightgauge forge issue view *)"
	ac3578          = "`.claude/agents/test-runner.md` `tools:` no longer grants the bare `Bash(nightgauge *)` pattern; it grants only the read-only `nightgauge` subcommands the agent needs."
)

func agentFile3582(tools string) string {
	return "---\nname: test-runner\ndescription: Runs tests.\n" + tools + "\nmodel: sonnet\n---\n\nBody.\n"
}

// repo3582 seeds main with base, then writes branch on the feature branch.
func repo3582(t *testing.T, base, branch map[string]string) *ACDiff {
	t.Helper()
	dir := changelogRepo(t)
	gitOut(t, dir, "checkout", "-q", "main")
	for p, c := range base {
		writeFile(t, dir, p, c)
	}
	gitOut(t, dir, "add", "-A")
	gitOut(t, dir, "commit", "-qm", "seed")
	gitOut(t, dir, "checkout", "-q", "docs/2661-changelog")
	gitOut(t, dir, "merge", "-q", "main")
	for p, c := range branch {
		writeFile(t, dir, p, c)
	}
	return collect(t, dir)
}

// #3578's first criterion against commit 54ddf9416's change: the subject
// span `tools:` stays, the granted pattern is gone.
func TestVerifyACAgainstDiff_3582_IssueCriterionNotUnmet(t *testing.T) {
	const f = ".claude/agents/test-runner.md"
	d := repo3582(t, map[string]string{f: agentFile3582(toolsBefore3582)},
		map[string]string{f: agentFile3582(toolsAfter3582)})
	got, ok := VerifyACAgainstDiff(ac3578, nil, d, ACTestState{})
	if !ok || got.Status != ACMet || !strings.Contains(got.Evidence, "Bash(nightgauge *)") ||
		strings.Contains(got.Evidence, "tools:") {
		t.Fatalf("#3578 criterion: got %+v ok=%v, want met on `Bash(nightgauge *)` only", got, ok)
	}
}

func TestVerifyACAgainstDiff_3582_GrantStillPresent(t *testing.T) {
	d := repo3582(t, map[string]string{"f.md": "a\nX here\n"},
		map[string]string{"f.md": "a\nX here\nb\n"})
	got, ok := VerifyACAgainstDiff("`f.md` no longer grants `X`.", nil, d, ACTestState{})
	if !ok || got.Status != ACUnmet {
		t.Fatalf("got %+v ok=%v, want unmet", got, ok)
	}
}

func TestVerifyACAgainstDiff_3582_GrantRemoved(t *testing.T) {
	d := repo3582(t, map[string]string{"f.md": "a\nX here\n"},
		map[string]string{"f.md": "a\n"})
	got, ok := VerifyACAgainstDiff("`f.md` no longer grants `X`.", nil, d, ACTestState{})
	if !ok || got.Status != ACMet {
		t.Fatalf("got %+v ok=%v, want met", got, ok)
	}
}

func TestACNegContentRE_3582Verbs(t *testing.T) {
	for _, s := range []string{
		"no longer grants", "no longer grant", "no longer allows", "no longer admits",
		"no longer permits", "no longer carry", "no longer carries", "no longer lists",
		"no longer declares", "no longer defines",
		"does not grant", "doesn't grant", "does not allow", "doesn't allow",
		"does not admit", "doesn't admit", "does not permit", "doesn't permit",
	} {
		if !acNegContentRE.MatchString("F " + s + " X") {
			t.Errorf("%q should be a negative-content phrase", s)
		}
	}
	for _, s := range []string{"no longer fails", "no longer prints", "lists", "grants"} {
		if acNegContentRE.MatchString("F " + s + " X") {
			t.Errorf("%q must stay with the judge", s)
		}
	}
}

func TestVerifyACAgainstDiff_3582_DoesNotGrant(t *testing.T) {
	d := repo3582(t, map[string]string{"f.md": "a\nX here\n", "g.md": "Y\n"},
		map[string]string{"f.md": "a\n", "g.md": "Y\nz\n"})
	if got, ok := VerifyACAgainstDiff("`f.md` does not grant `X`.", nil, d, ACTestState{}); !ok || got.Status != ACMet {
		t.Errorf("removed: got %+v ok=%v, want met", got, ok)
	}
	if got, ok := VerifyACAgainstDiff("`g.md` doesn't allow `Y`.", nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("present: got %+v ok=%v, want unmet", got, ok)
	}
	// No span follows the phrase: every span is a target.
	if got, ok := VerifyACAgainstDiff("`Y` in `g.md`: the file no longer contains it.", nil, d, ACTestState{}); !ok || got.Status != ACUnmet {
		t.Errorf("fallback: got %+v ok=%v, want unmet", got, ok)
	}
}
