package runstate

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// Allowed lifecycle transitions. Exposed via TransitionAllowed for callers
// (e.g. the CLI) that want to validate without invoking a Mark* helper.
var allowedTransitions = map[Lifecycle][]Lifecycle{
	StateRunning:   {StatePaused, StateCompleted, StateAborted},
	StatePaused:    {StateRunning, StateDiscarded},
	StateAborted:   {StateDiscarded},
	StateCompleted: {},
	StateDiscarded: {},
}

// TransitionAllowed reports whether moving from `from` → `to` is legal.
func TransitionAllowed(from, to Lifecycle) bool {
	for _, allowed := range allowedTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// IllegalTransitionError is returned when a Mark* call would violate the
// state machine. Wraps the from/to pair so handlers can inspect.
type IllegalTransitionError struct {
	From Lifecycle
	To   Lifecycle
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("illegal lifecycle transition: %s → %s", e.From, e.To)
}

// MarkRunningOptions captures the parameters of a fresh-start transition.
// Force=true bypasses concurrent-run detection — only the user-driven CLI
// should set it; the autonomous orchestrator never does.
type MarkRunningOptions struct {
	IssueNumber  int
	Branch       string
	WorktreePath string
	Force        bool
	HostID       string
}

// MarkRunning starts a new run, generating a fresh RunID. Returns
// ConcurrentRunRefusedError when an existing record is `running` with a
// live PID and Force=false.
func MarkRunning(baseDir string, opts MarkRunningOptions) (*RunState, error) {
	cur, err := Load(baseDir)
	if err != nil {
		return nil, err
	}
	if cur != nil && cur.State == StateRunning && !opts.Force {
		last := lastAttempt(cur)
		if last != nil && last.PID != nil && processAlive(*last.PID) {
			return nil, &ConcurrentRunRefusedError{
				IssueNumber: cur.IssueNumber,
				HolderPID:   *last.PID,
				HostID:      strOrEmpty(last.HostID),
			}
		}
	}

	runID, err := NewRunID()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pid := os.Getpid()
	hostID := opts.HostID
	if hostID == "" {
		hostID, _ = os.Hostname()
	}
	resume := StageIssuePickup
	rs := &RunState{
		SchemaVersion:   SchemaVersion,
		IssueNumber:     opts.IssueNumber,
		State:           StateRunning,
		RunID:           runID,
		AttemptNumber:   1,
		CompletedStages: []Stage{},
		ResumeFromStage: &resume,
		Branch:          opts.Branch,
		CreatedAt:       now,
		UpdatedAt:       now,
		Attempts: []Attempt{
			{
				RunID:         runID,
				AttemptNumber: 1,
				StartedAt:     now,
				PID:           &pid,
				HostID:        &hostID,
				LastStage:     &resume,
			},
		},
	}
	if opts.WorktreePath != "" {
		wp := opts.WorktreePath
		rs.WorktreePath = &wp
	}
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// MarkPaused transitions a running run to paused. Sets reason and the
// caller-provided resume_from_stage (defaults to the current resume_from).
//
// Stop NEVER deletes branches or worktrees — see ADR-001 in
// .nightgauge/knowledge/features/3238-graceful-pipeline-stop-with-durable/decisions.md.
func MarkPaused(baseDir, reason string, resumeFrom *Stage) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if !TransitionAllowed(rs.State, StatePaused) {
		return nil, &IllegalTransitionError{From: rs.State, To: StatePaused}
	}
	rs.State = StatePaused
	if reason != "" {
		r := reason
		rs.Reason = &r
	}
	if resumeFrom != nil {
		rs.ResumeFromStage = resumeFrom
	}
	t := true
	rs.Recoverable = &t
	rs.RecoveryActions = []string{"resume", "restart", "discard"}
	rs.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// MarkCompleted transitions to the terminal `completed` state on successful
// pr-merge. The caller is expected to invoke ArchiveRun afterwards.
func MarkCompleted(baseDir string) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if !TransitionAllowed(rs.State, StateCompleted) {
		return nil, &IllegalTransitionError{From: rs.State, To: StateCompleted}
	}
	rs.State = StateCompleted
	f := false
	rs.Recoverable = &f
	rs.RecoveryActions = nil
	rs.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// MarkAborted transitions to `aborted`. The recoverable flag distinguishes
// transient failures from structural mismatches (e.g., pre-Gap-1 first run
// after upgrade per ADR-002, which is non-recoverable).
func MarkAborted(baseDir, reason string, recoverable bool) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if !TransitionAllowed(rs.State, StateAborted) {
		return nil, &IllegalTransitionError{From: rs.State, To: StateAborted}
	}
	rs.State = StateAborted
	r := reason
	rs.Reason = &r
	rs.Recoverable = &recoverable
	if recoverable {
		rs.RecoveryActions = []string{"restart", "discard"}
	} else {
		rs.RecoveryActions = []string{"discard"}
	}
	rs.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// MarkDiscarded transitions to the terminal `discarded` state. Callers are
// responsible for branch + worktree teardown via the archive package.
func MarkDiscarded(baseDir, reason string) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if !TransitionAllowed(rs.State, StateDiscarded) {
		return nil, &IllegalTransitionError{From: rs.State, To: StateDiscarded}
	}
	rs.State = StateDiscarded
	r := reason
	rs.Reason = &r
	f := false
	rs.Recoverable = &f
	rs.RecoveryActions = nil
	rs.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// Resume transitions a `paused` run back to `running`, appending a new
// attempt. Re-uses the same RunID — restart (new RunID) is a separate
// operation handled by archiving + MarkRunning.
func Resume(baseDir string) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if rs.State != StatePaused {
		return nil, fmt.Errorf("cannot resume from %s; only paused → running is allowed", rs.State)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	pid := os.Getpid()
	host, _ := os.Hostname()
	last := rs.AttemptNumber + 1
	rs.AttemptNumber = last
	rs.State = StateRunning
	rs.Reason = nil
	rs.UpdatedAt = now
	rs.Attempts = append(rs.Attempts, Attempt{
		RunID:         rs.RunID,
		AttemptNumber: last,
		StartedAt:     now,
		PID:           &pid,
		HostID:        &host,
		LastStage:     rs.ResumeFromStage,
	})
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

// MarkStageComplete records a stage-rename success in completed_stages and
// advances resume_from_stage to the next pipeline stage. Idempotent.
func MarkStageComplete(baseDir string, stage Stage) (*RunState, error) {
	rs, err := requireExisting(baseDir)
	if err != nil {
		return nil, err
	}
	if hasStage(rs.CompletedStages, stage) {
		return rs, nil
	}
	rs.CompletedStages = append(rs.CompletedStages, stage)
	if next := nextStage(stage); next != nil {
		rs.ResumeFromStage = next
	}
	rs.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if err := Save(baseDir, rs); err != nil {
		return nil, err
	}
	return rs, nil
}

func requireExisting(baseDir string) (*RunState, error) {
	rs, err := Load(baseDir)
	if err != nil {
		return nil, err
	}
	if rs == nil {
		return nil, errors.New("run-state.json does not exist; call MarkRunning first")
	}
	return rs, nil
}

func lastAttempt(rs *RunState) *Attempt {
	if len(rs.Attempts) == 0 {
		return nil
	}
	return &rs.Attempts[len(rs.Attempts)-1]
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
