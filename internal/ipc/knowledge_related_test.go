package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge/recall"
)

// Related Decisions used to render rows like `decisions 0.69` (#1207).
//
// Three defects stacked. Two of them live here: the query was the DIGITS of the
// issue number, so a "related" hit was any file that tokenized them; and the
// self-filter matched `"<N>-"` as a substring anywhere in the path, so issue 5
// suppressed every knowledge base whose slug contained those two characters.

func writeRelatedContextFile(t *testing.T, root string, issueNumber int, body string) {
	t.Helper()
	dir := filepath.Join(root, ".nightgauge", "pipeline")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, fmt.Sprintf("issue-%d.json", issueNumber))
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRelatedIssueQuery_UsesTheTitleNotTheDigits(t *testing.T) {
	root := t.TempDir()
	writeRelatedContextFile(t, root, 390, `{"issue_number":390,"title":"cost cache token counts"}`)

	got := relatedIssueQuery(root, 390)
	if !strings.Contains(got, "cost cache token counts") {
		t.Errorf("relatedIssueQuery = %q, want the issue title", got)
	}
	// "issue 390" is the query that made every score noise.
	if strings.Contains(got, "390") {
		t.Errorf("relatedIssueQuery = %q — the issue number is not a subject", got)
	}
}

func TestRelatedIssueQuery_AddsSubjectLabelsOnly(t *testing.T) {
	root := t.TempDir()
	writeRelatedContextFile(t, root, 390, `{"issue_number":390,"title":"token counts",
		"labels":["component:go-binary","priority:high","size:L","area:recall"]}`)

	got := relatedIssueQuery(root, 390)
	for _, want := range []string{"token counts", "go-binary", "recall"} {
		if !strings.Contains(got, want) {
			t.Errorf("relatedIssueQuery = %q, missing %q", got, want)
		}
	}
	// Priority and size say nothing about subject matter; including them would
	// rank every high-priority knowledge base as "related".
	for _, unwanted := range []string{"high", "size", "priority"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("relatedIssueQuery = %q, should not carry %q", got, unwanted)
		}
	}
}

func TestRelatedIssueQuery_EmptyWhenThereIsNothingToAsk(t *testing.T) {
	root := t.TempDir()
	// No context file: the pickup stage has not run. Ranking digit collisions
	// here would be worse than reporting nothing.
	if got := relatedIssueQuery(root, 390); got != "" {
		t.Errorf("relatedIssueQuery with no context file = %q, want empty", got)
	}

	writeRelatedContextFile(t, root, 391, `{"issue_number":391}`)
	if got := relatedIssueQuery(root, 391); got != "" {
		t.Errorf("relatedIssueQuery with no title = %q, want empty", got)
	}
}

func TestIsOwnKnowledgeDir_AnchorsOnTheDirectorySegment(t *testing.T) {
	cases := []struct {
		path  string
		issue int
		want  bool
	}{
		{".nightgauge/knowledge/features/5-add-upload/decisions.md", 5, true},
		{".nightgauge/knowledge/features/5-add-upload/PRD.md", 5, true},
		// The substring form matched "5-" ANYWHERE, so issue 5 suppressed every
		// KB whose slug happened to contain those two characters.
		{".nightgauge/knowledge/features/912-http-5-retry/decisions.md", 5, false},
		{".nightgauge/knowledge/features/390-cache-v5-rollout/PRD.md", 5, false},
		{".nightgauge/knowledge/features/50-other/decisions.md", 5, false},
		{".nightgauge/knowledge/features/390-cost-cache/decisions.md", 390, true},
		{".nightgauge/knowledge/workspace/product/README.md", 5, false},
	}
	for _, c := range cases {
		if got := isOwnKnowledgeDir(c.path, c.issue); got != c.want {
			t.Errorf("isOwnKnowledgeDir(%q, %d) = %v, want %v", c.path, c.issue, got, c.want)
		}
	}
}

// The end-to-end claim: a title query ranks the semantically related KB first,
// where the digit query it replaced does not rank it at all.
func TestRelatedDecisions_TitleQueryBeatsTheDigitQuery(t *testing.T) {
	root := t.TempDir()
	mk := func(slug, body string) {
		dir := filepath.Join(root, ".nightgauge", "knowledge", "features", slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "decisions.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Genuinely related: shares the subject.
	mk("390-cost-cache-token-counts",
		"# Decisions\n\nDecision: route token counts through the Go native counter.\n")
	// Digit collision only: mentions 742 in prose, shares no subject.
	mk("111-discord-notifier",
		"# Decisions\n\nDecision: retry the webhook 742 times before giving up.\n")

	cfg := &config.KnowledgeConfig{}
	idx, err := recall.BuildIndex(root, nil, cfg)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}

	titleRes, err := recall.Query(idx, "token counts", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(titleRes.Hits) == 0 {
		t.Fatal("title query returned no hits")
	}
	if !strings.Contains(titleRes.Hits[0].Path, "390-cost-cache-token-counts") {
		t.Errorf("top hit for the title query = %q, want the KB about token counts",
			titleRes.Hits[0].Path)
	}

	// The query this replaced. It ranks the digit collision and misses the
	// related KB entirely — which is what a 0.69 "relevance" was measuring.
	digitRes, err := recall.Query(idx, "issue 742", 10, nil)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, h := range digitRes.Hits {
		if strings.Contains(h.Path, "390-cost-cache-token-counts") {
			t.Fatal("fixture is not exercising the defect: the digit query " +
				"found the related KB, so it proves nothing")
		}
	}
}
