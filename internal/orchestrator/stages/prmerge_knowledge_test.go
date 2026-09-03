package stages

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/knowledge"
)

// cleanSnapshot is a PR nothing but the knowledge gate could stop.
func cleanSnapshot() PRViewSnapshot {
	return PRViewSnapshot{
		State: "OPEN", Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN",
		ReviewDecision: "APPROVED",
	}
}

// TestDeterministicRunner_NonConformantKnowledge_DoesNotMerge pins the gate on
// the DEFAULT path. Placing the check only in the pr-merge skill would leave
// it unenforced on every normal run, because the skill is reached only when
// this runner punts.
func TestDeterministicRunner_NonConformantKnowledge_DoesNotMerge(t *testing.T) {
	workdir := t.TempDir()
	entry := filepath.Join(workdir, ".nightgauge", "knowledge", "features", "100-widget", "PRD.md")
	if err := os.MkdirAll(filepath.Dir(entry), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entry, []byte("# PRD: #100\n\nNo frontmatter block.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gh := &fakeGh{preMerge: cleanSnapshot(), postMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", workdir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if gh.mergeCalls != 0 {
		t.Fatalf("merged despite a non-conformant knowledge entry (%d merge calls)", gh.mergeCalls)
	}
	if res.Path != PathPunt {
		t.Errorf("Path = %q, want punt", res.Path)
	}
	if !strings.HasPrefix(res.Reason, ReasonKnowledgeNonConformant) {
		t.Errorf("Reason = %q, want it to start with %q", res.Reason, ReasonKnowledgeNonConformant)
	}
	if !strings.Contains(res.Reason, "PRD.md") {
		t.Errorf("Reason must name the offending entry, got %q", res.Reason)
	}
}

// TestDeterministicRunner_ConformantKnowledge_Merges is the other half: the
// gate must not turn every merge into a punt.
func TestDeterministicRunner_ConformantKnowledge_Merges(t *testing.T) {
	workdir := t.TempDir()
	if _, err := knowledge.Scaffold(workdir, 100, "Widget", nil); err != nil {
		t.Fatal(err)
	}

	gh := &fakeGh{preMerge: cleanSnapshot(), postMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", workdir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Fatalf("Path = %q (%s), want merged", res.Path, res.Reason)
	}
	if gh.mergeCalls != 1 {
		t.Errorf("merge calls = %d, want 1", gh.mergeCalls)
	}
}

// TestDeterministicRunner_UnrelatedIssueViolation_StillMerges is why the gate
// is scoped to one issue's directory. The knowledge base is local, gitignored,
// per-machine state; a stale entry from an unrelated issue must never block
// somebody else's merge.
func TestDeterministicRunner_UnrelatedIssueViolation_StillMerges(t *testing.T) {
	workdir := t.TempDir()
	if _, err := knowledge.Scaffold(workdir, 100, "Widget", nil); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(workdir, ".nightgauge", "knowledge", "features", "999-stale", "PRD.md")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("# Stale\n\nNo block.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	gh := &fakeGh{preMerge: cleanSnapshot(), postMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", workdir)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Errorf("an unrelated issue's stale entry blocked the merge: %q (%s)", res.Path, res.Reason)
	}
}

// TestDeterministicRunner_NoKnowledgeBase_Merges keeps the gate fail-open for
// repositories that do not use the knowledge base at all.
func TestDeterministicRunner_NoKnowledgeBase_Merges(t *testing.T) {
	gh := &fakeGh{preMerge: cleanSnapshot(), postMerge: PRViewSnapshot{State: "MERGED"}}
	r := newRunnerWith(gh, 42)

	res, err := r.Run(context.Background(), 100, "owner/repo", t.TempDir())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.Path != PathMerged {
		t.Errorf("Path = %q (%s), want merged", res.Path, res.Reason)
	}
}
