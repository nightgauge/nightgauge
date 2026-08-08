package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/orchestrator/gates"
	"github.com/nightgauge/nightgauge/internal/state"
)

// ConflictRecoveryLoop is the deterministic recovery action that turns an
// unresolvable rebase conflict at pr-merge into a feature-dev re-dispatch on the
// SAME branch, instead of the legacy blind fresh-branch restart that discarded
// all dev work (#4072, epic #4067).
//
// pr-merge (merge.md Step 6.1.5) captures the conflict into
// conflict-context-{N}.json (conflicting files + ours/theirs blobs) and writes a
// CONFLICT_RESOLUTION_NEEDED feedback signal targeting feature-dev. This action
// is the bridge that, on the pr-merge KindNoOp failure, ENSURES that signal is
// present in feedback-{N}.json and then returns Recovered=false +
// FollowUpStageCanResume so the scheduler's feedback-rewind plumbing
// (scheduler.go) rewinds the pipeline to feature-dev. The actual conflict
// resolution is the LLM dev stage's job — this action does NO LLM work and
// resolves no conflict itself, keeping Execute deterministic per
// docs/AUTO_TRIAGE.md's determinism invariant.
//
// Bound (two cooperating layers, both sized by max_dev_redispatch):
//
//   - In-memory edge count (RetryEngine.MaxConflictRedispatch) is the
//     AUTHORITATIVE termination bound. The scheduler counts pr-merge→feature-dev
//     rewinds and declines past the bound — reliable on every path, including the
//     skill-crash path, and cleared per run by RetryEngine.Reset.
//   - On-disk signal count (countConflictSignals over feedback-{N}.json) is the
//     PRIMARY escalation trigger on the NORMAL path: the pr-merge skill appends
//     one CONFLICT_RESOLUTION_NEEDED signal per distinct failure, so once the
//     count exceeds the bound this action escalates with the specific conflicting
//     files for human triage. On the skill-crash path only the de-duped fallback
//     writer runs, so this count stays pinned and the in-memory bound terminates
//     instead (the scheduler still surfaces the conflicting files from this
//     action's evidence). The two never under-count: whichever trips first stops
//     the loop at exactly max_dev_redispatch re-dispatches (#4072 review).
type ConflictRecoveryLoop struct {
	maxDevRedispatch int
}

// NewConflictRecoveryLoop builds the action with the configured re-dispatch
// bound. A non-positive bound falls back to the default.
func NewConflictRecoveryLoop(maxDevRedispatch int) *ConflictRecoveryLoop {
	if maxDevRedispatch <= 0 {
		maxDevRedispatch = DefaultConflictMaxDevRedispatch
	}
	return &ConflictRecoveryLoop{maxDevRedispatch: maxDevRedispatch}
}

// Name implements RecoveryAction.
func (a *ConflictRecoveryLoop) Name() string { return "conflict-recovery-loop" }

// CapExempt implements recovery.CapExempt: conflict re-dispatches are bounded
// independently by max_dev_redispatch (per-edge), so they neither draw from nor
// are gated by the global max_attempts_per_run pool (#4072 review).
func (a *ConflictRecoveryLoop) CapExempt() bool { return true }

// Description implements RecoveryAction.
func (a *ConflictRecoveryLoop) Description() string {
	return "pr-merge hit an unresolvable rebase conflict — re-dispatch feature-dev on the same branch (via feedback rewind) to resolve, preserving the branch; escalate with the specific files once max_dev_redispatch is exhausted."
}

// Matches implements RecoveryAction. Pure: inspects only typed fields. Fires on
// a pr-merge KindNoOp failure whose reason/evidence names a conflict and that
// carries a workspace (where conflict-context-{N}.json would live). The
// existence of the context file is checked in Execute, not here, to keep Matches
// IO-free (mirrors pipeline_heal_base.Matches).
//
// Registered BEFORE branch-out-of-date in Default() so a real conflict routes to
// dev re-dispatch ahead of the plain BEHIND/DIRTY rebase: a clean BEHIND is a
// fast-forward (branch-out-of-date rebases + merges), but a genuine content
// conflict needs the LLM dev stage.
func (a *ConflictRecoveryLoop) Matches(failure StageFailure) bool {
	if failure.Stage != state.StagePRMerge {
		return false
	}
	if failure.GateKind != gates.KindNoOp {
		return false
	}
	if failure.Workspace == "" {
		return false
	}
	if failure.IssueNumber == 0 {
		return false
	}
	return mentionsConflict(failure.Reason, failure.Evidence)
}

// mentionsConflict reports whether the reason or any evidence string names a
// merge/rebase conflict. A plain BEHIND/DIRTY merge state (no conflict token) is
// deliberately excluded — that routes to branch-out-of-date.
func mentionsConflict(reason string, evidence []string) bool {
	combined := strings.ToLower(reason + " " + strings.Join(evidence, " "))
	return strings.Contains(combined, "conflict") ||
		strings.Contains(combined, "conflicting")
}

// conflictContextFile is the on-disk shape of conflict-context-{N}.json. Only
// the fields this action reads are modeled; extra fields are tolerated.
type conflictContextFile struct {
	IssueNumber      int    `json:"issue_number"`
	PRNumber         int    `json:"pr_number"`
	Branch           string `json:"branch"`
	BaseRef          string `json:"base_ref"`
	ConflictingFiles []struct {
		Path string `json:"path"`
	} `json:"conflicting_files"`
}

// feedbackOnDisk is the read/write shape of feedback-{N}.json. It preserves
// unknown top-level fields and any sibling signals (feature-validate may have
// written this file too) — the new CONFLICT_RESOLUTION_NEEDED signal is MERGED
// in, never clobbering existing signals.
type feedbackOnDisk struct {
	SchemaVersion string                 `json:"schema_version,omitempty"`
	IssueNumber   int                    `json:"issue_number,omitempty"`
	Signals       []feedbackSignalOnDisk `json:"signals"`
	CreatedAt     string                 `json:"created_at,omitempty"`
}

type feedbackSignalOnDisk struct {
	SignalType           string   `json:"signal_type"`
	EmittedByStage       string   `json:"emitted_by_stage"`
	BacktrackTargetStage string   `json:"backtrack_target_stage,omitempty"`
	Rationale            string   `json:"rationale"`
	Evidence             []string `json:"evidence"`
	Severity             string   `json:"severity"`
}

const conflictResolutionSignalType = "CONFLICT_RESOLUTION_NEEDED"

// unknownBranch is the sentinel currentBranch returns when it cannot name the
// checked-out branch. It is a HARD STOP, never an acceptable value to record:
// feature-dev's conflict intake skips the branch checkout when the context says
// "unknown", so a context carrying it silently discards the same-branch
// guarantee the conflict-recovery loop exists to provide (#301).
const unknownBranch = "unknown"

// Execute implements RecoveryAction. Deterministic — emits/normalizes the
// feedback signal and defers to the scheduler's rewind. No LLM, no conflict
// resolution here.
func (a *ConflictRecoveryLoop) Execute(ctx context.Context, failure StageFailure) RecoveryResult {
	pipelineDir := filepath.Join(failure.Workspace, ".nightgauge", "pipeline")
	contextPath := filepath.Join(pipelineDir, fmt.Sprintf("conflict-context-%d.json", failure.IssueNumber))

	data, err := os.ReadFile(contextPath)
	if err != nil {
		// No conflict context — pr-merge could not capture the conflicting
		// files (e.g. rebase failed with no markers). A dev re-dispatch with no
		// context would be a useless spin, so escalate with the raw reason.
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json not found — cannot re-dispatch feature-dev with conflict context", failure.IssueNumber),
			Evidence: append([]string{fmt.Sprintf("pr=%d", failure.PRNumber)},
				fmt.Sprintf("missing=%s", contextPath)),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	var cc conflictContextFile
	if jerr := json.Unmarshal(data, &cc); jerr != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("conflict-context-%d.json is not valid JSON: %s", failure.IssueNumber, truncate(jerr.Error(), 200)),
			Evidence: []string{fmt.Sprintf("pr=%d", failure.PRNumber)},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	files := make([]string, 0, len(cc.ConflictingFiles))
	for _, f := range cc.ConflictingFiles {
		if f.Path != "" {
			files = append(files, f.Path)
		}
	}

	// A context naming zero files, or naming no resolvable branch, is not
	// actionable: feature-dev would be re-dispatched with nothing to resolve (and
	// with "unknown" it skips the branch checkout outright), spinning through the
	// whole max_dev_redispatch budget and terminating in triage anyway. Escalate
	// here instead — the reader enforces the same invariant the writer does, so a
	// degenerate context from ANY writer (including the pr-merge skill) stops at
	// one honest escalation rather than N useless dispatches (#301).
	if len(files) == 0 {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json names zero conflicting files — nothing for feature-dev to resolve", failure.IssueNumber),
			Evidence: []string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("branch=%s", cc.Branch),
				fmt.Sprintf("context=%s", contextPath),
			},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}
	if cc.Branch == "" || cc.Branch == unknownBranch {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json does not name a resolvable branch — feature-dev cannot check out %q to resolve the conflict", failure.IssueNumber, cc.Branch),
			Evidence: append([]string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("context=%s", contextPath),
			}, prefixed("conflicting_file=", files)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	feedbackPath := filepath.Join(pipelineDir, fmt.Sprintf("feedback-%d.json", failure.IssueNumber))

	// Ensure feedback-{N}.json carries the CONFLICT_RESOLUTION_NEEDED signal for
	// THIS conflict so (a) the scheduler's EvaluateBacktrack can rewind to
	// feature-dev and (b) the signal count below reflects this failure. pr-merge's
	// skill normally writes it; ensureFeedbackSignal is idempotent (it won't
	// double-append) and self-heals the case where the skill exited before the
	// write.
	if err := a.ensureFeedbackSignal(feedbackPath, failure.IssueNumber, cc, files); err != nil {
		return RecoveryResult{
			Action:   a.Name(),
			Reason:   fmt.Sprintf("failed to write conflict feedback signal: %s", truncate(err.Error(), 200)),
			Evidence: []string{fmt.Sprintf("pr=%d", failure.PRNumber)},
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// Normal-path escalation: the pr-merge skill appends one
	// CONFLICT_RESOLUTION_NEEDED signal per distinct conflict failure, so the
	// on-disk count is the number of failures observed for this issue (this one
	// included). Once it exceeds the bound, escalate to human triage naming the
	// specific files — a richer outcome than the in-memory bound's plain decline.
	// On the skill-crash path only the de-duped fallback writes, so this count
	// can stay pinned; the in-memory edge bound (RetryEngine) then terminates the
	// loop at the same max_dev_redispatch instead. Both stop at the bound.
	conflictCount := countConflictSignals(feedbackPath)
	if conflictCount > a.maxDevRedispatch {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict recovery exhausted: %d feature-dev re-dispatches (max %d) did not resolve the conflict on branch %q",
				a.maxDevRedispatch, a.maxDevRedispatch, cc.Branch),
			Evidence: append([]string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("branch=%s", cc.Branch),
				fmt.Sprintf("conflicts=%d", conflictCount),
				fmt.Sprintf("max_dev_redispatch=%d", a.maxDevRedispatch),
			}, prefixed("conflicting_file=", files)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// Recovered=false: the conflict is NOT resolved yet — the LLM dev stage does
	// that after the rewind. FollowUpStageCanResume tells the scheduler to honor
	// the CONFLICT_RESOLUTION_NEEDED feedback signal and rewind to feature-dev.
	// The RetryEngine treats that signal's edge with a per-edge COUNT limit
	// (max_dev_redispatch traversals) rather than the open-ended-ping-pong
	// oscillation block, so the loop re-dispatches feature-dev up to the bound and
	// then declines → terminal failure (#4072 review).
	return RecoveryResult{
		Recovered: false,
		Action:    a.Name(),
		Reason: fmt.Sprintf("re-dispatching feature-dev on branch %q to resolve %d conflicting file(s) (attempt %d/%d)",
			cc.Branch, len(files), conflictCount, a.maxDevRedispatch),
		Evidence: append([]string{
			fmt.Sprintf("pr=%d", failure.PRNumber),
			fmt.Sprintf("branch=%s", cc.Branch),
			fmt.Sprintf("base_ref=%s", cc.BaseRef),
			fmt.Sprintf("attempt=%d/%d", conflictCount, a.maxDevRedispatch),
		}, prefixed("conflicting_file=", files)...),
		FollowUp: FollowUpStageCanResume,
	}
}

// currentBranch returns the workspace's checked-out branch name, or
// unknownBranch when HEAD names no branch (detached, or mid-rebase — git
// detaches HEAD for a rebase's duration and answers the literal "HEAD").
//
// This answers "what branch is checked out", which is NOT the same question as
// "what branch is the rebase operating on" — see rebaseBranch. Callers that
// need the latter mid-rebase must not use this one (#301).
func currentBranch(ctx context.Context, workspace string) string {
	out, err := execGit(ctx, workspace, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return unknownBranch
	}
	b := strings.TrimSpace(string(out))
	if b == "" || b == "HEAD" {
		return unknownBranch
	}
	return b
}

// rebaseBranch returns the branch an in-progress rebase is replaying onto, or
// "" when there is none to name.
//
// git detaches HEAD while rebasing, so currentBranch cannot answer this; git
// instead records the original ref in rebase-merge/head-name (merge backend) or
// rebase-apply/head-name (am backend). Resolved via `rev-parse --git-path` so
// linked worktrees — which the pipeline uses for every issue — find their own
// rebase state rather than the main checkout's.
//
// Only a refs/heads/ value is accepted. When the rebase was started from a
// detached HEAD, git writes the literal string "detached HEAD" into that file;
// there genuinely is no branch then, and "" is the honest answer.
func rebaseBranch(ctx context.Context, workspace string) string {
	for _, rel := range []string{"rebase-merge/head-name", "rebase-apply/head-name"} {
		out, err := execGit(ctx, workspace, "rev-parse", "--git-path", rel)
		if err != nil {
			continue
		}
		path := strings.TrimSpace(string(out))
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		ref := strings.TrimSpace(string(data))
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		if b := strings.TrimPrefix(ref, "refs/heads/"); b != "" {
			return b
		}
	}
	return ""
}

// captureOutcome names WHICH of the three possible realities a conflict capture
// landed in. They are three distinct states and they get three distinct values:
// folding them into one empty-slice return is exactly the #301 defect (an empty
// capture was indistinguishable from a successful one, and the caller then ran
// the destructive `rebase --abort` and reported the run resumable).
type captureOutcome string

const (
	// captureFailed — the capture itself did not work: the unmerged-path probe
	// errored, the branch could not be named, the index blobs could not be read,
	// or the write failed. A conflict may well exist and is now recorded NOWHERE,
	// so the caller MUST NOT run `git rebase --abort`: the index is the only
	// surviving copy of the evidence.
	captureFailed captureOutcome = "failed"
	// captureNoConflictState — the probe succeeded and found zero unmerged paths.
	// The rebase failed for a reason that is not a content conflict (dirty index,
	// pre-existing rebase state, unborn base). There is nothing to capture and
	// nothing for feature-dev to resolve.
	captureNoConflictState captureOutcome = "no-conflict-state"
	// captureCaptured — at least one unmerged path, with index blobs, written to
	// conflict-context-{N}.json and signalled. The ONLY outcome that has earned a
	// feature-dev re-dispatch.
	captureCaptured captureOutcome = "captured"
)

// conflictCapture is the result of captureConflictContextFromIndex.
type conflictCapture struct {
	Outcome captureOutcome
	// Files is the set of unmerged paths the probe found. Populated on
	// captureCaptured, and also on captureFailed when the failure came AFTER the
	// probe (so the escalation can still name what was at stake).
	Files []string
	// Branch is the branch recorded in the context file. Set on captureCaptured.
	Branch string
	// Err explains a captureFailed. Nil otherwise.
	Err error
}

// captureConflictContextFromIndex snapshots the in-progress rebase conflict
// (conflicting files + ours/theirs blobs) into conflict-context-{N}.json and
// merges a CONFLICT_RESOLUTION_NEEDED feedback signal into feedback-{N}.json.
//
// It MUST be called while the conflict is still staged (after a failed
// `git rebase`, BEFORE `git rebase --abort`) — `git show :2:<path>` / `:3:<path>`
// only resolve the ours/theirs blobs while the conflict is in the index. This is
// the deterministic Go-side mirror of merge.md's capture_conflict_and_signal:
// branch-out-of-date calls it so a rebase conflict it cannot land defers to the
// conflict-recovery rewind instead of escalating immediately (#4072).
//
// INVARIANT (#301): it writes conflict-context-{N}.json and emits
// CONFLICT_RESOLUTION_NEEDED for captureCaptured and for NOTHING else. A failed
// or empty capture leaves no artifact at all — absence is already handled
// correctly by ConflictRecoveryLoop.Execute (missing context → human triage),
// whereas a "capture_failed: true" marker would only work for readers that
// remembered to check it, and the first reader that forgot would reintroduce
// this bug. Writing nothing also keeps the emitted document conformant with the
// published ConflictContextSchema, which requires conflicting_files to be
// non-empty.
//
// All shell-outs go through execGit so tests can stub them.
func captureConflictContextFromIndex(ctx context.Context, workspace string, issue, pr int, branch, baseRef string) conflictCapture {
	// Conflicting files via `git diff --name-only --diff-filter=U`. An ERROR here
	// is not "no conflicting files" — it means we do not know, which is a failed
	// capture, not an empty one.
	out, err := execGit(ctx, workspace, "diff", "--name-only", "--diff-filter=U")
	if err != nil {
		return conflictCapture{
			Outcome: captureFailed,
			Err:     fmt.Errorf("could not list unmerged paths: %w", err),
		}
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if f := strings.TrimSpace(line); f != "" {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return conflictCapture{Outcome: captureNoConflictState}
	}

	// A conflict exists but we cannot say which branch it is on. feature-dev's
	// intake refuses to check out the "unknown" sentinel, so a context carrying
	// it would be re-dispatched against the wrong tree (or no checkout at all).
	// Treat it as a failed capture so the conflicted index is preserved for
	// triage rather than aborted away behind a context nobody can act on.
	if branch == "" || branch == unknownBranch {
		return conflictCapture{
			Outcome: captureFailed,
			Files:   files,
			Err:     errors.New("could not resolve the branch under rebase"),
		}
	}

	// Build conflicting_files[] with ours/theirs blobs. Track whether ANY index
	// blob resolved: if every `git show :2:`/`:3:` fails, the index is already
	// gone and all we have is a list of names — not a capture.
	cf := make([]map[string]string, 0, len(files))
	blobsResolved := 0
	for _, f := range files {
		ours := ""
		theirs := ""
		if b, err := execGit(ctx, workspace, "show", ":2:"+f); err == nil {
			ours = string(b)
			blobsResolved++
		}
		if b, err := execGit(ctx, workspace, "show", ":3:"+f); err == nil {
			theirs = string(b)
			blobsResolved++
		}
		cf = append(cf, map[string]string{"path": f, "ours": ours, "theirs": theirs})
	}
	if blobsResolved == 0 {
		return conflictCapture{
			Outcome: captureFailed,
			Files:   files,
			Err:     errors.New("conflicting paths listed but no ours/theirs index blobs could be read"),
		}
	}

	if baseRef == "" {
		baseRef = "main"
	}

	contextDoc := map[string]interface{}{
		"schema_version":    "1.0",
		"issue_number":      issue,
		"pr_number":         pr,
		"branch":            branch,
		"base_ref":          baseRef,
		"conflicting_files": cf,
		"created_at":        time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(contextDoc, "", "  ")
	if err != nil {
		return conflictCapture{Outcome: captureFailed, Files: files, Err: fmt.Errorf("encode conflict context: %w", err)}
	}
	pipelineDir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		return conflictCapture{Outcome: captureFailed, Files: files, Err: fmt.Errorf("create pipeline dir: %w", err)}
	}
	contextPath := filepath.Join(pipelineDir, fmt.Sprintf("conflict-context-%d.json", issue))
	if err := os.WriteFile(contextPath, data, 0o644); err != nil {
		return conflictCapture{Outcome: captureFailed, Files: files, Err: fmt.Errorf("write conflict context: %w", err)}
	}

	// Merge the CONFLICT_RESOLUTION_NEEDED signal into feedback-{N}.json. Without
	// it the scheduler never rewinds, so a context file with no signal is a
	// half-written capture — remove it rather than leave a success-shaped artifact
	// behind a failed capture.
	cc := conflictContextFile{IssueNumber: issue, PRNumber: pr, Branch: branch, BaseRef: baseRef}
	loop := &ConflictRecoveryLoop{maxDevRedispatch: DefaultConflictMaxDevRedispatch}
	if err := loop.ensureFeedbackSignal(filepath.Join(pipelineDir, fmt.Sprintf("feedback-%d.json", issue)), issue, cc, files); err != nil {
		_ = os.Remove(contextPath)
		return conflictCapture{Outcome: captureFailed, Files: files, Err: fmt.Errorf("write conflict feedback signal: %w", err)}
	}

	return conflictCapture{Outcome: captureCaptured, Files: files, Branch: branch}
}

// countConflictSignals returns how many CONFLICT_RESOLUTION_NEEDED signals are
// already present in feedback-{N}.json. A missing/unparseable file counts as 0.
func countConflictSignals(feedbackPath string) int {
	data, err := os.ReadFile(feedbackPath)
	if err != nil {
		return 0
	}
	var fb feedbackOnDisk
	if json.Unmarshal(data, &fb) != nil {
		return 0
	}
	n := 0
	for _, s := range fb.Signals {
		if s.SignalType == conflictResolutionSignalType {
			n++
		}
	}
	return n
}

// ensureFeedbackSignal is the FALLBACK writer of the CONFLICT_RESOLUTION_NEEDED
// signal. The pr-merge skill (and the branch-out-of-date deferral) is the
// authoritative per-failure writer — it appends one signal per distinct conflict
// failure, which is what drives the bound count. So if a CONFLICT signal already
// exists, this function leaves the file UNCHANGED (it must not inflate the count
// by double-writing for the same failure). It only writes when NO conflict
// signal is present yet (e.g. the skill crashed before its write), preserving any
// existing non-conflict signals such as a concurrent feature-validate revision.
//
// files is always non-empty: both call sites reject a zero-file conflict before
// reaching here (#301), so there is no "signal with no evidence" case to paper
// over.
func (a *ConflictRecoveryLoop) ensureFeedbackSignal(feedbackPath string, issue int, cc conflictContextFile, files []string) error {
	evidence := files
	newSignal := feedbackSignalOnDisk{
		SignalType:           conflictResolutionSignalType,
		EmittedByStage:       "pr-merge",
		BacktrackTargetStage: "feature-dev",
		Rationale:            fmt.Sprintf("pr-merge rebase conflict on branch %s — re-dispatch feature-dev to resolve", cc.Branch),
		Evidence:             evidence,
		Severity:             "blocking",
	}

	var fb feedbackOnDisk
	if data, err := os.ReadFile(feedbackPath); err == nil {
		if json.Unmarshal(data, &fb) != nil {
			// Corrupt file — rebuild it rather than fail the rewind.
			fb = feedbackOnDisk{}
		}
		// Idempotency: if pr-merge already wrote a CONFLICT_RESOLUTION_NEEDED
		// signal, do not append a duplicate (it would inflate the attempt
		// count). The signal is already present, so the rewind will fire.
		for _, s := range fb.Signals {
			if s.SignalType == conflictResolutionSignalType {
				return nil
			}
		}
	}

	if fb.SchemaVersion == "" {
		fb.SchemaVersion = "1.1"
	}
	if fb.IssueNumber == 0 {
		fb.IssueNumber = issue
	}
	if fb.CreatedAt == "" {
		fb.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	fb.Signals = append(fb.Signals, newSignal)

	out, err := json.MarshalIndent(fb, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(feedbackPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(feedbackPath, out, 0o644)
}
