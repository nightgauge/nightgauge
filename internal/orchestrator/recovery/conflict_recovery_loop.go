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

// unmergedPath is one path with an unmerged index entry, plus the blob SHA of
// each stage git actually recorded for it (1 = merge base, 2 = ours, 3 =
// theirs). Which stages exist is decided by the conflict's SHAPE, not by us: a
// modify/delete conflict genuinely has no stage 3, an add/add has no stage 1.
// Reading them from git rather than assuming is what lets a missing side be
// reported as missing instead of as an empty string.
type unmergedPath struct {
	Path   string
	Stages map[int]string
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
	byPath := make(map[string]map[int]string)
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
			byPath[p] = make(map[int]string)
			order = append(order, p)
		}
		byPath[p][stage] = fields[1]
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
func capturableBlob(ctx context.Context, workspace, sha string, present bool) (string, error) {
	if !present {
		return "", nil
	}
	if !isBlobSHA(sha) {
		return "", fmt.Errorf("git reported a malformed blob id %q", truncate(sha, 80))
	}
	b, err := execGit(ctx, workspace, "cat-file", "blob", sha)
	if err != nil {
		return "", fmt.Errorf("index blob %s is unreadable: %w", sha[:8], err)
	}
	if len(b) > maxConflictBlobBytes {
		return "", fmt.Errorf("index blob %s is %d bytes, over the %d-byte context cap", sha[:8], len(b), maxConflictBlobBytes)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("index blob %s is not valid UTF-8 (binary conflict)", sha[:8])
	}
	return string(b), nil
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
// side is never ambiguous between "deleted on that side" and "empty file". The
// published ConflictContextSchema is a passthrough object, so these ride along
// without a schema bump.
func buildConflictFiles(ctx context.Context, workspace string, paths []unmergedPath) ([]map[string]interface{}, []string) {
	files := make([]map[string]interface{}, 0, len(paths))
	var uncapturable []string
	for _, p := range paths {
		// Stage 2 / stage 3 keep git's names, and under a REBASE those names are
		// counter-intuitive: git replays each feature commit ONTO the upstream, so
		// stage 2 ("ours") holds the rebase base's version and stage 3 ("theirs")
		// the feature branch's — the inverse of a merge. Verified against real git
		// by TestRealGit_RebaseConflict_CapturesThenAborts. The published
		// ConflictFileSchema describes these fields the other way round; that
		// mislabeling predates this code and is tracked separately.
		oursSHA, hasOurs := p.Stages[2]
		theirsSHA, hasTheirs := p.Stages[3]
		if !hasOurs && !hasTheirs {
			uncapturable = append(uncapturable, fmt.Sprintf("%s (index carries neither an ours nor a theirs stage)", p.Path))
			continue
		}
		ours, oerr := capturableBlob(ctx, workspace, oursSHA, hasOurs)
		if oerr != nil {
			uncapturable = append(uncapturable, fmt.Sprintf("%s ours: %s", p.Path, oerr))
			continue
		}
		theirs, terr := capturableBlob(ctx, workspace, theirsSHA, hasTheirs)
		if terr != nil {
			uncapturable = append(uncapturable, fmt.Sprintf("%s theirs: %s", p.Path, terr))
			continue
		}
		files = append(files, map[string]interface{}{
			"path":           p.Path,
			"ours":           ours,
			"theirs":         theirs,
			"ours_present":   hasOurs,
			"theirs_present": hasTheirs,
		})
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
func preserveConflictEvidence(ctx context.Context, workspace string, issue, pr int, branch, baseRef string, paths []unmergedPath) (string, error) {
	dir := filepath.Join(workspace, ".nightgauge", "pipeline", fmt.Sprintf("conflict-evidence-%d", issue))
	// A dump from an earlier attempt describes an earlier conflict; leaving it
	// merged with this one would produce an artifact that is true of neither.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("clear stale evidence dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		return "", fmt.Errorf("create evidence dir: %w", err)
	}

	type stageRecord struct {
		Stage int    `json:"stage"`
		SHA   string `json:"sha"`
		Bytes int64  `json:"bytes"`
		File  string `json:"file"`
	}
	type entryRecord struct {
		Path   string        `json:"path"`
		Stages []stageRecord `json:"stages"`
	}

	entries := make([]entryRecord, 0, len(paths))
	for _, p := range paths {
		entry := entryRecord{Path: p.Path}
		for _, stage := range []int{1, 2, 3} {
			sha, ok := p.Stages[stage]
			if !ok {
				continue
			}
			if !isBlobSHA(sha) {
				return "", fmt.Errorf("git reported a malformed blob id %q for %s", truncate(sha, 80), p.Path)
			}
			rel := "blobs/" + sha
			if err := execGitToFile(ctx, workspace, filepath.Join(dir, "blobs", sha), "cat-file", "blob", sha); err != nil {
				return "", fmt.Errorf("preserve stage %d blob for %s: %w", stage, p.Path, err)
			}
			size := int64(-1)
			if fi, err := os.Stat(filepath.Join(dir, "blobs", sha)); err == nil {
				size = fi.Size()
			}
			entry.Stages = append(entry.Stages, stageRecord{Stage: stage, SHA: sha, Bytes: size, File: rel})
		}
		entries = append(entries, entry)
	}

	manifest := map[string]interface{}{
		"schema_version": "1.0",
		"issue_number":   issue,
		"pr_number":      pr,
		"branch":         branch,
		"base_ref":       baseRef,
		"captured_at":    time.Now().UTC().Format(time.RFC3339),
		"note":           "raw git index stages preserved before `git rebase --abort`; stage 1 = merge base, 2 = ours, 3 = theirs",
		"entries":        entries,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode evidence manifest: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), data, 0o644); err != nil {
		return "", fmt.Errorf("write evidence manifest: %w", err)
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
// or empty capture leaves no context at all — absence is already handled
// correctly by ConflictRecoveryLoop.Execute (missing context → human triage),
// whereas a "capture_failed: true" marker would only work for readers that
// remembered to check it, and the first reader that forgot would reintroduce
// this bug. Writing nothing also keeps the emitted document conformant with the
// published ConflictContextSchema, which requires conflicting_files to be
// non-empty.
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

	// Enumerate the conflicted index. An ERROR here is not "no conflicting
	// files" — it means we do not know, which is a failed capture, not an empty
	// one. Nothing has been enumerated yet, so there is nothing to preserve.
	paths, err := listUnmergedPaths(ctx, workspace)
	if err != nil {
		return conflictCapture{
			Outcome: captureFailed,
			Err:     fmt.Errorf("could not list unmerged paths: %w", err),
		}
	}
	if len(paths) == 0 {
		return conflictCapture{Outcome: captureNoConflictState}
	}

	files := make([]string, 0, len(paths))
	for _, p := range paths {
		files = append(files, p.Path)
	}

	// From here on a conflict demonstrably exists, so every failure exit runs the
	// durable dump first: whatever went wrong, these bytes are about to be the
	// only copy.
	failed := func(cause error) conflictCapture {
		res := conflictCapture{Outcome: captureFailed, Files: files, Err: cause}
		dir, perr := preserveConflictEvidence(ctx, workspace, issue, pr, branch, baseRef, paths)
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

	cf, uncapturable := buildConflictFiles(ctx, workspace, paths)
	if len(uncapturable) > 0 {
		return failed(fmt.Errorf("%d of %d conflicting path(s) cannot be represented in a conflict context: %s",
			len(uncapturable), len(paths), truncate(strings.Join(uncapturable, "; "), 300)))
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
		return failed(fmt.Errorf("encode conflict context: %w", err))
	}
	pipelineDir := filepath.Join(workspace, ".nightgauge", "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o755); err != nil {
		return failed(fmt.Errorf("create pipeline dir: %w", err))
	}
	contextPath := filepath.Join(pipelineDir, fmt.Sprintf("conflict-context-%d.json", issue))
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
