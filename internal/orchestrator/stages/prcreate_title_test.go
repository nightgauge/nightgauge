package stages

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

const longIssueTitle = "Pipeline PR and queue-run titles repeat the issue or the whole commit message every time"

func titleSnap() PRCreateSnapshot {
	return PRCreateSnapshot{IssueNumber: 42, IssueType: "fix", IssueTitle: "short issue title"}
}

func assertWithinLimit(t *testing.T, title string) {
	t.Helper()
	if n := utf8.RuneCountInString(title); n > maxTitleRunes {
		t.Errorf("title is %d runes, over the %d limit: %q", n, maxTitleRunes, title)
	}
}

func TestRenderTitle_RecordedSummaryWins(t *testing.T) {
	s := titleSnap()
	s.PRSummary = "drop run-name and cap PR titles"
	if got, want := RenderTitle(s), "fix(#42): drop run-name and cap PR titles"; got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
	if note := TitleNote(s); note != "" {
		t.Errorf("a summary used as written needs no note, got %q", note)
	}
}

func TestRenderTitle_LongIssueTitleCutsAtWordBoundary(t *testing.T) {
	s := titleSnap()
	s.IssueTitle = longIssueTitle
	got := RenderTitle(s)
	assertWithinLimit(t, got)
	want := "fix(#42): Pipeline PR and queue-run titles repeat the issue or the whole"
	if got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
	if note := TitleNote(s); !strings.Contains(note, "falls back to the issue title (cut at a word boundary)") {
		t.Errorf("TitleNote = %q", note)
	}
}

func TestRenderTitle_ShortIssueTitleFallbackIsNoted(t *testing.T) {
	s := titleSnap()
	if got, want := RenderTitle(s), "fix(#42): short issue title"; got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
	note := TitleNote(s)
	if !strings.Contains(note, "falls back to the issue title") || strings.Contains(note, "cut at") {
		t.Errorf("TitleNote = %q", note)
	}
}

func TestRenderTitle_OverLongSummaryIsCut(t *testing.T) {
	s := titleSnap()
	s.PRSummary = strings.Repeat("word ", 30)
	got := RenderTitle(s)
	assertWithinLimit(t, got)
	if strings.HasSuffix(got, " ") || !strings.HasSuffix(got, "word") {
		t.Errorf("cut is not at a word boundary: %q", got)
	}
	if note := TitleNote(s); !strings.Contains(note, "pr_summary exceeds") {
		t.Errorf("TitleNote = %q", note)
	}
}

func TestRenderTitle_BlankSummaryFallsBack(t *testing.T) {
	for _, blank := range []string{"", "   ", "\n\t", "feat:"} {
		s := titleSnap()
		s.PRSummary = blank
		if got, want := RenderTitle(s), "fix(#42): short issue title"; got != want {
			t.Errorf("summary %q: RenderTitle = %q, want %q", blank, got, want)
		}
	}
}

func TestRenderTitle_WordEndingExactlyAtBudgetIsKept(t *testing.T) {
	s := titleSnap()
	scope := len(titleScope(s))
	word := strings.Repeat("a", maxTitleRunes-scope-4)
	s.PRSummary = word + " bbb ccc"
	got := RenderTitle(s)
	if want := "fix(#42): " + word + " bbb"; got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
}

func TestRenderTitle_TrimsPunctuationStrandedByTheCut(t *testing.T) {
	s := titleSnap()
	scope := len(titleScope(s))
	s.PRSummary = strings.Repeat("a", maxTitleRunes-scope-6) + " bb, - cc dddd"
	got := RenderTitle(s)
	assertWithinLimit(t, got)
	if !strings.HasSuffix(got, " bb") {
		t.Errorf("stranded punctuation kept: %q", got)
	}
}

func TestRenderTitle_CountsRunesNotBytes(t *testing.T) {
	s := titleSnap()
	budget := maxTitleRunes - utf8.RuneCountInString(titleScope(s))
	exact := strings.Repeat("é", budget)
	s.PRSummary = exact
	if got := RenderTitle(s); got != titleScope(s)+exact {
		t.Errorf("a %d-rune summary fits the budget, got %q", budget, got)
	}
	s.PRSummary = exact + " more"
	got := RenderTitle(s)
	assertWithinLimit(t, got)
	if got != titleScope(s)+exact {
		t.Errorf("multi-byte cut = %q", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("cut split a rune: %q", got)
	}
}

func TestRenderTitle_SingleOverLongWordIsCutAtTheLimit(t *testing.T) {
	s := titleSnap()
	s.PRSummary = strings.Repeat("x", 200)
	got := RenderTitle(s)
	if n := utf8.RuneCountInString(got); n != maxTitleRunes {
		t.Errorf("title is %d runes, want exactly %d: the word has no boundary", n, maxTitleRunes)
	}
}

func TestRenderTitle_FiveDigitIssueNumberKeepsTheLimit(t *testing.T) {
	s := titleSnap()
	s.IssueNumber = 12345
	s.IssueType = "refactor"
	s.IssueTitle = longIssueTitle
	got := RenderTitle(s)
	assertWithinLimit(t, got)
	if !strings.HasPrefix(got, "refactor(#12345): ") {
		t.Errorf("scope lost: %q", got)
	}
}

func TestRenderTitle_NeverCarriesAClosingKeyword(t *testing.T) {
	cases := []struct{ summary, issue string }{
		{summary: "Fix #12 and drop the flag"},
		{summary: "drop the flag\nCloses #12"},
		{issue: "Resolves #12 in the parser"},
	}
	for _, c := range cases {
		s := titleSnap()
		s.PRSummary, s.IssueTitle = c.summary, c.issue
		got := RenderTitle(s)
		if strings.Contains(strings.ToLower(got), "fix #12") ||
			strings.Contains(strings.ToLower(got), "closes #12") ||
			strings.Contains(strings.ToLower(got), "resolves #12") {
			t.Errorf("closing keyword survived: %q", got)
		}
		if !strings.Contains(got, "Refs #12") {
			t.Errorf("keyword not rewritten to Refs: %q", got)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("title spans lines: %q", got)
		}
	}
}

// Every closing form is defanged in a title, not only `#N` (#2859).
func TestRenderTitle_DefangsGHAndURLClosingForms(t *testing.T) {
	cases := []struct{ summary, issue, want string }{
		{summary: "Fixes GH-12 in the parser", want: "Refs GH-12"},
		{issue: "Fixes GH-12 in the parser", want: "Refs GH-12"},
		{summary: "Closes https://github.com/o/r/issues/12 now", want: "Refs https://github.com/o/r/issues/12"},
		{issue: "Closes https://github.com/o/r/issues/12 now", want: "Refs https://github.com/o/r/issues/12"},
	}
	for _, c := range cases {
		s := titleSnap()
		s.PRSummary, s.IssueTitle = c.summary, c.issue
		got := RenderTitle(s)
		if !strings.Contains(got, c.want) {
			t.Errorf("RenderTitle(%+v) = %q, want it to contain %q", c, got, c.want)
		}
		if strings.Contains(strings.ToLower(got), "fixes gh-12") ||
			strings.Contains(strings.ToLower(got), "closes https://") {
			t.Errorf("closing keyword survived: %q", got)
		}
	}
}

func TestRenderTitle_StripsConventionalPrefixFromSummary(t *testing.T) {
	s := titleSnap()
	s.PRSummary = "fix(#42): cap the title"
	if got, want := RenderTitle(s), "fix(#42): cap the title"; got != want {
		t.Errorf("RenderTitle = %q, want %q", got, want)
	}
}

func TestRenderTitle_LongInputDeterministic(t *testing.T) {
	s := titleSnap()
	s.PRSummary = strings.Repeat("word ", 30)
	first := RenderTitle(s)
	for i := 0; i < 50; i++ {
		if got := RenderTitle(s); got != first {
			t.Fatalf("drifted on iteration %d: %q vs %q", i, first, got)
		}
	}
}

func TestCommitMessage_SummaryWinsOverMissingIssueTitle(t *testing.T) {
	s := PRCreateSnapshot{IssueNumber: 42, IssueType: "fix", PRSummary: "cap the title"}
	if subject, _, _ := strings.Cut(CommitMessage(s), "\n"); subject != "fix(#42): cap the title" {
		t.Errorf("subject = %q", subject)
	}
	s.PRSummary = ""
	if subject, _, _ := strings.Cut(CommitMessage(s), "\n"); subject != "fix(#42): implement issue #42" {
		t.Errorf("subject = %q", subject)
	}
}

// captureLog routes the standard logger to a buffer for the test's duration.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestRunner_LogsTitleFallback_OnBothPaths(t *testing.T) {
	snap := richSnap()
	snap.IssueTitle = longIssueTitle
	snap.PRSummary = ""
	const want = "title falls back to the issue title (cut at a word boundary)"

	buf := captureLog(t)
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 99, URL: "u", NodeID: "PR_99"}}
	if _, err := newTestRunner(snap, prc, &fakeGit{}).Run(context.Background(), 42, "owner/repo", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), want) {
		t.Errorf("create path did not log the fallback: %q", buf.String())
	}
	assertWithinLimit(t, prc.lastTitle)

	buf.Reset()
	prc = &fakePRClient{listResults: []CreatedPR{{Number: 7, URL: "u"}}}
	if _, err := newTestRunner(snap, prc, &fakeGit{}).Run(context.Background(), 42, "owner/repo", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), want) {
		t.Errorf("existing-PR path did not log the fallback: %q", buf.String())
	}
}

func TestRunner_RecordedSummary_LogsNothingAboutTheTitle(t *testing.T) {
	snap := richSnap()
	snap.PRSummary = "cap the title"
	buf := captureLog(t)
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 99, URL: "u", NodeID: "PR_99"}}
	if _, err := newTestRunner(snap, prc, &fakeGit{}).Run(context.Background(), 42, "owner/repo", "/tmp"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "title") {
		t.Errorf("unexpected title log: %q", buf.String())
	}
	if prc.lastTitle != "feat(#42): cap the title" {
		t.Errorf("title handed to CreatePR = %q", prc.lastTitle)
	}
}

func writePipelineFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadTitleSnapshot(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)

	if _, err := ReadTitleSnapshot(root, 42); err == nil {
		t.Fatal("no issue context must be an error, not a bare-scope title")
	}

	writePipelineFile(t, dir, "issue-42.json", `{"title":"","type":"bug"}`)
	if _, err := ReadTitleSnapshot(root, 42); err == nil {
		t.Fatal("no title text at all must be an error")
	}

	writePipelineFile(t, dir, "issue-42.json", `{"title":"the bug as reported","type":"bug"}`)
	snap, err := ReadTitleSnapshot(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := RenderTitle(snap), "fix(#42): the bug as reported"; got != want {
		t.Errorf("without dev context: %q, want %q", got, want)
	}

	writePipelineFile(t, dir, "dev-42.json", `{"pr_summary":"state the change"}`)
	if snap, err = ReadTitleSnapshot(root, 42); err != nil {
		t.Fatal(err)
	}
	if got, want := RenderTitle(snap), "fix(#42): state the change"; got != want {
		t.Errorf("with a summary: %q, want %q", got, want)
	}

	writePipelineFile(t, dir, "dev-42.json", `{`)
	if _, err = ReadTitleSnapshot(root, 42); err == nil {
		t.Error("a corrupt dev context must be an error")
	}
}

func TestDefaultReadCreateContext_ProjectsPRSummary(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	writePipelineFile(t, dir, "issue-42.json", `{"title":"t","type":"bug"}`)
	writePipelineFile(t, dir, "dev-42.json", `{"pr_summary":"state the change"}`)
	snap, err := defaultReadCreateContext(context.Background(), root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if snap.PRSummary != "state the change" {
		t.Errorf("PRSummary = %q", snap.PRSummary)
	}
	gittest.Run(t, root, "checkout", "-q", "-b", "fix/42-slug")
	if snap, err = ReadTitleSnapshot(root, 42); err != nil {
		t.Fatal(err)
	}
	if got := RenderTitle(snap); got != "fix(#42): state the change" {
		t.Errorf("title = %q: the prefix must come from the branch type", got)
	}
}

// A recorded summary is enough on its own: the pr-create skill tolerates a
// missing issue context, so the title must too.
func TestReadTitleSnapshot_SummaryWithoutIssueContext(t *testing.T) {
	root := layouttest.Repo(t)
	dir := layouttest.MkPipelineDir(t, root)
	body := []byte(`{"pr_summary":"state the change"}`)
	if err := os.WriteFile(filepath.Join(dir, "dev-42.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := ReadTitleSnapshot(root, 42)
	if err != nil {
		t.Fatal(err)
	}
	if snap.PRSummary != "state the change" {
		t.Errorf("PRSummary = %q", snap.PRSummary)
	}
	gittest.Run(t, root, "checkout", "-q", "-b", "fix/42-slug")
	if snap, err = ReadTitleSnapshot(root, 42); err != nil {
		t.Fatal(err)
	}
	if got := RenderTitle(snap); got != "fix(#42): state the change" {
		t.Errorf("title = %q: the prefix must come from the branch type", got)
	}
}
