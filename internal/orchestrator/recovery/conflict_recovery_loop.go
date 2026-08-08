package recovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	IssueNumber int    `json:"issue_number"`
	PRNumber    int    `json:"pr_number"`
	Branch      string `json:"branch"`
	BaseRef     string `json:"base_ref"`
	// CaptureFailed is the writer's own admission that it could not record the
	// conflict faithfully. The shell writer in
	// skills/nightgauge-pr-merge/_includes/merge.md sets it whenever a blob read
	// or the enumeration failed, so a partial document is never mistaken for a
	// complete one (#301 round-2 finding 2/4). The Go writer never emits a
	// failed capture at all — it writes nothing — so this is only ever true for
	// a skill-written document.
	CaptureFailed    bool                   `json:"capture_failed"`
	Operation        string                 `json:"conflict_operation"`
	ConflictingFiles []conflictContextEntry `json:"conflicting_files"`
}

// conflictContextEntry is one conflicting_files[] element as a READER sees it.
// The presence flags are pointers so "the writer said false" is distinguishable
// from "the writer did not say" — that difference is what lets the reader tell a
// legitimately-empty side from a silently-failed blob read.
type conflictContextEntry struct {
	Path          string `json:"path"`
	Ours          string `json:"ours"`
	Theirs        string `json:"theirs"`
	OursPresent   *bool  `json:"ours_present"`
	TheirsPresent *bool  `json:"theirs_present"`
	OursMode      string `json:"ours_mode"`
	TheirsMode    string `json:"theirs_mode"`
	// CaptureError is the skill writer's per-path diagnosis of why THIS entry
	// could not be recorded (the document-level capture_failed says only that
	// something failed). A reader must refuse such an entry outright — and
	// separately from unexplainedEmpty, which would call the same entry "never
	// recorded" and throw away the one field saying what actually went wrong.
	CaptureError string `json:"capture_error"`
}

// unexplainedEmpty reports whether BOTH sides of this entry are empty with
// nothing in the entry accounting for it — the exact shape a writer produces
// when its blob reads failed and it substituted "" (#301 round-2 finding 4).
//
// Four things legitimately produce an empty side, and all four are visible in
// the document:
//
//   - a non-blob mode (160000, a submodule pointer): the side's "content" IS a
//     commit id, recorded in ours_commit/theirs_commit, so there are no bytes to
//     inline and empty is the correct encoding;
//   - a side the index does not carry at all (delete/delete, modify/delete),
//     which the writer states via ours_present/theirs_present = false;
//   - a MODE-ONLY conflict: an empty placeholder (`.gitkeep`, `__init__.py`,
//     `py.typed`) added on both sides with different exec bits stages as
//     `100644 e69de29 2` / `100755 e69de29 3`. Both sides are present, both are
//     genuinely empty, and the disagreement IS the conflict — the differing
//     modes are what explains the emptiness (#301 round-4b). Content-identical
//     sides with the SAME mode are never an unmerged path, so differing modes
//     are the whole legitimate population here and a same-mode all-empty entry
//     stays rejected;
//   - genuinely empty file content on one side, in which case the OTHER side is
//     non-empty and this predicate is false anyway.
//
// Anything else — `{"path":"x","ours":"","theirs":""}` with no metadata — is the
// legacy shell writer's silent failure, and re-dispatching feature-dev against
// it burns the whole max_dev_redispatch budget resolving nothing.
func (e conflictContextEntry) unexplainedEmpty() bool {
	if e.Ours != "" || e.Theirs != "" {
		return false
	}
	if e.OursMode != "" && !isBlobMode(e.OursMode) {
		return false
	}
	if e.TheirsMode != "" && !isBlobMode(e.TheirsMode) {
		return false
	}
	if e.OursMode != "" && e.TheirsMode != "" && e.OursMode != e.TheirsMode {
		return false
	}
	if e.OursPresent != nil && !*e.OursPresent {
		return false
	}
	if e.TheirsPresent != nil && !*e.TheirsPresent {
		return false
	}
	return true
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
	var degenerate []string
	var unrecordable []string
	for _, f := range cc.ConflictingFiles {
		if f.Path == "" {
			continue
		}
		files = append(files, f.Path)
		// A path the writer diagnosed is reported by its diagnosis, not as
		// "never recorded" — the two escalations carry different information
		// and only one of them names the cause (#301 round-4b).
		if f.CaptureError != "" {
			unrecordable = append(unrecordable, fmt.Sprintf("%s: %s", f.Path, truncate(f.CaptureError, 200)))
			continue
		}
		// A path carrying U+FFFD is a path JSON could not represent. Every JSON
		// encoder — encoding/json, jq — substitutes exactly that rune for an
		// invalid byte, so the name in the document is not the name in the index
		// and nothing can open it. Both writers refuse such a path at the source
		// (the Go one with utf8.Valid, the skill one by re-hashing what jq gives
		// back); this is the reader's independent half, so the invariant holds
		// for ANY writer (#301 round-5). A file genuinely named with U+FFFD is
		// indistinguishable from the mangled case from here, and escalating it
		// costs one human triage — re-dispatching feature-dev against a
		// fabricated name costs the whole budget AND the conflicted index, which
		// `git rebase --abort` has already destroyed by now.
		if strings.ContainsRune(f.Path, utf8.RuneError) {
			unrecordable = append(unrecordable, fmt.Sprintf("%s: path name contains U+FFFD — a name JSON could not represent, so it names no file", f.Path))
			continue
		}
		if f.unexplainedEmpty() {
			degenerate = append(degenerate, f.Path)
		}
	}

	// The writer said outright that it could not record this conflict. Believe
	// it: the document is partial by its author's own admission, so anything
	// feature-dev resolved from it would be resolved against a fiction (#301
	// round-2 findings 2/4).
	if cc.CaptureFailed {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json is marked capture_failed — the writer could not record the conflict, so there is nothing sound to re-dispatch feature-dev against", failure.IssueNumber),
			Evidence: append(append([]string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("branch=%s", cc.Branch),
				"capture_failed=true",
				fmt.Sprintf("context=%s", contextPath),
			}, prefixed("capture_error=", unrecordable)...),
				prefixed("conflicting_file=", files)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// A per-entry capture_error without the document-level marker: the writer
	// diagnosed a path it could not record but did not raise capture_failed.
	// The skill writer always raises both, so this is the reader's independent
	// half of the contract — a writer that names the reason must not have that
	// reason silently discarded (#301 round-4b).
	if len(unrecordable) > 0 {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json carries %d of %d entries the writer marked capture_error — those paths were not recorded",
				failure.IssueNumber, len(unrecordable), len(files)),
			Evidence: append(append([]string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("branch=%s", cc.Branch),
				fmt.Sprintf("context=%s", contextPath),
			}, prefixed("capture_error=", unrecordable)...),
				prefixed("conflicting_file=", files)...),
			FollowUp: FollowUpHumanTriageRequired,
		}
	}

	// A context naming zero files, or naming no resolvable branch, is not
	// actionable: feature-dev would be re-dispatched with nothing to resolve (and
	// with "unknown" it skips the branch checkout outright), spinning through the
	// whole max_dev_redispatch budget and terminating in triage anyway. Escalate
	// here instead.
	//
	// The per-entry check below is what makes that claim true for the SKILL
	// writer as well as the Go one. Rejecting only the whole-document shapes
	// (zero files, unnamed branch) let a two-file context through when just ONE
	// entry had both sides empty — precisely what a failed `git show ":2:$f"`
	// used to produce next to a healthy sibling — and that context cost the full
	// re-dispatch budget (#301 round-2 finding 4). Now any writer's degenerate
	// entry stops at one honest escalation.
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
	if len(degenerate) > 0 {
		return RecoveryResult{
			Action: a.Name(),
			Reason: fmt.Sprintf("conflict-context-%d.json carries %d of %d entries with both sides empty and nothing explaining why — those conflicts were never recorded",
				failure.IssueNumber, len(degenerate), len(files)),
			Evidence: append(append([]string{
				fmt.Sprintf("pr=%d", failure.PRNumber),
				fmt.Sprintf("branch=%s", cc.Branch),
				fmt.Sprintf("context=%s", contextPath),
			}, prefixed("unrecorded_file=", degenerate)...),
				prefixed("conflicting_file=", files)...),
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
	// EvidenceDir is the durable conflict-evidence-{N}/ directory holding the raw
	// index blobs, written on captureFailed when the conflict could not be turned
	// into an actionable context. "" means the conflict is recorded NOWHERE.
	//
	// This — not the outcome — is what licenses `git rebase --abort`: the abort
	// destroys the :2:/:3: stages permanently, so the only question that matters
	// at that moment is whether the evidence already exists somewhere else. On
	// captureCaptured the context file itself is that somewhere (it carries the
	// full text of both sides), so no dump is needed or written.
	EvidenceDir string
	// Err explains a captureFailed. Nil otherwise.
	Err error
}

// maxConflictBlobBytes caps how much of ONE side of ONE conflicting file is
// inlined into conflict-context-{N}.json. Past it the path is not capturable
// into the context and the raw bytes go to the durable evidence dump instead —
// a TRUNCATED side is worse than none, because feature-dev resolves against
// what the context says and would write back the truncation (#301 review).
const maxConflictBlobBytes = 1 << 20 // 1 MiB

// unmergedStage is one index stage of one conflicting path: the mode git
// recorded for it and the object id at that stage.
//
// The MODE is load-bearing and used to be discarded (#301 round-2 findings 1/6).
// `git ls-files -u` prints it as the first field, and for a conflicted submodule
// pointer it is 160000 — a GITLINK, whose object id names a COMMIT in the
// submodule's own object store, not a blob in this repository. Running
// `git cat-file blob <that id>` exits 128 ("bad file"), which used to fail the
// whole capture AND the evidence dump, which in turn suppressed
// `git rebase --abort` and left the worktree permanently detached with an
// unmerged index — from an ordinary submodule-pointer conflict.
type unmergedStage struct {
	Mode string
	OID  string
}

// unmergedPath is one path with an unmerged index entry, plus the stage entries
// git actually recorded for it (1 = merge base, 2 = git's "ours", 3 = git's
// "theirs"). Which stages exist is decided by the conflict's SHAPE, not by us: a
// modify/delete conflict genuinely has no stage 3, an add/add has no stage 1,
// and a delete/delete has only stage 1. Reading them from git rather than
// assuming is what lets a missing side be reported as missing instead of as an
// empty string.
type unmergedPath struct {
	Path   string
	Stages map[int]unmergedStage
}

// isBlobMode reports whether an index mode names an object this repository can
// read with `git cat-file blob`. Regular files (100644), executables (100755)
// and symlinks (120000) are blobs — a symlink's "content" is its target path,
// which reads and inlines exactly like text. 160000 (gitlink) is not: its id is
// a commit in another repository's object store. Any other mode is likewise
// treated as not-a-blob rather than guessed at.
func isBlobMode(mode string) bool {
	switch mode {
	case "100644", "100755", "120000":
		return true
	default:
		return false
	}
}

// conflictOperation names which git operation produced the conflicted index.
// It decides the stage→side mapping and NOTHING else — see conflictSides.
type conflictOperation string

const (
	opRebase conflictOperation = "rebase"
	opMerge  conflictOperation = "merge"
)

// conflictSides maps the operation to the index stages that hold the PR
// branch's work ("ours" in the CONSUMER's vocabulary) and the base ("theirs").
//
//	operation | ours (PR branch work) | theirs (base)
//	----------|-----------------------|--------------
//	rebase    | stage 3               | stage 2
//	merge     | stage 2               | stage 3
//
// git's own stage names are relative to what is CHECKED OUT, and a rebase checks
// out the upstream and replays your commits onto it — so git calls the base
// "ours" and your work "theirs", the exact inverse of a merge. Every consumer of
// conflict-context-{N}.json (the SDK schema, feature-dev's Step 0.7.1b intake,
// merge.md) defines ours as the PR's feature work, so the writer must do this
// translation rather than pass git's naming through (#301 round-2 findings 3/5).
func conflictSides(op conflictOperation) (oursStage, theirsStage int) {
	if op == opRebase {
		return 3, 2
	}
	return 2, 3
}

// detectConflictOperation asks the repository which operation is in progress,
// rather than assuming. A rebase state directory means rebase; MERGE_HEAD means
// a merge. Anything else (including a cherry-pick, which like a merge replays
// onto the checked-out branch) uses git's plain stage naming.
func detectConflictOperation(ctx context.Context, workspace string) conflictOperation {
	if rebaseStateDir(ctx, workspace) != "" {
		return opRebase
	}
	if gitPathExists(ctx, workspace, "MERGE_HEAD") {
		return opMerge
	}
	return opMerge
}

// gitPathExists resolves a $GIT_DIR-relative path through `rev-parse --git-path`
// (worktree-safe) and reports whether it exists.
func gitPathExists(ctx context.Context, workspace, rel string) bool {
	out, err := execGit(ctx, workspace, "rev-parse", "--git-path", rel)
	if err != nil {
		return false
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(workspace, p)
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

// listUnmergedPaths enumerates the conflicted index via `git ls-files -u -z`.
//
// `-z` is not optional and neither is ls-files (#301 review). The obvious
// `git diff --name-only --diff-filter=U` C-quotes any path with a non-ASCII or
// control byte — a real conflict in `café.txt` comes back as the literal
// 12-character string `"caf\303\251.txt"`, which no filesystem path matches and
// which `git show :2:<path>` rejects with "does not exist (neither on disk nor
// in the index)". The capture then recorded that path with both sides empty and
// still called itself a success. `-z` emits raw bytes with a NUL terminator, so
// no quoting and no newline ambiguity, and ls-files additionally hands back the
// per-stage blob SHAs — which means every subsequent read is by SHA and the path
// never has to survive a round-trip through a git argument at all.
//
// An unparseable record is an ERROR, never a skipped line: dropping it silently
// is how a conflicted path becomes invisible and the capture reports fewer files
// than exist.
func listUnmergedPaths(ctx context.Context, workspace string) ([]unmergedPath, error) {
	out, err := execGit(ctx, workspace, "ls-files", "-u", "-z")
	if err != nil {
		return nil, err
	}
	var order []string
	byPath := make(map[string]map[int]unmergedStage)
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		// "<mode> <sha> <stage>\t<path>"
		meta, p, ok := strings.Cut(rec, "\t")
		if !ok || p == "" {
			return nil, fmt.Errorf("unparseable `git ls-files -u -z` record: %q", truncate(rec, 120))
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 {
			return nil, fmt.Errorf("unparseable `git ls-files -u -z` record: %q", truncate(rec, 120))
		}
		stage, cerr := strconv.Atoi(fields[2])
		if cerr != nil || stage < 1 || stage > 3 {
			return nil, fmt.Errorf("unparseable index stage in `git ls-files -u -z` record: %q", truncate(rec, 120))
		}
		if _, seen := byPath[p]; !seen {
			byPath[p] = make(map[int]unmergedStage)
			order = append(order, p)
		}
		// fields[0] is the MODE. Keeping it is what stops a gitlink (160000)
		// being handed to `cat-file blob` (#301 round-2 findings 1/6).
		byPath[p][stage] = unmergedStage{Mode: fields[0], OID: fields[1]}
	}
	paths := make([]unmergedPath, 0, len(order))
	for _, p := range order {
		paths = append(paths, unmergedPath{Path: p, Stages: byPath[p]})
	}
	return paths, nil
}

// isBlobSHA reports whether s is a plausible git object id. Enforced before a
// SHA is ever used as a filename in the evidence dump, so nothing git prints can
// steer a write outside that directory.
func isBlobSHA(s string) bool {
	if len(s) < 40 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// capturableBlob reads one side of one conflict for INLINING INTO JSON, and
// refuses every shape that would be silently corrupted on the way there.
//
// present=false is the honest "this side does not exist in the index" — a
// modify/delete conflict — and returns "" with no error. Every other empty
// return is an error, so the caller never has to guess which empty string it is
// holding (#301).
//
// The UTF-8 check is the binary-conflict fix: encoding/json replaces every
// invalid byte with U+FFFD, so a 256-byte binary blob round-tripped to 512 bytes
// of replacement runes and was still reported `capture=captured` before the
// abort deleted the original. Bytes that cannot survive JSON do not go into
// JSON.
// The size cap is enforced BEFORE the read, not after it. `git cat-file -s` answers
// the object's size in O(1) without materializing it; checking `len(b)` after an
// `execGit` that already buffered the whole object into memory enforced the cap
// only once the allocation it exists to prevent had happened, so a
// multi-hundred-megabyte conflicting asset OOM'd the long-lived scheduler
// process before being politely refused (#301 round-2 advisory).
func capturableBlob(ctx context.Context, workspace, sha string, present bool) (string, error) {
	if !present {
		return "", nil
	}
	if !isBlobSHA(sha) {
		return "", fmt.Errorf("git reported a malformed blob id %q", truncate(sha, 80))
	}
	sizeOut, err := execGit(ctx, workspace, "cat-file", "-s", sha)
	if err != nil {
		return "", fmt.Errorf("index blob %s is unreadable: %w", sha[:8], err)
	}
	size, perr := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
	if perr != nil {
		return "", fmt.Errorf("index blob %s has an unparseable size %q", sha[:8], truncate(string(sizeOut), 40))
	}
	if size > maxConflictBlobBytes {
		return "", fmt.Errorf("index blob %s is %d bytes, over the %d-byte context cap", sha[:8], size, maxConflictBlobBytes)
	}
	b, err := execGit(ctx, workspace, "cat-file", "blob", sha)
	if err != nil {
		return "", fmt.Errorf("index blob %s is unreadable: %w", sha[:8], err)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("index blob %s is not valid UTF-8 (binary conflict)", sha[:8])
	}
	return string(b), nil
}

// conflictSide renders ONE side of ONE conflicting path for the context
// document. It returns the inlined text, the commit id when the side is a
// gitlink (metadata-only), and an error only when the side genuinely could not
// be represented.
//
// A non-blob mode is not a failure and never reads an object: a submodule
// pointer's entire content IS the commit id, so it is recorded as such and the
// text stays empty. That is the difference between capturing a submodule
// conflict and wedging the worktree (#301 round-2 findings 1/6).
func conflictSide(ctx context.Context, workspace string, st unmergedStage, present bool) (text, commit string, err error) {
	if !present {
		return "", "", nil
	}
	if !isBlobMode(st.Mode) {
		if !isBlobSHA(st.OID) {
			return "", "", fmt.Errorf("git reported a malformed object id %q for mode %s", truncate(st.OID, 80), truncate(st.Mode, 12))
		}
		return "", st.OID, nil
	}
	text, err = capturableBlob(ctx, workspace, st.OID, true)
	return text, "", err
}

// buildConflictFiles turns the unmerged index into conflicting_files[] entries,
// applying the capture precondition PER FILE.
//
// Per file, not globally (#301 review): the previous code counted blob reads
// across the whole capture, so one readable file made the entire capture
// "captured" while its siblings went into the context with both sides empty —
// feature-dev then re-conflicted on them and burned the whole
// max_dev_redispatch budget. Any path that cannot be captured makes the CAPTURE
// fail; a partial capture is not a capture.
//
// ours_present / theirs_present are recorded alongside the blobs so an empty
// side is never ambiguous between "deleted on that side" and "empty file", and
// ours_mode / theirs_mode so a metadata-only (gitlink) side is likewise
// self-describing. All four are declared in the published ConflictFileSchema —
// zod strips unknown keys from a plain object, so a field that is not in the
// schema never reaches a TypeScript consumer at all.
//
// `op` decides which index stage is which side; see conflictSides for the table
// and why git's own naming cannot be passed through.
func buildConflictFiles(ctx context.Context, workspace string, paths []unmergedPath, op conflictOperation) ([]map[string]interface{}, []string) {
	oursStage, theirsStage := conflictSides(op)
	files := make([]map[string]interface{}, 0, len(paths))
	var uncapturable []string
	for _, p := range paths {
		// A path whose NAME is not valid UTF-8 (legal on ext4/xfs) would be
		// silently rewritten by encoding/json — every invalid byte becomes U+FFFD
		// — producing a context that names a path nothing can open while the
		// capture reported success (#301 round-2 advisory).
		if !utf8.ValidString(p.Path) {
			uncapturable = append(uncapturable, fmt.Sprintf("%q (path name is not valid UTF-8 and cannot survive JSON)", p.Path))
			continue
		}
		oursSt, hasOurs := p.Stages[oursStage]
		theirsSt, hasTheirs := p.Stages[theirsStage]
		ours, oursCommit, oerr := conflictSide(ctx, workspace, oursSt, hasOurs)
		if oerr != nil {
			uncapturable = append(uncapturable, fmt.Sprintf("%s ours: %s", p.Path, oerr))
			continue
		}
		theirs, theirsCommit, terr := conflictSide(ctx, workspace, theirsSt, hasTheirs)
		if terr != nil {
			uncapturable = append(uncapturable, fmt.Sprintf("%s theirs: %s", p.Path, terr))
			continue
		}
		// Neither side present is a delete/delete conflict (stage 1 only). It is a
		// real conflict class with a real decision to make, and it is fully
		// described by the two presence flags — failing the capture over it only
		// suppressed the abort and wedged the worktree.
		entry := map[string]interface{}{
			"path":           p.Path,
			"ours":           ours,
			"theirs":         theirs,
			"ours_present":   hasOurs,
			"theirs_present": hasTheirs,
			"ours_mode":      oursSt.Mode,
			"theirs_mode":    theirsSt.Mode,
		}
		if oursCommit != "" {
			entry["ours_commit"] = oursCommit
		}
		if theirsCommit != "" {
			entry["theirs_commit"] = theirsCommit
		}
		files = append(files, entry)
	}
	return files, uncapturable
}

// preserveConflictEvidence copies the raw conflicted index out of git and into
// `.nightgauge/pipeline/conflict-evidence-{N}/` so the conflict survives the
// `git rebase --abort` that follows.
//
// This exists because "leave the rebase in progress instead" does not work in
// the running system (#301 review): the scheduler's own terminal defer sees the
// `UU` paths as uncommitted work, runs `git add -A`, and collapses the very
// :2:/:3: stages the skipped abort was protecting — then commits conflict
// markers onto the detached HEAD and relabels the run `worktree_uncommitted`,
// which means "recovered, not a failure". The worktree is left detached, which
// the sweep skips forever, and unusable (`git checkout` refuses on an unmerged
// index). Copying the evidence OUT and then aborting keeps the evidence and
// leaves a worktree every consumer can still handle.
//
// Blobs are stored content-addressed under blobs/<sha> and streamed rather than
// buffered, so neither a hostile path nor a huge binary can hurt this. The
// manifest names which stage of which path each blob was.
//
// A stage whose MODE is not a blob (160000, a submodule pointer) is recorded as
// metadata — stage, mode, commit id — and no object is read for it. Feeding such
// an id to `cat-file blob` exits 128, which used to abort the dump after it had
// already written some blobs, leaving orphan files and no manifest to interpret
// them, and leaving the caller with nothing that licensed the abort (#301
// round-2 findings 1/6).
//
// Any error removes the whole directory: a half-written dump is unreadable
// debris that no tool ever cleans up, and its presence would misreport the
// conflict as preserved.
func preserveConflictEvidence(ctx context.Context, workspace string, issue, pr int, branch, baseRef string, paths []unmergedPath, op conflictOperation) (dumpDir string, err error) {
	dir := filepath.Join(workspace, ".nightgauge", "pipeline", fmt.Sprintf("conflict-evidence-%d", issue))
	// A dump from an earlier attempt describes an earlier conflict; leaving it
	// merged with this one would produce an artifact that is true of neither.
	if rerr := os.RemoveAll(dir); rerr != nil {
		return "", fmt.Errorf("clear stale evidence dir: %w", rerr)
	}
	if merr := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); merr != nil {
		return "", fmt.Errorf("create evidence dir: %w", merr)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	type stageRecord struct {
		Stage   int    `json:"stage"`
		Mode    string `json:"mode"`
		SHA     string `json:"sha"`
		Bytes   int64  `json:"bytes,omitempty"`
		File    string `json:"file,omitempty"`
		Gitlink bool   `json:"gitlink,omitempty"`
	}
	type entryRecord struct {
		Path   string        `json:"path"`
		Stages []stageRecord `json:"stages"`
	}

	oursStage, theirsStage := conflictSides(op)
	entries := make([]entryRecord, 0, len(paths))
	for _, p := range paths {
		entry := entryRecord{Path: p.Path}
		for _, stage := range []int{1, 2, 3} {
			st, ok := p.Stages[stage]
			if !ok {
				continue
			}
			if !isBlobSHA(st.OID) {
				return "", fmt.Errorf("git reported a malformed object id %q for %s", truncate(st.OID, 80), p.Path)
			}
			if !isBlobMode(st.Mode) {
				// Metadata only. The commit id IS the content of a gitlink, and
				// the object lives in the submodule's store, not this one.
				entry.Stages = append(entry.Stages, stageRecord{Stage: stage, Mode: st.Mode, SHA: st.OID, Gitlink: true})
				continue
			}
			rel := "blobs/" + st.OID
			if werr := execGitToFile(ctx, workspace, filepath.Join(dir, "blobs", st.OID), "cat-file", "blob", st.OID); werr != nil {
				return "", fmt.Errorf("preserve stage %d blob for %s: %w", stage, p.Path, werr)
			}
			size := int64(-1)
			if fi, serr := os.Stat(filepath.Join(dir, "blobs", st.OID)); serr == nil {
				size = fi.Size()
			}
			entry.Stages = append(entry.Stages, stageRecord{Stage: stage, Mode: st.Mode, SHA: st.OID, Bytes: size, File: rel})
		}
		entries = append(entries, entry)
	}

	manifest := map[string]interface{}{
		"schema_version":     "1.1",
		"issue_number":       issue,
		"pr_number":          pr,
		"branch":             branch,
		"base_ref":           baseRef,
		"conflict_operation": string(op),
		"captured_at":        time.Now().UTC().Format(time.RFC3339),
		"note": fmt.Sprintf(
			"raw git index stages preserved before `git rebase --abort`. Stage 1 = merge base; under a %s stage %d is the PR branch's work and stage %d is the base. A stage with gitlink=true is a submodule pointer: its sha is a COMMIT in the submodule's own store, so no bytes are dumped for it.",
			op, oursStage, theirsStage),
		"entries": entries,
	}
	data, merr := json.MarshalIndent(manifest, "", "  ")
	if merr != nil {
		return "", fmt.Errorf("encode evidence manifest: %w", merr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); werr != nil {
		return "", fmt.Errorf("write evidence manifest: %w", werr)
	}
	return dir, nil
}

// rebaseStateDir returns the path of an in-progress rebase's state directory
// (`rebase-merge` for the merge backend, `rebase-apply` for am), or "" when no
// rebase is in progress. Resolved via `rev-parse --git-path` so a linked
// worktree finds its own state rather than the main checkout's.
func rebaseStateDir(ctx context.Context, workspace string) string {
	for _, rel := range []string{"rebase-merge", "rebase-apply"} {
		out, err := execGit(ctx, workspace, "rev-parse", "--git-path", rel)
		if err != nil {
			continue
		}
		p := strings.TrimSpace(string(out))
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(workspace, p)
		}
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			return p
		}
	}
	return ""
}

// captureConflictContextFromIndex snapshots the in-progress rebase conflict
// (conflicting files + ours/theirs blobs) into conflict-context-{N}.json and
// merges a CONFLICT_RESOLUTION_NEEDED feedback signal into feedback-{N}.json.
//
// It MUST be called while the conflict is still staged (after a failed
// `git rebase`, BEFORE `git rebase --abort`) — the stage-1/2/3 entries exist
// only while the conflict is in the index, and the abort drops them
// permanently. This is the deterministic Go-side mirror of merge.md's
// capture_conflict_and_signal:
// branch-out-of-date calls it so a rebase conflict it cannot land defers to the
// conflict-recovery rewind instead of escalating immediately (#4072).
//
// INVARIANT (#301): it writes conflict-context-{N}.json and emits
// CONFLICT_RESOLUTION_NEEDED for captureCaptured and for NOTHING else. A failed
// or empty capture leaves no context at all, and actively REMOVES one left by an
// earlier attempt, so absence really is the state — which
// ConflictRecoveryLoop.Execute already routes correctly (missing context → human
// triage). Writing nothing also keeps the emitted document conformant with the
// published ConflictContextSchema, which requires conflicting_files to be
// non-empty.
//
// The `capture_failed` marker the reader understands exists for the SKILL
// writer, which — being an LLM-driven shell function that has already emitted
// output by the time a blob read fails — cannot always take the
// write-nothing option. Two writers, two mechanisms, one reader that rejects
// both failure shapes.
//
// A captureFailed that HAS unmerged paths does write one thing, and it is not a
// context: the raw index stages are dumped to conflict-evidence-{N}/ (see
// preserveConflictEvidence). Nothing reads that as a capture — it is an
// operator artifact — and it is what makes the caller's `git rebase --abort`
// non-destructive.
//
// All shell-outs go through execGit / execGitToFile so tests can stub them.
func captureConflictContextFromIndex(ctx context.Context, workspace string, issue, pr int, branch, baseRef string) conflictCapture {
	if baseRef == "" {
		baseRef = "main"
	}
	pipelineDir := filepath.Join(workspace, ".nightgauge", "pipeline")
	contextPath := filepath.Join(pipelineDir, fmt.Sprintf("conflict-context-%d.json", issue))

	// Enumerate the conflicted index. An ERROR here is not "no conflicting
	// files" — it means we do not know, which is a failed capture, not an empty
	// one. Nothing has been enumerated yet, so there is nothing to preserve.
	//
	// Absence is this function's signal for "not captured", so every non-captured
	// exit also drops a document left by an EARLIER attempt:
	// `.nightgauge/pipeline/` outlives the run, and a stale context is read by
	// ConflictRecoveryLoop exactly like a fresh one — re-dispatching feature-dev
	// against files and blobs from a conflict that no longer exists (#301 round-2
	// advisory). Best-effort: a context we cannot remove is still handled by the
	// reader's own guards.
	paths, err := listUnmergedPaths(ctx, workspace)
	if err != nil {
		_ = os.Remove(contextPath)
		return conflictCapture{
			Outcome: captureFailed,
			Err:     fmt.Errorf("could not list unmerged paths: %w", err),
		}
	}
	if len(paths) == 0 {
		_ = os.Remove(contextPath)
		return conflictCapture{Outcome: captureNoConflictState}
	}

	// Which operation produced this index decides which stage is whose work.
	// Asked while the operation is still in progress — after the abort the answer
	// is gone.
	op := detectConflictOperation(ctx, workspace)

	files := make([]string, 0, len(paths))
	for _, p := range paths {
		files = append(files, p.Path)
	}

	// From here on a conflict demonstrably exists, so every failure exit runs the
	// durable dump first: whatever went wrong, these bytes are about to be the
	// only copy.
	failed := func(cause error) conflictCapture {
		_ = os.Remove(contextPath)
		res := conflictCapture{Outcome: captureFailed, Files: files, Err: cause}
		dir, perr := preserveConflictEvidence(ctx, workspace, issue, pr, branch, baseRef, paths, op)
		if perr != nil {
			res.Err = fmt.Errorf("%w; and the raw index could not be preserved either: %v", cause, perr)
			return res
		}
		res.EvidenceDir = dir
		return res
	}

	// A conflict exists but we cannot say which branch it is on. feature-dev's
	// intake refuses to check out the "unknown" sentinel, so a context carrying
	// it would be re-dispatched against the wrong tree (or no checkout at all).
	if branch == "" || branch == unknownBranch {
		return failed(errors.New("could not resolve the branch under rebase"))
	}

	cf, uncapturable := buildConflictFiles(ctx, workspace, paths, op)
	if len(uncapturable) > 0 {
		return failed(fmt.Errorf("%d of %d conflicting path(s) cannot be represented in a conflict context: %s",
			len(uncapturable), len(paths), truncate(strings.Join(uncapturable, "; "), 300)))
	}

	contextDoc := map[string]interface{}{
		"schema_version": "1.1",
		"issue_number":   issue,
		"pr_number":      pr,
		"branch":         branch,
		"base_ref":       baseRef,
		// Which git operation the sides were read from. The mapping is already
		// applied to ours/theirs below — this records WHY, so a reader can tell
		// the document was written by an operation-aware writer.
		"conflict_operation": string(op),
		"conflicting_files":  cf,
		"created_at":         time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.MarshalIndent(contextDoc, "", "  ")
	if err != nil {
		return failed(fmt.Errorf("encode conflict context: %w", err))
	}
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		return failed(fmt.Errorf("create pipeline dir: %w", err))
	}
	if err := os.WriteFile(contextPath, data, 0o644); err != nil {
		return failed(fmt.Errorf("write conflict context: %w", err))
	}

	// Merge the CONFLICT_RESOLUTION_NEEDED signal into feedback-{N}.json. Without
	// it the scheduler never rewinds, so a context file with no signal is a
	// half-written capture — remove it rather than leave a success-shaped artifact
	// behind a failed capture.
	cc := conflictContextFile{IssueNumber: issue, PRNumber: pr, Branch: branch, BaseRef: baseRef}
	loop := &ConflictRecoveryLoop{maxDevRedispatch: DefaultConflictMaxDevRedispatch}
	if err := loop.ensureFeedbackSignal(filepath.Join(pipelineDir, fmt.Sprintf("feedback-%d.json", issue)), issue, cc, files); err != nil {
		_ = os.Remove(contextPath)
		return failed(fmt.Errorf("write conflict feedback signal: %w", err))
	}

	// captureCaptured needs no evidence dump: conflict-context-{N}.json carries
	// the full text of BOTH sides of EVERY conflicting path — that document IS
	// the durable copy, which is the whole reason this outcome may abort.
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
