package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (f *recoveryFixture) claim(t *testing.T) string {
	t.Helper()
	c, err := f.s.recoveryClaimRun(f.req)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	return c.Token
}

func TestRecoveryExecute_RestartRunsEveryStageOnce(t *testing.T) {
	f := newRecoveryFixture(t)
	tok := f.claim(t)
	res, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionRestart})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CompletedSteps) != 4 {
		t.Fatalf("completed = %v", res.CompletedSteps)
	}
	if _, err := os.Stat(f.snap); !os.IsNotExist(err) {
		t.Fatal("snapshot should have been archived")
	}
	if f.s.recoveryClaimHeld(f.req.RunID) {
		t.Fatal("claim should be released after a closed transaction")
	}
}

func TestRecoveryExecute_FailureIsRetryableAndIdempotent(t *testing.T) {
	f := newRecoveryFixture(t)
	f.ops.failDispatch.Store(true)
	tok := f.claim(t)
	_, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionRestart})
	if err == nil || !strings.Contains(err.Error(), codeRecoveryStepFailed) {
		t.Fatalf("err = %v", err)
	}

	// The archive step already moved the snapshot; the retry claim is judged
	// against the hash the transaction began with.
	f.ops.failDispatch.Store(false)
	f.s.recovery.mu.Lock()
	delete(f.s.recovery.claims, f.req.RunID)
	f.s.recovery.mu.Unlock()
	tok = f.claim(t)
	res, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionRestart})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := strings.Join(res.SkippedSteps, ","); got != "guard-branch,cleanup-branch,archive-snapshot" {
		t.Fatalf("skipped = %q", got)
	}
	if f.ops.guard.Load() != 1 || f.ops.cleanup.Load() != 1 || f.ops.archive.Load() != 1 {
		t.Fatalf("side effects repeated: guard=%d cleanup=%d archive=%d",
			f.ops.guard.Load(), f.ops.cleanup.Load(), f.ops.archive.Load())
	}
	if f.ops.dispatch.Load() != 2 {
		t.Fatalf("dispatch = %d, want 2 (failed once, retried once)", f.ops.dispatch.Load())
	}
}

func TestRecoveryExecute_TamperedJournalRefused(t *testing.T) {
	f := newRecoveryFixture(t)
	f.ops.failDispatch.Store(true)
	tok := f.claim(t)
	_, _ = f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionRestart})

	jp := filepath.Join(f.s.recovery.keyDir, "recovery-journal", f.req.RunID+".jsonl")
	b, err := os.ReadFile(jp)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a completed step to look unfinished-then-done differently: edit the
	// detail of an entry without recomputing its hash.
	forged := strings.Replace(string(b), `"step":"guard-branch"`, `"step":"guard-brancH"`, 1)
	if forged == string(b) {
		t.Fatal("test did not alter the journal")
	}
	if err := os.WriteFile(jp, []byte(forged), 0o600); err != nil {
		t.Fatal(err)
	}
	f.s.recovery.mu.Lock()
	delete(f.s.recovery.claims, f.req.RunID)
	f.s.recovery.mu.Unlock()
	_, err = f.s.recoveryClaimRun(f.req)
	if err == nil || !strings.Contains(err.Error(), codeRecoveryTampered) {
		t.Fatalf("err = %v, want %s", err, codeRecoveryTampered)
	}
}

func TestRecoveryExecute_ForgedMACWithRecomputedHashRefused(t *testing.T) {
	// A forger with workspace write access but no key can recompute hashes;
	// only the HMAC stops them.
	f := newRecoveryFixture(t)
	f.ops.failDispatch.Store(true)
	tok := f.claim(t)
	_, _ = f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionRestart})
	jp := filepath.Join(f.s.recovery.keyDir, "recovery-journal", f.req.RunID+".jsonl")
	j, err := openRecoveryJournal(f.s.recovery.keyDir, f.req.RunID)
	if err != nil {
		t.Fatal(err)
	}
	// Re-forge the whole journal with a self-consistent chain under another key.
	orig := j.entries
	if err := os.Remove(jp); err != nil {
		t.Fatal(err)
	}
	j.key = []byte(strings.Repeat("k", 32))
	j.entries = nil
	for _, e := range orig {
		if err := j.append(journalEntry{Step: e.Step, Status: e.Status, Action: e.Action, Snapshot: e.Snapshot, Detail: e.Detail}); err != nil {
			t.Fatal(err)
		}
	}
	// j.append wrote with the attacker's key; the legitimate loader must refuse.
	if _, err := openRecoveryJournal(f.s.recovery.keyDir, f.req.RunID); err == nil ||
		!strings.Contains(err.Error(), codeRecoveryTampered) {
		t.Fatalf("err = %v, want %s (journal %s)", err, codeRecoveryTampered, jp)
	}
}

func TestRecoveryExecute_ResumeIdentityMismatch(t *testing.T) {
	f := newRecoveryFixture(t)
	tok := f.claim(t)
	if err := os.WriteFile(f.snap, []byte(`{"changed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionResume})
	if err == nil || !strings.Contains(err.Error(), codeRecoverySnapshotChanged) {
		t.Fatalf("err = %v", err)
	}
	if f.ops.dispatch.Load() != 0 {
		t.Fatal("resume dispatched despite an identity mismatch")
	}
}

func TestRecoveryExecute_LiveOwnerAppearingStopsExecution(t *testing.T) {
	f := newRecoveryFixture(t)
	tok := f.claim(t)
	f.s.activeRuntimes[f.req.RunID] = &runEntry{lastSeen: f.s.recoveryNow(), issue: 7}
	_, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: tok, Action: RecoveryActionResume})
	if err == nil || !strings.HasPrefix(err.Error(), codeRunWrongOwner) {
		t.Fatalf("err = %v", err)
	}
	if f.ops.dispatch.Load() != 0 {
		t.Fatal("dispatched with a live owner")
	}
}

func TestRecoveryExecute_WrongTokenRefused(t *testing.T) {
	f := newRecoveryFixture(t)
	f.claim(t)
	_, err := f.s.recoveryExecuteRun(RecoveryExecuteParams{RunID: f.req.RunID, Token: "nope", Action: RecoveryActionDiscard})
	if err == nil || !strings.HasPrefix(err.Error(), codeRecoveryNoClaim) {
		t.Fatalf("err = %v", err)
	}
}
