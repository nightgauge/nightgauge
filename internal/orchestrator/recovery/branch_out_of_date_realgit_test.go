package recovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// These tests run BranchOutOfDate.Execute against a REAL git repository built
// by scripts/capture-conflicted-rebase-fixture.sh — no execGit stub, no
// hand-authored conflict-context JSON. That matters for #301 specifically:
// both defects are consequences of what git actually does under a rebase, and
// both were invisible to the stubbed unit test because the stub answered
// `rev-parse --abbrev-ref HEAD` with a branch name, which real git never does
// mid-rebase (it detaches HEAD and answers "HEAD").
//
// Everything asserted here — the branch name, the conflicting path, the
// ours/theirs blobs, the "rebase refused, nothing conflicted" state — is
// produced by git at test time (#166 evidence rule).

const fixtureBranch = "feat/301-conflict-fixture"

// realGitFixture builds the fixture repo in mode and returns the working clone.
func realGitFixture(t *testing.T, mode string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "..", "scripts", "capture-conflicted-rebase-fixture.sh"))
	if err != nil {
		t.Fatalf("resolve fixture script: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "fixture")
	// bash is invoked with the script PATH plus positional args (no `-c` string),
	// so nothing here is interpreted as shell syntax; both args are test-local
	// constants regardless. gittest.Env() (rather than a plain exec.Command) so
	// every `git` invocation the script itself makes inherits the
	// background-maintenance disarming (#680) alongside the ambient-config
	// isolation the script already sets up on its own (#542).
	cmd := exec.Command("bash", script, dest, mode)
	cmd.Env = gittest.Env()
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("fixture script (%s) failed: %v\n%s", mode, err, stderr)
	}
	work := strings.TrimSpace(string(out))
	if work == "" {
		t.Fatalf("fixture script printed no workdir")
	}
	return work
}

func readConflictContext(t *testing.T, ws string, issue int) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(conflictContextPathForTest(ws, issue))
	if err != nil {
		t.Fatalf("read conflict context: %v", err)
	}
	var doc map[string]interface{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse conflict context: %v", err)
	}
	return doc
}

func conflictContextPathForTest(ws string, issue int) string {
	return filepath.Join(ws, ".nightgauge", "pipeline", "conflict-context-"+strconv.Itoa(issue)+".json")
}

func feedbackPathForTest(ws string, issue int) string {
	return filepath.Join(ws, ".nightgauge", "pipeline", "feedback-"+strconv.Itoa(issue)+".json")
}

// hasConflictSignal reports whether feedback-{issue}.json carries a
// CONFLICT_RESOLUTION_NEEDED signal. A missing file is "no signal".
func hasConflictSignal(t *testing.T, ws string, issue int) bool {
	t.Helper()
	data, err := os.ReadFile(feedbackPathForTest(ws, issue))
	if err != nil {
		return false
	}
	var fb feedbackOnDisk
	if err := json.Unmarshal(data, &fb); err != nil {
		t.Fatalf("parse feedback: %v", err)
	}
	for _, s := range fb.Signals {
		if s.SignalType == conflictResolutionSignalType {
			return true
		}
	}
	return false
}

// rebaseInProgress reports whether the workspace is sitting mid-rebase.
func rebaseInProgress(t *testing.T, ws string) bool {
	t.Helper()
	out, err := gittest.Command(ws, "rev-parse", "--git-path", "rebase-merge").Output()
	if err != nil {
		t.Fatalf("rev-parse --git-path: %v", err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(ws, p)
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

func prMergeConflictFailure(ws string, issue int) StageFailure {
	return StageFailure{
		Stage:       state.StagePRMerge,
		GateKind:    gates.KindNoOp,
		PRNumber:    1,
		IssueNumber: issue,
		Workspace:   ws,
		Reason:      "dirty-merge-state: BEHIND",
	}
}

// TestRealGit_RebaseConflict_RecordsRealBranch is the #301 defect-2 regression:
// git detaches HEAD for the duration of a rebase, so resolving the branch from
// inside the conflict handler reads "HEAD" and degrades to the "unknown"
// sentinel. feature-dev's conflict intake explicitly SKIPS the branch checkout
// when the context says "unknown", so an "unknown" context silently discards
// the same-branch guarantee the whole conflict-recovery loop exists to provide.
//
// The branch is knowable here — the fixture is on feat/301-conflict-fixture and
// git itself records refs/heads/feat/301-conflict-fixture in
// rebase-merge/head-name — so the context file must name it.
func TestRealGit_RebaseConflict_RecordsRealBranch(t *testing.T) {
	ws := realGitFixture(t, "conflict")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	doc := readConflictContext(t, ws, 301)
	if got := doc["branch"]; got != fixtureBranch {
		t.Errorf("conflict-context branch = %v, want %q (real branch under rebase)", got, fixtureBranch)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "branch="+fixtureBranch) {
		t.Errorf("evidence should name the real branch, got %v", res.Evidence)
	}
	if strings.Contains(res.Reason, unknownBranch) {
		t.Errorf("reason must not fall back to the %q sentinel: %q", unknownBranch, res.Reason)
	}
}

// TestRealGit_RebaseConflict_CapturesThenAborts locks the capture-before-destroy
// ordering against real git: the ours/theirs blobs only resolve while the
// conflict is in the index, so a genuine capture must read them BEFORE
// `git rebase --abort`. Asserting on the blobs' CONTENT (not merely that a file
// was written) is what proves the ordering held — post-abort those `git show
// :2:` / `:3:` lookups fail and the fields would be empty.
func TestRealGit_RebaseConflict_CapturesThenAborts(t *testing.T) {
	ws := realGitFixture(t, "conflict")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.Recovered {
		t.Error("a rebase conflict is not recovered in place — the dev stage resolves it")
	}
	if res.FollowUp != FollowUpStageCanResume {
		t.Errorf("FollowUp = %q, want %q on a captured conflict", res.FollowUp, FollowUpStageCanResume)
	}

	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	if len(rawFiles) != 1 {
		t.Fatalf("conflicting_files = %v, want exactly f.txt", doc["conflicting_files"])
	}
	entry, _ := rawFiles[0].(map[string]interface{})
	if entry["path"] != "f.txt" {
		t.Errorf("conflicting file path = %v, want f.txt", entry["path"])
	}
	// ours = the PR branch's work, theirs = the base. That is the contract every
	// consumer is written against (ConflictFileSchema, feature-dev's Step 0.7.1b,
	// merge.md), and under a REBASE it is the INVERSE of git's own stage names:
	// git checks out the upstream and replays the feature commit onto it, so
	// stage 2 (git's "ours") is origin/main and stage 3 (git's "theirs") is the
	// feature branch. Passing git's naming through handed feature-dev the base
	// under the field labelled "this PR's feature work" (#301 round-2 findings
	// 3/5).
	ours, _ := entry["ours"].(string)
	theirs, _ := entry["theirs"].(string)
	if !strings.Contains(ours, "feature-side") {
		t.Errorf("ours must be the PR branch's work; got %q", ours)
	}
	if !strings.Contains(theirs, "main-side") {
		t.Errorf("theirs must be the rebase base; got %q", theirs)
	}
	if doc["conflict_operation"] != "rebase" {
		t.Errorf("conflict_operation = %v, want \"rebase\" — the mapping above is only correct for a rebase", doc["conflict_operation"])
	}

	if !hasConflictSignal(t, ws, 301) {
		t.Error("a captured conflict must emit CONFLICT_RESOLUTION_NEEDED so the scheduler rewinds")
	}
	if rebaseInProgress(t, ws) {
		t.Error("a captured conflict must abort the rebase — the evidence is already on disk")
	}
}

// TestRealGit_RebaseRefused_NoConflictState is the #301 defect-1 regression on
// its most reachable route: the rebase fails for a reason that is NOT a content
// conflict (a staged, uncommitted change makes git refuse before it starts).
// The unmerged-path probe then SUCCEEDS and reports zero files — an empty
// capture that is indistinguishable from a successful one on unfixed code.
//
// There is nothing for feature-dev to resolve, so this must not write a
// conflict context and must not emit CONFLICT_RESOLUTION_NEEDED: doing so burns
// the whole max_dev_redispatch budget re-running the dev stage against a
// context naming zero files.
func TestRealGit_RebaseRefused_NoConflictState(t *testing.T) {
	ws := realGitFixture(t, "dirty-index")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if _, err := os.Stat(conflictContextPathForTest(ws, 301)); err == nil {
		data, _ := os.ReadFile(conflictContextPathForTest(ws, 301))
		t.Errorf("a rebase that conflicted with nothing must not write a conflict context; got:\n%s", data)
	}
	if hasConflictSignal(t, ws, 301) {
		t.Error("no conflict happened — CONFLICT_RESOLUTION_NEEDED must not be emitted")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q (nothing to re-dispatch feature-dev for)", res.FollowUp, FollowUpHumanTriageRequired)
	}
	if res.Recovered {
		t.Error("expected Recovered=false")
	}
}

// TestRealGit_DetachedHead_BranchGenuinelyUnknown pins the ONE state where the
// branch is legitimately undeterminable: the workspace is on a detached HEAD, so
// there is no branch, and git's own rebase-merge/head-name says the literal
// "detached HEAD" rather than a refs/heads/ ref.
//
// A conflict context naming "unknown" is not actionable — feature-dev refuses to
// check the branch out — so this is a FAILED capture, not a successful one: no
// context file, no rewind signal, human triage.
//
// The conflict is still not thrown away: the raw index stages are copied out to
// conflict-evidence-{N}/ FIRST, and only then does the rebase abort. Leaving the
// worktree mid-rebase instead does not preserve anything in this system — the
// scheduler's terminal defer reads the `UU` paths as uncommitted work and
// `git add -A`s the stages away moments later (#301 review).
func TestRealGit_DetachedHead_BranchGenuinelyUnknown(t *testing.T) {
	ws := realGitFixture(t, "detached")

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if _, err := os.Stat(conflictContextPathForTest(ws, 301)); err == nil {
		data, _ := os.ReadFile(conflictContextPathForTest(ws, 301))
		t.Errorf("no branch is resolvable — must not write a context feature-dev cannot act on; got:\n%s", data)
	}
	if hasConflictSignal(t, ws, 301) {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	entries := readEvidenceManifest(t, ws, 301)
	if len(entries) == 0 {
		t.Fatal("the raw index must be preserved before the abort — nothing was written")
	}
	if rebaseInProgress(t, ws) {
		t.Error("with the evidence preserved the abort is non-destructive and must run, leaving a usable worktree")
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "evidence_preserved=true") {
		t.Errorf("evidence must point the operator at the preserved dump, got %v", res.Evidence)
	}
}

// evidenceStage locates one preserved index stage in the evidence manifest and
// returns the raw bytes that were written for it.
func evidenceStage(t *testing.T, ws string, issue int, path string, stage int) []byte {
	t.Helper()
	for _, entry := range readEvidenceManifest(t, ws, issue) {
		if entry.Path != path {
			continue
		}
		for _, st := range entry.Stages {
			if st.Stage != stage {
				continue
			}
			data, err := os.ReadFile(filepath.Join(ws, ".nightgauge", "pipeline",
				"conflict-evidence-"+strconv.Itoa(issue), filepath.FromSlash(st.File)))
			if err != nil {
				t.Fatalf("read preserved stage %d blob for %s: %v", stage, path, err)
			}
			return data
		}
		t.Fatalf("stage %d not preserved for %s (stages: %+v)", stage, path, entry.Stages)
	}
	t.Fatalf("path %q missing from the evidence manifest", path)
	return nil
}

type evidenceEntry struct {
	Path   string `json:"path"`
	Stages []struct {
		Stage   int    `json:"stage"`
		Mode    string `json:"mode"`
		SHA     string `json:"sha"`
		Bytes   int64  `json:"bytes"`
		File    string `json:"file"`
		Gitlink bool   `json:"gitlink"`
	} `json:"stages"`
}

func readEvidenceManifest(t *testing.T, ws string, issue int) []evidenceEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(ws, ".nightgauge", "pipeline",
		"conflict-evidence-"+strconv.Itoa(issue), "manifest.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Entries []evidenceEntry `json:"entries"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse evidence manifest: %v", err)
	}
	return doc.Entries
}

// gitShow reads a committed blob. Used to derive what the :2:/:3: index stages
// MUST contain without hand-authoring a single byte: on a one-commit rebase,
// stage 2 is the feature branch's version of the file and stage 3 is
// origin/main's, so git itself supplies both expected values.
func gitShow(t *testing.T, ws, rev, path string) []byte {
	t.Helper()
	out, err := gittest.Command(ws, "show", rev+":"+path).Output()
	if err != nil {
		t.Fatalf("git show %s:%s: %v", rev, path, err)
	}
	return out
}

// TestRealGit_MultiFileConflict_NonASCIIPathCaptured is the #301-review
// regression for the enumeration bug. `git diff --name-only --diff-filter=U`
// C-QUOTES any path with a non-ASCII byte: a genuine conflict in `café.txt`
// comes back as the literal 12-character string `"caf\303\251.txt"`. Feeding
// that to `git show :2:<path>` fails ("does not exist (neither on disk nor in
// the index)"), so the entry was written with the mangled path and BOTH SIDES
// EMPTY — and because the sibling `f.txt` read fine, the global blob counter was
// non-zero and the whole capture still reported `capture=captured`, aborted the
// rebase, and told the scheduler the stage could resume.
//
// Both paths must arrive intact with both sides populated, or the capture must
// not claim success. Only real git produces the quoting, which is why this
// cannot be stubbed.
func TestRealGit_MultiFileConflict_NonASCIIPathCaptured(t *testing.T) {
	ws := realGitFixture(t, "unicode-path")
	const unicodePath = "café.txt"

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("FollowUp = %q, want %q — both paths are ordinary text conflicts: %s", res.FollowUp, FollowUpStageCanResume, res.Reason)
	}

	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	byPath := map[string]map[string]interface{}{}
	for _, rf := range rawFiles {
		entry, _ := rf.(map[string]interface{})
		p, _ := entry["path"].(string)
		byPath[p] = entry
	}
	if len(byPath) != 2 {
		t.Fatalf("conflicting_files = %v, want exactly f.txt and %q", doc["conflicting_files"], unicodePath)
	}
	// ours = the PR branch's work, theirs = the base — the consumer contract, not
	// git's stage naming. Under a rebase that means stage 3 and stage 2
	// respectively; see TestRealGit_RebaseConflict_CapturesThenAborts.
	for _, want := range []string{"f.txt", unicodePath} {
		entry, ok := byPath[want]
		if !ok {
			t.Fatalf("path %q missing from the capture (got %v) — C-quoted?", want, byPath)
		}
		ours, _ := entry["ours"].(string)
		theirs, _ := entry["theirs"].(string)
		if !strings.Contains(ours, "feature-side") {
			t.Errorf("%s ours = %q, want the PR branch's version", want, ours)
		}
		if !strings.Contains(theirs, "main-side") {
			t.Errorf("%s theirs = %q, want the rebase base's version", want, theirs)
		}
		if entry["ours_present"] != true || entry["theirs_present"] != true {
			t.Errorf("%s: both sides exist in the index, so neither empty-side flag may be false: %v", want, entry)
		}
		if entry["ours_mode"] != "100644" || entry["theirs_mode"] != "100644" {
			t.Errorf("%s: both sides are ordinary files, so both modes must be 100644: %v", want, entry)
		}
	}
	if !hasConflictSignal(t, ws, 301) {
		t.Error("a captured conflict must emit CONFLICT_RESOLUTION_NEEDED")
	}
	if rebaseInProgress(t, ws) {
		t.Error("a captured conflict must abort the rebase")
	}
}

// TestRealGit_BinaryConflict_NotCapturedAsSuccess is the #301-review regression
// for the corruption bug. `ours = string(blobBytes)` followed by
// json.MarshalIndent replaces every invalid UTF-8 byte with U+FFFD, so a binary
// conflict was stored as something that does not equal the blob (a 256-byte
// input round-trips to ~512 bytes of replacement runes) — and the result still
// read `capture=captured`, still aborted, and still told the scheduler the stage
// could resume. The bytes were then unrecoverable.
//
// Bytes that cannot survive JSON must not be reported as captured. They must
// still SURVIVE: the raw stages go to conflict-evidence-{N}/ byte-for-byte, and
// the expected bytes here come from `git show` on the two commits — git supplies
// both sides, nothing is hand-authored.
func TestRealGit_BinaryConflict_NotCapturedAsSuccess(t *testing.T) {
	ws := realGitFixture(t, "binary")
	const binPath = "bin.dat"

	// The evidence dump is a RAW index dump and keys by git's own stage numbers,
	// so the expectation here is stated in git's terms: under a rebase stage 2
	// holds origin/main's bytes and stage 3 the feature branch's. (The CONTEXT
	// document is the one that translates those into ours/theirs — see
	// TestRealGit_RebaseConflict_CapturesThenAborts.) Both expected values come
	// straight out of git.
	wantStage2 := gitShow(t, ws, "origin/main", binPath)
	wantStage3 := gitShow(t, ws, fixtureBranch, binPath)
	if utf8.Valid(wantStage2) || utf8.Valid(wantStage3) {
		t.Fatalf("fixture is not exercising the binary class: stage2 valid=%v stage3 valid=%v",
			utf8.Valid(wantStage2), utf8.Valid(wantStage3))
	}

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q — a binary conflict is not something feature-dev can be re-dispatched to resolve",
			res.FollowUp, FollowUpHumanTriageRequired)
	}
	if _, err := os.Stat(conflictContextPathForTest(ws, 301)); err == nil {
		data, _ := os.ReadFile(conflictContextPathForTest(ws, 301))
		t.Errorf("bytes that cannot round-trip through JSON must not be written as a capture; got:\n%s", data)
	}
	if hasConflictSignal(t, ws, 301) {
		t.Error("a failed capture must not emit CONFLICT_RESOLUTION_NEEDED")
	}

	// The whole capture fails, not just the binary path: a partial capture would
	// re-dispatch feature-dev against a conflict set that is missing a file.
	joined := strings.Join(res.Evidence, " ")
	if !strings.Contains(joined, "conflicting_file="+binPath) || !strings.Contains(joined, "conflicting_file=f.txt") {
		t.Errorf("escalation must name every conflicting path, got %v", res.Evidence)
	}

	if got := evidenceStage(t, ws, 301, binPath, 2); !bytes.Equal(got, wantStage2) {
		t.Errorf("preserved stage-2 blob is not byte-identical: got %d bytes, want %d", len(got), len(wantStage2))
	}
	if got := evidenceStage(t, ws, 301, binPath, 3); !bytes.Equal(got, wantStage3) {
		t.Errorf("preserved stage-3 blob is not byte-identical: got %d bytes, want %d", len(got), len(wantStage3))
	}
	if rebaseInProgress(t, ws) {
		t.Error("the bytes are preserved verbatim on disk, so the abort is safe and must run")
	}
}

// TestRealGit_GitlinkConflict_CapturedAsMetadata is the #301 round-2 finding-1/6
// regression: a SUBMODULE POINTER conflict must capture, and must not wedge the
// worktree.
//
// `git ls-files -u` lists a conflicted gitlink as mode 160000 whose stage object
// ids are COMMITs in the submodule's own store. The previous code parsed the
// mode field and threw it away, so `git cat-file blob <commit-id>` exited 128 —
// which (a) made the path uncapturable and failed the whole capture, and then
// (b) failed the raw evidence dump for the same reason, leaving EvidenceDir
// empty. With no evidence anywhere, BranchOutOfDate deliberately skipped
// `git rebase --abort` — so an ordinary pointer conflict in any repo with
// submodules left a detached worktree with an unmerged index that nothing in the
// pipeline reclaims (the sweep skips detached, ClassifyStatus calls `UU`
// blocking, and every later attempt hits the pre-existing-rebase guard forever).
//
// A gitlink's content IS its commit id, so it captures as metadata: no blob read
// anywhere, both sides empty with ours_commit/theirs_commit naming the two
// commits, and the sibling text conflict captured normally alongside it.
func TestRealGit_GitlinkConflict_CapturedAsMetadata(t *testing.T) {
	ws := realGitFixture(t, "gitlink")
	const subPath = "sub"

	// The two commit ids come from git's own trees, not from the fixture script's
	// variables. Under a rebase the PR branch's side becomes index stage 3 and the
	// base's stage 2, so this is also the expectation for the ours/theirs mapping.
	oursMode, wantOurs := treeEntry(t, ws, fixtureBranch, subPath)
	theirsMode, wantTheirs := treeEntry(t, ws, "origin/main", subPath)
	if oursMode != "160000" || theirsMode != "160000" {
		t.Fatalf("fixture precondition: %s must be a gitlink on both sides, got %q/%q", subPath, oursMode, theirsMode)
	}
	if wantOurs == "" || wantOurs == wantTheirs {
		t.Fatalf("fixture precondition: the two sides must point at different commits, got %q vs %q", wantOurs, wantTheirs)
	}

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("FollowUp = %q, want %q — a submodule pointer conflict is capturable: %s", res.FollowUp, FollowUpStageCanResume, res.Reason)
	}
	if rebaseInProgress(t, ws) {
		t.Error("the capture succeeded, so the abort must run — a wedged mid-rebase worktree is never reclaimed")
	}

	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	byPath := map[string]map[string]interface{}{}
	for _, rf := range rawFiles {
		entry, _ := rf.(map[string]interface{})
		p, _ := entry["path"].(string)
		byPath[p] = entry
	}
	if len(byPath) != 2 {
		t.Fatalf("conflicting_files = %v, want f.txt and %q", doc["conflicting_files"], subPath)
	}
	sub, ok := byPath[subPath]
	if !ok {
		t.Fatalf("%q missing from the capture: %v", subPath, byPath)
	}
	if sub["ours_mode"] != "160000" || sub["theirs_mode"] != "160000" {
		t.Errorf("%s: modes must be carried through so a reader can tell this is metadata-only: %v", subPath, sub)
	}
	if sub["ours"] != "" || sub["theirs"] != "" {
		t.Errorf("%s: a gitlink has no blob bytes to inline: %v", subPath, sub)
	}
	if sub["ours_commit"] != wantOurs {
		t.Errorf("%s ours_commit = %v, want %q (the PR branch's submodule commit)", subPath, sub["ours_commit"], wantOurs)
	}
	if sub["theirs_commit"] != wantTheirs {
		t.Errorf("%s theirs_commit = %v, want %q (the base's submodule commit)", subPath, sub["theirs_commit"], wantTheirs)
	}
	// The sibling text conflict is unaffected.
	if txt := byPath["f.txt"]; txt == nil || !strings.Contains(txt["ours"].(string), "feature-side") {
		t.Errorf("f.txt must still capture normally alongside the gitlink: %v", byPath["f.txt"])
	}
	if !hasConflictSignal(t, ws, 301) {
		t.Error("a captured conflict must emit CONFLICT_RESOLUTION_NEEDED")
	}
}

// TestRealGit_GitlinkConflict_EvidenceDumpSurvives covers the OTHER half of the
// same root cause: preserveConflictEvidence ran the identical `cat-file blob` on
// the gitlink stages, so the dump failed too — after having already streamed
// some blobs to disk, leaving orphan files (one of them zero-byte) and NO
// manifest.json, which readEvidenceManifest cannot interpret at all.
//
// Driven through the capture-failed path by giving the capture no resolvable
// branch, so the gitlink reaches the dump rather than the context.
func TestRealGit_GitlinkConflict_EvidenceDumpSurvives(t *testing.T) {
	ws := realGitFixture(t, "gitlink")
	const subPath = "sub"

	startRebase(t, ws)
	cap := captureConflictContextFromIndex(context.Background(), ws, 301, 7, unknownBranch, "main")

	if cap.Outcome != captureFailed {
		t.Fatalf("precondition: an unresolvable branch is a failed capture, got %q", cap.Outcome)
	}
	if cap.EvidenceDir == "" {
		t.Fatalf("the raw index must be preserved even when a stage is a gitlink: %v", cap.Err)
	}
	entries := readEvidenceManifest(t, ws, 301)
	var sub *evidenceEntry
	for i := range entries {
		if entries[i].Path == subPath {
			sub = &entries[i]
		}
	}
	if sub == nil {
		t.Fatalf("manifest must record the gitlink path; entries=%+v", entries)
	}
	for _, st := range sub.Stages {
		if st.Mode != "160000" {
			t.Errorf("stage %d mode = %q, want 160000", st.Stage, st.Mode)
		}
		if !st.Gitlink {
			t.Errorf("stage %d must be flagged gitlink so an operator knows the sha is a commit, not a blob: %+v", st.Stage, st)
		}
		if st.File != "" {
			t.Errorf("stage %d must not claim a dumped blob file (%q) — nothing is readable for a gitlink", st.Stage, st.File)
		}
	}
	// The text sibling's bytes still land on disk, so the dump is a real dump.
	if got := evidenceStage(t, ws, 301, "f.txt", 2); len(got) == 0 {
		t.Error("f.txt's stage-2 blob must still be preserved byte-for-byte")
	}
}

// TestRealGit_ModeOnlyConflict_SurvivesItsOwnReader is the #301 round-4b
// regression for the ONE real conflict whose faithful record is byte-identical
// to a failed blob read: an empty placeholder (`.gitkeep`, `__init__.py`,
// `py.typed`) added on both sides with different exec bits. git stages it as
// `100644 e69de29 2` / `100755 e69de29 3` — two PRESENT blob sides, both
// legitimately empty, the conflict being the mode.
//
// The writer records that correctly. The reader's `unexplainedEmpty` guard then
// rejected it, because it excused an all-empty entry only via a non-blob mode or
// a `*_present:false` flag and never considered the modes DISAGREEING. By the
// time the reader ran, `branch-out-of-date` had already aborted the rebase on
// the captured path (which writes no evidence dump, the context being the
// durable copy) — so the escalation said the conflict "was never recorded"
// about a document that recorded it, with the index already gone. On main the
// same conflict got a context and a feature-dev re-dispatch.
//
// Writer and reader are asserted together on purpose: this defect existed only
// in the seam between them, and each half is correct in isolation.
func TestRealGit_ModeOnlyConflict_SurvivesItsOwnReader(t *testing.T) {
	ws := realGitFixture(t, "mode-only")
	const placeholder = "n.txt"

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))
	if res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("FollowUp = %q, want %q — a mode conflict on an empty file is resolvable: %s",
			res.FollowUp, FollowUpStageCanResume, res.Reason)
	}

	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	var entry map[string]interface{}
	for _, rf := range rawFiles {
		e, _ := rf.(map[string]interface{})
		if e["path"] == placeholder {
			entry = e
		}
	}
	if entry == nil {
		t.Fatalf("%q missing from the capture: %v", placeholder, doc["conflicting_files"])
	}
	// The record git's own index dictates: both sides present, both empty, modes
	// differing. Nothing here is hand-authored — see the fixture's mode-only mode.
	if entry["ours"] != "" || entry["theirs"] != "" {
		t.Fatalf("fixture is not exercising the mode-only class; both sides must be empty: %v", entry)
	}
	if entry["ours_present"] != true || entry["theirs_present"] != true {
		t.Errorf("both stages exist in the index, so neither presence flag may be false: %v", entry)
	}
	if entry["ours_mode"] == entry["theirs_mode"] {
		t.Fatalf("fixture is not exercising the mode-only class; the modes must differ: %v", entry)
	}

	// The reader must accept the document the writer just produced. This is the
	// half that failed: "carries 1 of 1 entries with both sides empty and nothing
	// explaining why — those conflicts were never recorded".
	loop := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
	if loop.FollowUp != FollowUpStageCanResume {
		t.Fatalf("the reader rejected a faithful capture (index already aborted, nothing left to triage from): FollowUp = %q, reason = %s",
			loop.FollowUp, loop.Reason)
	}
	if rebaseInProgress(t, ws) {
		t.Error("a captured conflict must abort the rebase")
	}
}

// TestRealGit_SymlinkConflict_ReadsAsBlob is the control for the gitlink fix: a
// SYMLINK is index mode 120000 and is a perfectly ordinary blob whose bytes are
// the target path. It must keep being read and inlined — the fix must key on
// "is this a blob", not on "is this mode unusual".
func TestRealGit_SymlinkConflict_ReadsAsBlob(t *testing.T) {
	ws := realGitFixture(t, "symlink")
	const linkPath = "link"

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if res.FollowUp != FollowUpStageCanResume {
		t.Fatalf("FollowUp = %q, want %q — a symlink conflict is ordinary blob content: %s", res.FollowUp, FollowUpStageCanResume, res.Reason)
	}
	doc := readConflictContext(t, ws, 301)
	rawFiles, _ := doc["conflicting_files"].([]interface{})
	for _, rf := range rawFiles {
		entry, _ := rf.(map[string]interface{})
		if entry["path"] != linkPath {
			continue
		}
		if entry["ours_mode"] != "120000" {
			t.Errorf("%s ours_mode = %v, want 120000", linkPath, entry["ours_mode"])
		}
		if ours, _ := entry["ours"].(string); ours != "target-feature-side" {
			t.Errorf("%s ours = %q, want the PR branch's link target inlined verbatim", linkPath, ours)
		}
		if theirs, _ := entry["theirs"].(string); theirs != "target-main-side" {
			t.Errorf("%s theirs = %q, want the base's link target inlined verbatim", linkPath, theirs)
		}
		return
	}
	t.Fatalf("%q missing from the capture: %v", linkPath, doc["conflicting_files"])
}

// treeEntry reads one path's mode and object id out of a committed tree via
// `git ls-tree`, so the expected values in the gitlink test are git's own rather
// than the fixture script's local variables. Output shape:
// "<mode> <type> <oid>\t<path>".
func treeEntry(t *testing.T, ws, rev, path string) (mode, oid string) {
	t.Helper()
	out, err := gittest.Command(ws, "ls-tree", rev, "--", path).Output()
	if err != nil {
		t.Fatalf("git ls-tree %s -- %s: %v", rev, path, err)
	}
	meta, p, ok := strings.Cut(strings.TrimRight(string(out), "\n"), "\t")
	if !ok || p != path {
		t.Fatalf("git ls-tree %s -- %s produced no entry for that path: %q", rev, path, out)
	}
	f := strings.Fields(meta)
	if len(f) != 3 {
		t.Fatalf("unparseable ls-tree metadata %q", meta)
	}
	return f[0], f[2]
}

// invalidUTF8UnmergedPaths returns the DISTINCT unmerged paths whose NAMES are
// not valid UTF-8, read out of git's own index with `ls-files -u -z` — the same
// raw-bytes enumeration the capture uses. It exists so a test asserts the class
// it is exercising from git rather than from the fixture script's variables: a
// name that reached the test as valid UTF-8 would mean the fixture stopped
// reproducing #301 and the assertions below became vacuous.
func invalidUTF8UnmergedPaths(t *testing.T, ws string) []string {
	t.Helper()
	out, err := gittest.Command(ws, "ls-files", "-u", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files -u -z: %v", err)
	}
	seen := map[string]bool{}
	var paths []string
	for _, rec := range bytes.Split(out, []byte{0}) {
		_, path, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			continue
		}
		p := string(path)
		if utf8.ValidString(p) || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

// startRebase runs the fetch+rebase BranchOutOfDate would run, so a test can
// call the capture directly against a genuinely conflicted index.
func startRebase(t *testing.T, ws string) {
	t.Helper()
	if out, err := gittest.Command(ws, "fetch", "origin", "main").CombinedOutput(); err != nil {
		t.Fatalf("git fetch: %v\n%s", err, out)
	}
	// A conflicting rebase exits non-zero; that is the point.
	_ = gittest.Command(ws, "rebase", "origin/main").Run()
	if !rebaseInProgress(t, ws) {
		t.Fatal("precondition: the rebase must be paused on a conflict")
	}
}

// runSkillCapture executes the pr-merge skill's capture_conflict_and_signal
// helper VERBATIM out of skills/nightgauge-pr-merge/_includes/merge.md against a
// real conflicted index.
//
// The function body is extracted from the shipped markdown at test time rather
// than copied here, so the test cannot pass against a skill that has drifted —
// which is the whole point: the reader's guard is only as good as its agreement
// with the writer that actually runs on the normal pr-merge path.
// extraEnv entries are appended last, so a "PATH=…" there wins (os/exec keeps
// the last value for a duplicated key).
func runSkillCapture(t *testing.T, ws string, issue, pr int, reason string, withHeadRef bool, extraEnv ...string) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH — the skill capture is a jq pipeline")
	}
	fn := skillShellFragment(t, mergeIncludePath(t), "capture_conflict_and_signal() {", "\n}\n")

	script := filepath.Join(t.TempDir(), "capture.sh")
	if err := os.WriteFile(script, []byte("set -u\n"+fn+"\ncapture_conflict_and_signal \"$CCS_REASON\"\n"), 0o644); err != nil {
		t.Fatalf("write capture script: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = ws
	// gittest.Env() rather than os.Environ() so the skill fragment's own git
	// calls inherit the same disarming/isolation every fixture in this
	// package uses (#680, #542) — it already covers the two GIT_CONFIG_*
	// overrides this call used to set by hand.
	cmd.Env = append(gittest.Env(),
		"ISSUE_NUMBER="+strconv.Itoa(issue),
		"PR_NUMBER="+strconv.Itoa(pr),
		"BASE_REF=main",
		"CCS_REASON="+reason,
	)
	if withHeadRef {
		cmd.Env = append(cmd.Env, "HEAD_REF="+fixtureBranch)
	}
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("skill capture failed: %v\n%s", err, out)
	}
	t.Logf("skill capture said: %s", strings.TrimSpace(string(out)))
}

func mergeIncludePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "..", "..", "skills", "nightgauge-pr-merge", "_includes", "merge.md"))
	if err != nil {
		t.Fatalf("resolve merge.md: %v", err)
	}
	return p
}

// skillShellFragment lifts a shell fragment out of a shipped skill markdown by
// its first line and its terminator, VERBATIM. Extracting at test time rather
// than copying the shell in here is the point: a test that carries its own copy
// of the code passes against a skill that has drifted.
func skillShellFragment(t *testing.T, src, start, end string) string {
	t.Helper()
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	i := strings.Index(string(body), start)
	if i < 0 {
		t.Fatalf("%q not found in %s", start, src)
	}
	rest := string(body)[i:]
	j := strings.Index(rest, end)
	if j < 0 {
		t.Fatalf("%q has no %q terminator in %s", start, end, src)
	}
	return rest[:j+len(end)]
}

// pathWithoutIconv builds a PATH containing everything the capture helper
// shells out to EXCEPT iconv, by symlinking each tool into a fresh directory.
//
// It exists because the helper's UTF-8 guard used to be `command -v iconv && …`,
// which silently skipped itself — and shipped a U+FFFD-corrupted capture as a
// success — on any host without iconv (alpine/musl images ship none). Inheriting
// the developer's PATH, where iconv exists, is precisely why the binary-conflict
// test could not catch that.
func pathWithoutIconv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Every external command in capture_conflict_and_signal. printf/echo/[ are
	// bash builtins and need no entry.
	for _, tool := range []string{"git", "jq", "mktemp", "date", "cat", "sed", "head", "mkdir", "rm", "mv"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on PATH — cannot build an iconv-free environment for the capture", tool)
		}
		if err := os.Symlink(p, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	// Assert the precondition rather than assume it: if iconv were reachable the
	// test would silently stop testing anything.
	if _, err := exec.LookPath("iconv"); err == nil {
		if _, err := os.Stat(filepath.Join(dir, "iconv")); err == nil {
			t.Fatalf("the iconv-free PATH contains iconv")
		}
	}
	return dir
}

// TestRealGit_SkillWriter_ReadByRecoveryLoop closes the #301 round-2 finding-2/4
// loop end to end: the SKILL's shell capture — the writer that runs on the
// normal MERGEABLE=CONFLICTING pr-merge path, and the one the reader hardening
// claimed to cover but did not — writes the document, and
// ConflictRecoveryLoop.Execute reads exactly that document.
//
// Two halves, both against real git:
//
//   - a healthy multi-file conflict including a non-ASCII path must be read as
//     actionable, with the path intact. The old writer enumerated with
//     `git diff --name-only --diff-filter=U`, which C-quotes `café.txt` into the
//     literal `"caf\303\251.txt"`; `git show ":2:<that>"` then failed and the
//     entry landed with both sides empty NEXT TO a healthy f.txt — a document
//     the reader accepted and spent the whole max_dev_redispatch budget on.
//   - a conflict the writer could not record faithfully (binary content) must
//     be escalated rather than resolved against.
func TestRealGit_SkillWriter_ReadByRecoveryLoop(t *testing.T) {
	t.Run("captured conflict is actionable and keeps the non-ASCII path", func(t *testing.T) {
		ws := realGitFixture(t, "unicode-path")
		startRebase(t, ws)
		// No HEAD_REF: this also exercises the head-name branch fallback, since
		// git has HEAD detached for the rebase's duration.
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", false)

		doc := readConflictContext(t, ws, 301)
		if doc["branch"] != fixtureBranch {
			t.Errorf("branch = %v, want %q resolved from rebase-merge/head-name", doc["branch"], fixtureBranch)
		}
		if doc["capture_failed"] != false {
			t.Errorf("capture_failed = %v, want false — every path read cleanly", doc["capture_failed"])
		}

		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpStageCanResume {
			t.Fatalf("FollowUp = %q, want %q: %s", res.FollowUp, FollowUpStageCanResume, res.Reason)
		}
		joined := strings.Join(res.Evidence, " ")
		if !strings.Contains(joined, "conflicting_file=café.txt") {
			t.Errorf("the non-ASCII path must survive to the reader intact, got %v", res.Evidence)
		}
	})

	t.Run("unrecordable conflict escalates instead of burning the budget", func(t *testing.T) {
		ws := realGitFixture(t, "binary")
		startRebase(t, ws)
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", true)

		doc := readConflictContext(t, ws, 301)
		if doc["capture_failed"] != true {
			t.Fatalf("capture_failed = %v, want true — bin.dat cannot round-trip through JSON", doc["capture_failed"])
		}

		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpHumanTriageRequired {
			t.Fatalf("FollowUp = %q, want %q — the writer said it could not record this: %s",
				res.FollowUp, FollowUpHumanTriageRequired, res.Reason)
		}
		if !strings.Contains(res.Reason, "capture_failed") {
			t.Errorf("the escalation must name the marker it acted on: %q", res.Reason)
		}
	})

	// #301 round-4b. The subtest above inherits the developer's PATH, where
	// iconv exists — so it passed while the guard it was checking was
	// `command -v iconv && ! iconv …`, a check that skips ITSELF wherever iconv
	// is not installed. On such a host the binary blob went straight to
	// `jq --rawfile`, which substitutes U+FFFD for every invalid byte, and the
	// document came back `capture_failed: false` with mojibake in `ours`; the
	// reader then returned "stage can resume" and feature-dev resolved the
	// conflict against fabricated bytes before `rebase --abort` destroyed the
	// originals. The verdict must not depend on which optional tools a runner
	// happens to have.
	// The shell writer records a mode-only conflict the same way the Go writer
	// does — two present blob sides, both empty, modes differing — so the
	// reader's mode-only clause has to hold for BOTH writers or the pr-merge
	// path escalates a faithful capture after its index is already gone.
	t.Run("mode-only conflict written by the skill is actionable", func(t *testing.T) {
		ws := realGitFixture(t, "mode-only")
		startRebase(t, ws)
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", true)

		doc := readConflictContext(t, ws, 301)
		if doc["capture_failed"] != false {
			t.Fatalf("capture_failed = %v, want false — every path read cleanly", doc["capture_failed"])
		}
		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpStageCanResume {
			t.Fatalf("FollowUp = %q, want %q: %s\nentries: %v",
				res.FollowUp, FollowUpStageCanResume, res.Reason, doc["conflicting_files"])
		}
	})

	// #301 round-5. A path NAME is bytes, not text: `caf\351.txt` is a legal
	// filename on ext4/xfs and a legal index entry everywhere, and `jq --arg`
	// rewrites every invalid byte to U+FFFD at exit 0. The writer therefore
	// shipped a SUCCESSFUL capture of a path nothing can open — #301's own
	// failure class — and, worse, two DISTINCT invalid names became the same
	// U+FFFD string, so `group_by(.path)` merged them and one conflicting file
	// disappeared from the document with capture_failed:false. The Go writer has
	// guarded this since round 2 (utf8.ValidString); until now the skill writer,
	// which is the one that runs on the ordinary MERGEABLE=CONFLICTING route,
	// did not — while docs/FEEDBACK_LOOPS.md asserted the rule for both.
	t.Run("a path name JSON cannot represent fails the capture", func(t *testing.T) {
		ws := realGitFixture(t, "latin1-path")
		// The one mode that arrives already paused: such a name cannot be
		// created in a worktree on macOS at all, so it exists only in the index.
		if !rebaseInProgress(t, ws) {
			t.Fatal("fixture precondition: the rebase must already be paused")
		}
		// Take the class from git's own index, never from the fixture's
		// variables: two unmerged paths whose names are not valid UTF-8, and
		// they must be different names.
		invalid := invalidUTF8UnmergedPaths(t, ws)
		if len(invalid) != 2 {
			t.Fatalf("fixture is not exercising the class: %d unmerged paths with a non-UTF-8 name, want 2", len(invalid))
		}
		if invalid[0] == invalid[1] {
			t.Fatalf("fixture must use two DISTINCT invalid names, got %q twice", invalid[0])
		}
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", true)

		doc := readConflictContext(t, ws, 301)
		if doc["capture_failed"] != true {
			t.Fatalf("capture_failed = %v, want true — %q cannot survive JSON; entries: %v",
				doc["capture_failed"], invalid[0], doc["conflicting_files"])
		}
		entries, _ := doc["conflicting_files"].([]interface{})
		var mangled, healthy int
		for _, raw := range entries {
			entry, _ := raw.(map[string]interface{})
			path, _ := entry["path"].(string)
			if !strings.ContainsRune(path, utf8.RuneError) {
				healthy++
				continue
			}
			mangled++
			if ce, _ := entry["capture_error"].(string); !strings.Contains(ce, "not valid UTF-8") {
				t.Errorf("entry %q capture_error = %q, want the path-name reason", path, ce)
			}
		}
		// Two entries, not one: the collapse is the half of this defect that
		// silently DROPS a conflicting file rather than mangling it.
		if mangled != 2 {
			t.Errorf("got %d unrepresentable entries, want 2 (two distinct names must not collapse into one); entries: %v",
				mangled, doc["conflicting_files"])
		}
		if healthy != 1 {
			t.Errorf("got %d healthy entries, want 1 (f.txt must still be recorded); entries: %v",
				healthy, doc["conflicting_files"])
		}
		if why, _ := doc["capture_error"].(string); !strings.Contains(why, "2 conflicting path name(s)") {
			t.Errorf("document capture_error = %q, want it to name how many paths were unrepresentable", why)
		}

		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpHumanTriageRequired {
			t.Fatalf("FollowUp = %q, want %q — feature-dev must not be sent after a path nothing can open: %s",
				res.FollowUp, FollowUpHumanTriageRequired, res.Reason)
		}
		if !strings.Contains(res.Reason, "capture_failed") {
			t.Errorf("the escalation must name the marker it acted on: %q", res.Reason)
		}
	})

	// #301 round-5. The writer resolved `git worktree list | head -1`, which is
	// ALWAYS the main worktree, while every reader resolves the STAGE worktree:
	// ConflictRecoveryLoop reads <Workspace>/.nightgauge/pipeline with Workspace
	// = the run's worktree (#275), and feature-dev's intake and this skill's own
	// context-bootstrap use the relative path from the same cwd. On a
	// worktree-isolated run — the pipeline's normal mode — a perfectly faithful
	// capture was therefore written where nothing looks, and every skill-captured
	// conflict escalated "conflict-context-{N}.json not found".
	t.Run("the capture lands in the worktree the reader resolves", func(t *testing.T) {
		ws := realGitFixture(t, "linked-worktree")
		// The fixture prints the STAGE worktree: <main>/.nightgauge/worktrees/issue-301.
		mainRoot := filepath.Dir(filepath.Dir(filepath.Dir(ws)))
		if _, err := os.Stat(filepath.Join(mainRoot, ".git")); err != nil {
			t.Fatalf("fixture precondition: %s must be the main worktree: %v", mainRoot, err)
		}
		startRebase(t, ws)
		// No HEAD_REF either: rebase-merge lives in the per-worktree git dir, so
		// this also proves the branch fallback resolves from a linked worktree.
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", false)

		for _, stray := range []string{conflictContextPathForTest(mainRoot, 301), feedbackPathForTest(mainRoot, 301)} {
			if _, err := os.Stat(stray); err == nil {
				t.Errorf("the capture escaped into the MAIN worktree: %s", stray)
			}
		}
		// Fatals if the writer put the document anywhere else.
		doc := readConflictContext(t, ws, 301)
		if doc["capture_failed"] != false {
			t.Fatalf("capture_failed = %v, want false — the conflict is an ordinary one: %v", doc["capture_failed"], doc["capture_error"])
		}
		if doc["branch"] != fixtureBranch {
			t.Errorf("branch = %v, want %q resolved from the linked worktree's rebase-merge/head-name", doc["branch"], fixtureBranch)
		}
		if !hasConflictSignal(t, ws, 301) {
			t.Errorf("the CONFLICT_RESOLUTION_NEEDED signal must land in the stage worktree too — feature-dev reads it from there")
		}

		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpStageCanResume {
			t.Fatalf("FollowUp = %q, want %q: %s", res.FollowUp, FollowUpStageCanResume, res.Reason)
		}
		if !strings.Contains(strings.Join(res.Evidence, " "), "conflicting_file=f.txt") {
			t.Errorf("the reader must see the captured path, got %v", res.Evidence)
		}
	})

	t.Run("unrecordable conflict escalates on a host without iconv", func(t *testing.T) {
		ws := realGitFixture(t, "binary")
		startRebase(t, ws)
		runSkillCapture(t, ws, 301, 7, "rebase --continue failed after partial resolution", true,
			"PATH="+pathWithoutIconv(t))

		doc := readConflictContext(t, ws, 301)
		if doc["capture_failed"] != true {
			t.Fatalf("capture_failed = %v, want true even with no iconv on PATH; entries: %v",
				doc["capture_failed"], doc["conflicting_files"])
		}
		res := (&ConflictRecoveryLoop{maxDevRedispatch: 3}).Execute(context.Background(), prMergeConflictFailure(ws, 301))
		if res.FollowUp != FollowUpHumanTriageRequired {
			t.Fatalf("FollowUp = %q, want %q: %s", res.FollowUp, FollowUpHumanTriageRequired, res.Reason)
		}
	})
}

// TestSkillWriter_ConflictEnumerationIsShellPortable runs the conflict
// enumeration loops shipped in skills/nightgauge-pr-merge/_includes/merge.md and
// skills/_shared/FRESHNESS_CHECK.md under BOTH bash and zsh, against a real
// two-file conflict.
//
// The loops assigned into `CONFLICT_FILES[$CONFLICT_COUNT]` starting at index 0.
// zsh arrays are 1-indexed and abort on that with "assignment to invalid
// subscript range", so under zsh the enumeration produced nothing — and the
// caller then reported "Rebase failed but no conflict markers found" for a
// conflict that demonstrably exists, aborted the rebase and exited 1. The agent
// shell is not guaranteed to be bash (#301 round-4b).
func TestSkillWriter_ConflictEnumerationIsShellPortable(t *testing.T) {
	shells := []string{"bash"}
	if _, err := exec.LookPath("zsh"); err == nil {
		shells = append(shells, "zsh")
	} else {
		t.Log("zsh not installed — only bash is covered on this host")
	}

	sharedInclude, err := filepath.Abs(filepath.Join("..", "..", "..", "skills", "_shared", "FRESHNESS_CHECK.md"))
	if err != nil {
		t.Fatalf("resolve FRESHNESS_CHECK.md: %v", err)
	}
	sources := map[string]string{
		"merge.md":           mergeIncludePath(t),
		"FRESHNESS_CHECK.md": sharedInclude,
	}

	ws := realGitFixture(t, "unicode-path")
	startRebase(t, ws)

	for name, src := range sources {
		// Verbatim from the shipped markdown: the array reset through the count.
		loop := skillShellFragment(t, src, "CONFLICT_FILES=()", "CONFLICT_COUNT=${#CONFLICT_FILES[@]}\n")
		script := filepath.Join(t.TempDir(), "enumerate.sh")
		if err := os.WriteFile(script, []byte(loop+"printf '%s\\n' \"$CONFLICT_COUNT\"\nprintf '%s\\n' \"${CONFLICT_FILES[@]}\"\n"), 0o644); err != nil {
			t.Fatalf("write enumeration script: %v", err)
		}
		for _, sh := range shells {
			t.Run(name+"/"+sh, func(t *testing.T) {
				cmd := exec.Command(sh, script)
				cmd.Dir = ws
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%s could not run the shipped enumeration: %v\n%s", sh, err, out)
				}
				got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
				want := []string{"2", "café.txt", "f.txt"}
				if len(got) != len(want) {
					t.Fatalf("%s enumerated %v, want %v", sh, got, want)
				}
				for i := range want {
					if got[i] != want[i] {
						t.Errorf("%s line %d = %q, want %q (full: %v)", sh, i, got[i], want[i], got)
					}
				}
			})
		}
	}
}

// TestRealGit_RebaseBranch_ResolvesHeadName covers rebaseBranch returning a REAL
// branch. Every other test resolves the branch before the steps loop with HEAD
// still attached, so the fallback only ever ran in the detached case where it
// correctly returns "" — leaving its `rev-parse --git-path` + os.ReadFile
// plumbing, the sole protection for a worktree that is ALREADY detached when
// Execute starts, entirely unexercised (#301 round-2 advisory).
func TestRealGit_RebaseBranch_ResolvesHeadName(t *testing.T) {
	ws := realGitFixture(t, "conflict")
	startRebase(t, ws)

	if got := currentBranch(context.Background(), ws); got != unknownBranch {
		t.Fatalf("precondition: git detaches HEAD during a rebase, so currentBranch must degrade to %q, got %q", unknownBranch, got)
	}
	if got := rebaseBranch(context.Background(), ws); got != fixtureBranch {
		t.Errorf("rebaseBranch = %q, want %q from rebase-merge/head-name", got, fixtureBranch)
	}
}

// TestRealGit_PreexistingRebase_OperatorWorkSurvives is the #301-review
// regression for the destructive `rebase --abort` in the no-conflict-state
// branch, which the code called a "harmless no-op". It is not harmless for the
// very case the comment itself listed — a pre-existing rebase.
//
// Handed a worktree parked in `git rebase -i` at an `edit` step with staged
// work, the action must touch nothing: `git rebase origin/main` fails with "there
// is already a rebase-merge directory", the unmerged-path probe finds zero
// conflicts, and the old code then aborted the operator's rebase and deleted
// their staged file while reporting only "exit status 128".
func TestRealGit_PreexistingRebase_OperatorWorkSurvives(t *testing.T) {
	ws := realGitFixture(t, "operator-rebase")

	if !rebaseInProgress(t, ws) {
		t.Fatal("fixture precondition: the worktree must start mid-rebase")
	}
	before := gitStatus(t, ws)
	if !strings.Contains(before, "wip.txt") {
		t.Fatalf("fixture precondition: staged operator work must be present, got %q", before)
	}

	a := NewBranchOutOfDate(&fakePRMergeRunner{})
	res := a.Execute(context.Background(), prMergeConflictFailure(ws, 301))

	if !rebaseInProgress(t, ws) {
		t.Error("the pipeline aborted a rebase it did not start")
	}
	if after := gitStatus(t, ws); after != before {
		t.Errorf("the operator's in-progress work was destroyed:\nbefore: %q\nafter:  %q", before, after)
	}
	if res.FollowUp != FollowUpHumanTriageRequired {
		t.Errorf("FollowUp = %q, want %q", res.FollowUp, FollowUpHumanTriageRequired)
	}
	if !strings.Contains(strings.Join(res.Evidence, " "), "preexisting_rebase=true") {
		t.Errorf("the escalation must say a rebase was already in progress, got %v (reason: %s)", res.Evidence, res.Reason)
	}
}

func gitStatus(t *testing.T, ws string) string {
	t.Helper()
	out, err := gittest.Command(ws, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	return string(out)
}
