package ipc

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
)

// recoveryClaimTTL bounds how long a claim is held without being executed or
// released; an expired claim is reclaimable, so a dead extension host cannot
// wedge a run.
const recoveryClaimTTL = 2 * time.Minute

// recoveryClaim is the lease record. It lives beside the run registry, under
// its own mutex (always taken BEFORE runtimesMu, never while holding it).
type recoveryClaim struct {
	token     string
	req       RecoveryRequest
	expiresAt time.Time
	// snapshot is the hash the claim was granted against; resume re-checks it.
	snapshot string
}

// recoveryOps is every side effect a recovery step has, so tests can count
// them and inject failures.
type recoveryOps interface {
	// GuardBranch applies the shared protected/default/current policy.
	GuardBranch(req RecoveryRequest) error
	// CleanupBranch deletes the local and remote branch only when the shared
	// merged-branch proof says SAFE-DELETE; it reports whether it deleted.
	CleanupBranch(req RecoveryRequest) (deleted bool, detail string, err error)
	// ArchiveSnapshot moves the run snapshot aside so the run is no longer
	// recoverable-in-place.
	ArchiveSnapshot(req RecoveryRequest, token string) error
	// Dispatch asks the host to re-run (restart) or continue (resume/execute)
	// the originating stage and phase.
	Dispatch(req RecoveryRequest, action string) error
}

// recoveryDeps are the Server's injectable recovery collaborators.
type recoveryDeps struct {
	mu     sync.Mutex
	claims map[string]*recoveryClaim

	now    func() time.Time
	ttl    time.Duration
	fs     fsOps
	keyDir string
	ops    recoveryOps
}

func (s *Server) recoveryNow() time.Time {
	if s.recovery.now != nil {
		return s.recovery.now()
	}
	return time.Now()
}

func (s *Server) recoveryTTL() time.Duration {
	if s.recovery.ttl > 0 {
		return s.recovery.ttl
	}
	return recoveryClaimTTL
}

func (s *Server) recoveryFS() fsOps {
	if s.recovery.fs != nil {
		return s.recovery.fs
	}
	return osFS{}
}

func (s *Server) recoveryKeyDir() string {
	if s.recovery.keyDir != "" {
		return s.recovery.keyDir
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "nightgauge")
}

func (s *Server) recoveryOpsImpl() recoveryOps {
	if s.recovery.ops != nil {
		return s.recovery.ops
	}
	return serverRecoveryOps{s: s}
}

// recoveryClaimHeld reports whether runID is under an unexpired recovery claim.
// resolveRun calls it on the mutating verb classes so a run being recovered
// fails closed for ordinary writers.
func (s *Server) recoveryClaimHeld(runID string) bool {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	c := s.recovery.claims[runID]
	return c != nil && s.recoveryNow().Before(c.expiresAt)
}

func newRecoveryToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func hashSnapshotFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Server) recoverySnapshotPath(req RecoveryRequest) (string, error) {
	dir := s.pipelineStateDir(req.Repo)
	if dir == "" {
		return "", fmt.Errorf("%s: no pipeline state directory for %s", codeRecoveryInvalid, req.Repo)
	}
	return filepath.Join(dir, state.SnapshotFilename(req.Issue, req.RunID)), nil
}

func (s *Server) recoveryApprovedRoots(req RecoveryRequest) []string {
	var roots []string
	if dir := s.pipelineStateDir(req.Repo); dir != "" {
		roots = append(roots, dir)
	}
	if root := s.repoRoot(req.Repo); root != "" {
		if base, err := layout.WorktreeBase(root, layout.WorktreeBaseSetting{}); err == nil {
			roots = append(roots, base)
		}
	}
	return roots
}

// recoveryClaimRun is pipeline.recoveryClaim. Everything that decides the
// outcome happens under recovery.mu so two racing claims cannot both win, and
// no write happens on any refusal.
func (s *Server) recoveryClaimRun(req RecoveryRequest) (*RecoveryClaimResult, error) {
	if err := validateRecoveryRequest(req); err != nil {
		return nil, s.rejectRecovery("pipeline.recoveryClaim", req, err)
	}
	if req.Worktree != "" {
		if _, err := containedPath(s.recoveryFS(), req.Worktree, s.recoveryApprovedRoots(req)); err != nil {
			return nil, s.rejectRecovery("pipeline.recoveryClaim", req, err)
		}
	}
	snapPath, err := s.recoverySnapshotPath(req)
	if err != nil {
		return nil, s.rejectRecovery("pipeline.recoveryClaim", req, err)
	}

	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	now := s.recoveryNow()

	if c := s.recovery.claims[req.RunID]; c != nil && now.Before(c.expiresAt) {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRecoveryClaimHeld, req.RunID, req.Issue,
			"another recovery claim is held")
	}
	if s.runLeaseIsFresh(req.RunID, now) {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRunWrongOwner, req.RunID, req.Issue,
			"the run has a live owner")
	}
	if s.schedulerRuns != nil && s.schedulerRuns.LookupRunByID(req.RunID) != nil {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRunWrongOwner, req.RunID, req.Issue,
			"the Go scheduler owns this run")
	}

	// A half-finished transaction pins the snapshot hash it began with: its
	// own archive step may already have moved the file, and the retry must
	// still be judged against what the operator originally saw.
	want := req.SnapshotSHA256
	var have string
	j, err := openRecoveryJournal(s.recoveryKeyDir(), req.RunID)
	if err != nil {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRecoveryTampered, req.RunID, req.Issue, err.Error())
	}
	if begin, open := j.open(); open {
		have = begin.Snapshot
	} else if have, err = hashSnapshotFile(snapPath); err != nil {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRunNotFound, req.RunID, req.Issue,
			"run snapshot is unreadable: "+err.Error())
	}
	if subtle.ConstantTimeCompare([]byte(have), []byte(want)) != 1 {
		return nil, s.rejectRun("pipeline.recoveryClaim", codeRecoverySnapshotChanged, req.RunID, req.Issue,
			"the run snapshot changed since it was read")
	}

	token, err := newRecoveryToken()
	if err != nil {
		return nil, err
	}
	if s.recovery.claims == nil {
		s.recovery.claims = map[string]*recoveryClaim{}
	}
	exp := now.Add(s.recoveryTTL())
	s.recovery.claims[req.RunID] = &recoveryClaim{token: token, req: req, expiresAt: exp, snapshot: have}
	return &RecoveryClaimResult{Token: token, ExpiresAt: exp.UTC().Format(time.RFC3339Nano)}, nil
}

func (s *Server) rejectRecovery(method string, req RecoveryRequest, err error) error {
	s.logRunRejection(method, codeRecoveryInvalid, req.RunID, req.Issue, err)
	return err
}

// recoveryRelease drops a claim the caller holds.
func (s *Server) recoveryRelease(runID, token string) error {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	c := s.recovery.claims[runID]
	if c == nil || subtle.ConstantTimeCompare([]byte(c.token), []byte(token)) != 1 {
		return s.rejectRun("pipeline.recoveryRelease", codeRecoveryNoClaim, runID, 0, "no such claim")
	}
	delete(s.recovery.claims, runID)
	return nil
}

// stepsFor is the ordered, journaled plan for an action.
func recoveryStepsFor(action string) ([]string, error) {
	switch action {
	case RecoveryActionRestart:
		return []string{"guard-branch", "cleanup-branch", "archive-snapshot", "dispatch"}, nil
	case RecoveryActionDiscard:
		return []string{"guard-branch", "cleanup-branch", "archive-snapshot"}, nil
	case RecoveryActionResume, RecoveryActionExecute:
		return []string{"verify-snapshot", "dispatch"}, nil
	}
	return nil, fmt.Errorf("%s: unknown recovery action %q", codeRecoveryInvalid, action)
}

// currentClaim returns the claim only while token still holds it and it has not
// expired, and the run still has no live owner. It is called before EVERY step.
func (s *Server) currentClaim(runID, token string) (*recoveryClaim, error) {
	s.recovery.mu.Lock()
	c := s.recovery.claims[runID]
	now := s.recoveryNow()
	var held bool
	if c != nil {
		held = subtle.ConstantTimeCompare([]byte(c.token), []byte(token)) == 1 && now.Before(c.expiresAt)
	}
	s.recovery.mu.Unlock()
	if !held {
		return nil, s.rejectRun("pipeline.recoveryExecute", codeRecoveryNoClaim, runID, 0,
			"the claim is not held (released, expired or taken)")
	}
	if s.runLeaseIsFresh(runID, now) {
		return nil, s.rejectRun("pipeline.recoveryExecute", codeRunWrongOwner, runID, c.req.Issue,
			"a live owner appeared while the claim was held")
	}
	return c, nil
}

// recoveryExecuteRun replays the journaled plan: completed steps are skipped,
// a tampered chain is refused, and a failed step leaves the transaction open so
// a later claim retries from exactly that step.
func (s *Server) recoveryExecuteRun(p RecoveryExecuteParams) (*RecoveryExecuteResult, error) {
	steps, err := recoveryStepsFor(p.Action)
	if err != nil {
		return nil, err
	}
	c, err := s.currentClaim(p.RunID, p.Token)
	if err != nil {
		return nil, err
	}
	j, err := openRecoveryJournal(s.recoveryKeyDir(), p.RunID)
	if err != nil {
		return nil, s.rejectRun("pipeline.recoveryExecute", codeRecoveryTampered, p.RunID, c.req.Issue, err.Error())
	}
	if begin, open := j.open(); open {
		if begin.Action != p.Action {
			return nil, s.rejectRun("pipeline.recoveryExecute", codeRecoveryInvalid, p.RunID, c.req.Issue,
				fmt.Sprintf("an open %s transaction exists for this run", begin.Action))
		}
	} else if err := j.append(journalEntry{Step: "begin", Status: journalBegin, Action: p.Action, Snapshot: c.snapshot}); err != nil {
		return nil, err
	}

	ops := s.recoveryOpsImpl()
	res := &RecoveryExecuteResult{Action: p.Action, CompletedSteps: []string{}, SkippedSteps: []string{}}
	for _, step := range steps {
		if j.stepDone(step) {
			res.SkippedSteps = append(res.SkippedSteps, step)
			continue
		}
		if _, err := s.currentClaim(p.RunID, p.Token); err != nil {
			return res, err
		}
		detail, stepErr := s.runRecoveryStep(ops, c, p, step)
		status := journalDone
		if stepErr != nil {
			status = journalFailed
			detail = stepErr.Error()
		}
		if err := j.append(journalEntry{Step: step, Status: status, Detail: detail}); err != nil {
			return res, err
		}
		if stepErr != nil {
			return res, s.rejectRun("pipeline.recoveryExecute", codeRecoveryStepFailed, p.RunID, c.req.Issue,
				step+": "+stepErr.Error())
		}
		res.CompletedSteps = append(res.CompletedSteps, step)
	}
	if err := j.append(journalEntry{Step: "close", Status: journalClosed}); err != nil {
		return res, err
	}
	s.recovery.mu.Lock()
	delete(s.recovery.claims, p.RunID)
	s.recovery.mu.Unlock()
	return res, nil
}

func (s *Server) runRecoveryStep(ops recoveryOps, c *recoveryClaim, p RecoveryExecuteParams, step string) (string, error) {
	switch step {
	case "guard-branch":
		return "", ops.GuardBranch(c.req)
	case "cleanup-branch":
		deleted, detail, err := ops.CleanupBranch(c.req)
		if err == nil && !deleted && detail == "" {
			detail = "branch kept"
		}
		return detail, err
	case "archive-snapshot":
		return "", ops.ArchiveSnapshot(c.req, c.token)
	case "verify-snapshot":
		path, err := s.recoverySnapshotPath(c.req)
		if err != nil {
			return "", err
		}
		got, err := hashSnapshotFile(path)
		if err != nil {
			return "", err
		}
		if got != c.snapshot {
			return "", fmt.Errorf("%s: resume identity mismatch — the snapshot changed under the claim", codeRecoverySnapshotChanged)
		}
		return "", nil
	case "dispatch":
		return "", ops.Dispatch(c.req, p.Action)
	}
	return "", fmt.Errorf("unknown step %q", step)
}

// serverRecoveryOps is the production implementation.
type serverRecoveryOps struct{ s *Server }

func (o serverRecoveryOps) svc(req RecoveryRequest) (*git.Service, error) {
	root := o.s.repoRoot(req.Repo)
	if root == "" {
		return nil, fmt.Errorf("no repository root for %s", req.Repo)
	}
	return git.NewService(root)
}

func (o serverRecoveryOps) GuardBranch(req RecoveryRequest) error {
	svc, err := o.svc(req)
	if err != nil {
		return err
	}
	cur, _ := svc.CurrentBranch()
	return svc.RefuseBranchMutation(req.Branch, "", cur)
}

func (o serverRecoveryOps) CleanupBranch(req RecoveryRequest) (bool, string, error) {
	svc, err := o.svc(req)
	if err != nil {
		return false, "", err
	}
	def, err := svc.DefaultBranch()
	if err != nil {
		return false, "", err
	}
	proof := svc.ProveBranchMerged(req.Branch, git.MergedProofInputs{Base: "origin/" + def})
	if proof.Verdict != git.VerdictSafeDelete {
		// Unproven work is kept, never deleted: the snapshot is archived and
		// the branch stays for the operator.
		return false, "branch kept: " + proof.Reason, nil
	}
	if err := svc.BranchCleanupPinned(req.Branch, proof.LocalTip, proof.OriginTip); err != nil {
		return false, "", err
	}
	return true, "branch deleted", nil
}

func (o serverRecoveryOps) ArchiveSnapshot(req RecoveryRequest, token string) error {
	path, err := o.s.recoverySnapshotPath(req)
	if err != nil {
		return err
	}
	dst := path + ".recovered-" + token
	if err := os.Rename(path, dst); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (o serverRecoveryOps) Dispatch(req RecoveryRequest, action string) error {
	o.s.Emit("pipeline.recoveryDispatch", map[string]interface{}{
		"action": action, "repo": req.Repo, "issue": req.Issue, "runId": req.RunID,
		"branch": req.Branch, "worktree": req.Worktree, "stage": req.Stage, "phase": req.Phase,
	})
	return nil
}
