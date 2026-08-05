package reclaim

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These drive real git. The defect being fixed is state that only exists in a
// repository — a stash list — and every assertion here is on that state rather
// than on a returned error, because an error-only assertion passes against the
// bug (it also passed against #296, #298 and #323).

type stashRepo struct {
	t   *testing.T
	dir string
}

func newStashRepo(t *testing.T) *stashRepo {
	t.Helper()
	dir := t.TempDir()
	r := &stashRepo{t: t, dir: dir}
	r.git("init", "-b", "main")
	r.git("config", "user.email", "test@test")
	r.git("config", "user.name", "test")
	r.write("tracked.txt", "base\n")
	r.git("add", ".")
	r.git("commit", "-m", "initial")
	return r
}

func (r *stashRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *stashRepo) write(name, content string) {
	r.t.Helper()
	full := filepath.Join(r.dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
}

// stashWork dirties the tree and stashes it under the given message,
// reproducing what a stage does before running a baseline comparison.
func (r *stashRepo) stashWork(content, message string) {
	r.t.Helper()
	r.write("tracked.txt", content)
	r.git("stash", "push", "-m", message)
}

func (r *stashRepo) stashList() string {
	r.t.Helper()
	return r.git("stash", "list")
}

// #330 AC5. A stage killed mid-run never reaches its `git stash pop`, so the
// stash outlives it with no owner and no expiry. The assertion is on the stash
// LIST — the thing that actually accumulated for five months across three
// repos — not on the sweep's return value.
func TestSweepPipelineStashes_KilledStageLeavesNoUnownedStash(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("work the stage moved aside\n", StashName(StashBaseline, 692, "feature-validate"))

	if !strings.Contains(r.stashList(), StashMarker) {
		t.Fatalf("fixture did not create a pipeline stash: %q", r.stashList())
	}

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 {
		t.Fatalf("the leaked stash was not reclaimed: reclaimed=%+v skipped=%+v", res.Reclaimed, res.Skipped)
	}
	if got := r.stashList(); strings.TrimSpace(got) != "" {
		t.Errorf("a pipeline stash survived the sweep: %q", got)
	}
	// Restore, not discard: the content is the whole reason the leak matters.
	// dashboard's leaked `feature-validate-692-baseline` held the entire #692
	// deliverable, and a sweep that dropped it would have destroyed the thing
	// it was cleaning up after.
	body, err := os.ReadFile(filepath.Join(r.dir, "tracked.txt"))
	if err != nil {
		t.Fatalf("read restored file: %v", err)
	}
	if string(body) != "work the stage moved aside\n" {
		t.Errorf("the stashed work was not restored to the tree; got %q", body)
	}
}

// The determined=false contract from #323, applied to stashes. An operator's
// own stash and a pre-marker pipeline stash are indistinguishable, so a sweep
// that guessed would eventually destroy someone's work.
func TestSweepPipelineStashes_NeverTouchesAnUnownedStash(t *testing.T) {
	r := newStashRepo(t)
	for _, msg := range []string{
		"wip before the refactor",                   // an operator's own
		"feature-validate-692-baseline",             // the pre-#330 convention
		"temp-stash-unrelated-changes-before-pr-36", // observed in the audit
		"lint-staged automatic backup",              // a hook's
		"nightgauge:baseline",                       // marker present, fields missing
		"nightgauge:baseline:notanumber:stage",      // marker present, issue unparseable
		"nightgauge:baseline:0:stage",               // marker present, issue not positive
	} {
		r.stashWork("content for "+msg+"\n", msg)
	}
	before := r.stashList()

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("a stash with unprovable ownership was reclaimed: %+v", res.Reclaimed)
	}
	if got := r.stashList(); got != before {
		t.Errorf("the stash stack changed:\nbefore:\n%s\nafter:\n%s", before, got)
	}
	for _, s := range res.Skipped {
		if s.Reason != StashSkipUnowned {
			t.Errorf("skip reason for %s = %q, want %q", s.Ref, s.Reason, StashSkipUnowned)
		}
	}
}

// Refs renumber on every removal: dropping stash@{1} makes stash@{2} become
// stash@{1}. A sweep iterating a captured list acts on the wrong stash from the
// second removal onward — and with a mix of owned and unowned entries, "the
// wrong stash" is the operator's.
func TestSweepPipelineStashes_ReclaimsInterleavedStashesWithoutRenumberingOntoAnOperators(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("pipeline A\n", StashName(StashBaseline, 101, "feature-validate"))
	r.stashWork("operator work\n", "my own wip")
	r.stashWork("pipeline B\n", StashName(StashBaseline, 102, "auto-fix"))

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir, Action: StashDrop})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 2 {
		t.Fatalf("expected both pipeline stashes reclaimed, got %+v (skipped %+v)", res.Reclaimed, res.Skipped)
	}
	got := r.stashList()
	if strings.Contains(got, StashMarker) {
		t.Errorf("a pipeline stash survived: %q", got)
	}
	if !strings.Contains(got, "my own wip") {
		t.Fatalf("the operator's stash was consumed by ref renumbering: %q", got)
	}
	if show := r.git("stash", "show", "--name-only", "stash@{0}"); !strings.Contains(show, "tracked.txt") {
		t.Errorf("the surviving stash is not the operator's content: %q", show)
	}
}

func TestSweepPipelineStashes_ScopesToOneIssue(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("for 101\n", StashName(StashBaseline, 101, "feature-validate"))
	r.stashWork("for 102\n", StashName(StashBaseline, 102, "feature-validate"))

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir, Issue: 102, Action: StashDrop})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 || res.Reclaimed[0].Issue != 102 {
		t.Fatalf("scoped sweep reclaimed the wrong set: %+v", res.Reclaimed)
	}
	if got := r.stashList(); !strings.Contains(got, ":101:") {
		t.Errorf("another issue's stash was reclaimed: %q", got)
	}
}

// Restoring on top of uncommitted work is how a pop turns into a conflict the
// caller cannot resolve. The stash stays and the report says why.
func TestSweepPipelineStashes_RefusesToRestoreOntoADirtyTree(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("stashed\n", StashName(StashBaseline, 101, "feature-validate"))
	r.write("tracked.txt", "uncommitted work in the tree\n")

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 0 {
		t.Fatalf("restored onto a dirty tree: %+v", res.Reclaimed)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != StashSkipDirtyTree {
		t.Fatalf("skip = %+v, want one %q", res.Skipped, StashSkipDirtyTree)
	}
	body, _ := os.ReadFile(filepath.Join(r.dir, "tracked.txt"))
	if string(body) != "uncommitted work in the tree\n" {
		t.Errorf("the tree's own work was overwritten: %q", body)
	}
}

// Pipeline exhaust is not "uncommitted work". Letting a scaffolded knowledge
// README block the restore is the same mistake `worktree sweep` made (#332),
// reached through a different door.
func TestSweepPipelineStashes_ExhaustDoesNotBlockRestore(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("stashed\n", StashName(StashBaseline, 101, "feature-validate"))
	r.write(".nightgauge/knowledge/README.md", "# Knowledge Base\n")

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 {
		t.Fatalf("pipeline exhaust blocked the restore: reclaimed=%+v skipped=%+v", res.Reclaimed, res.Skipped)
	}
	if got := r.stashList(); strings.TrimSpace(got) != "" {
		t.Errorf("stash survived: %q", got)
	}
}

func TestSweepPipelineStashes_DryRunTouchesNothing(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("stashed\n", StashName(StashBaseline, 101, "feature-validate"))
	before := r.stashList()

	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir, DryRun: true})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(res.Reclaimed) != 1 {
		t.Fatalf("dry run must still classify: %+v", res.Reclaimed)
	}
	if got := r.stashList(); got != before {
		t.Errorf("dry run mutated the stash stack:\nbefore %q\nafter %q", before, got)
	}
}

func TestSweepPipelineStashes_EmptyStackIsNotAnError(t *testing.T) {
	r := newStashRepo(t)
	res, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: r.dir})
	if err != nil {
		t.Fatalf("an empty stash stack is nothing to be wrong about: %v", err)
	}
	if res.Scanned != 0 || len(res.Reclaimed) != 0 || len(res.Skipped) != 0 {
		t.Errorf("unexpected result over an empty stack: %+v", res)
	}
}

// "I could not look" must never read as "there are none" (#296, #323). A repo
// root that is not a git repository has to surface as an error, or a caller
// reports a clean stash stack it never read.
func TestSweepPipelineStashes_UnreadableRepoIsAnError(t *testing.T) {
	if _, err := SweepPipelineStashes(StashSweepOptions{RepoRoot: t.TempDir()}); err == nil {
		t.Fatal("expected an error for a directory that is not a git repository")
	}
	if _, err := SweepPipelineStashes(StashSweepOptions{}); err == nil {
		t.Fatal("expected an error when no repo root is supplied")
	}
}

func TestListStashes_ReportsAgeAndOwnership(t *testing.T) {
	r := newStashRepo(t)
	r.stashWork("stashed\n", StashName(StashWIPPreserve, 496, "sibling-sync"))

	entries, err := ListStashes(r.dir)
	if err != nil {
		t.Fatalf("ListStashes: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want 1", entries)
	}
	e := entries[0]
	if !e.Owned || e.Issue != 496 || e.Purpose != StashWIPPreserve || e.Stage != "sibling-sync" {
		t.Errorf("classification = %+v, want owned baseline for #496", e)
	}
	if e.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero — age reporting is what makes a five-month-old leak legible")
	}
	// An audit found stashes up to five months old. Age has to be derived from
	// the stash commit, not from when the sweep happened to run.
	if age := e.Age(e.CreatedAt.Add(72 * time.Hour)); age != 72*time.Hour {
		t.Errorf("Age = %v, want 72h", age)
	}
}

func TestParseStashMessage(t *testing.T) {
	tests := []struct {
		name    string
		message string
		wantOK  bool
		issue   int
		stage   string
	}{
		{
			name: "git's own 'On <branch>:' prefix does not defeat the marker",
			// The marker is searched for, not anchored: the branch name is not
			// ours to predict, and git renders every stash this way.
			message: "On feat/330-stashes: nightgauge:baseline:330:feature-validate",
			wantOK:  true, issue: 330, stage: "feature-validate",
		},
		{
			name:    "WIP-on form",
			message: "WIP on main: nightgauge:wip-preserve:496:sibling-sync",
			wantOK:  true, issue: 496, stage: "sibling-sync",
		},
		{name: "no marker", message: "On main: wip before refactor"},
		{name: "too few fields", message: "nightgauge:baseline:330"},
		{name: "too many fields", message: "nightgauge:baseline:330:stage:extra"},
		{name: "empty purpose", message: "nightgauge::330:stage"},
		{name: "empty stage", message: "nightgauge:baseline:330:"},
		{name: "negative issue", message: "nightgauge:baseline:-1:stage"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, issue, stage, ok := ParseStashMessage(tc.message)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && (issue != tc.issue || stage != tc.stage) {
				t.Errorf("issue/stage = %d/%q, want %d/%q", issue, stage, tc.issue, tc.stage)
			}
		})
	}
}

func TestStashName_RoundTrips(t *testing.T) {
	name := StashName(StashBaseline, 289, "feature-validate")
	if !strings.HasPrefix(name, StashMarker) {
		t.Fatalf("%q does not carry the greppable marker", name)
	}
	purpose, issue, stage, ok := ParseStashMessage("On main: " + name)
	if !ok || purpose != StashBaseline || issue != 289 || stage != "feature-validate" {
		t.Errorf("round trip lost information: %v %v %d %q", ok, purpose, issue, stage)
	}
	// An empty stage must not produce a 2-field name that fails to parse.
	if _, _, _, ok := ParseStashMessage(StashName(StashBaseline, 1, "")); !ok {
		t.Error("a stash written with an unknown stage must still be reclaimable")
	}
}
