package attention

import (
	"context"
	"errors"
	"testing"
)

// The steer must reach disk BEFORE the verb runs (#1410).
//
// `Resolve` used to execute the verb inside the lock and write the steer after
// the unlock, at the very end. Any verb that re-dispatches the issue therefore
// started a run that reads feedback-{N}.json at stage start — racing the run
// against the note it was supposed to carry. A steer that silently fails to
// arrive is indistinguishable from one that worked.
//
// Latent until an option re-dispatches, which is why it is pinned now: at that
// point the failure would be attributed to the new option rather than to the
// ordering, which was always wrong.

// orderRecorder records the sequence of the two side effects.
type orderRecorder struct {
	seq []string
	err error
}

func (o *orderRecorder) ExecuteVerb(context.Context, *DecisionRequest, Option) error {
	o.seq = append(o.seq, "verb")
	return o.err
}

func (o *orderRecorder) steerWriter(*DecisionRequest, string) error {
	o.seq = append(o.seq, "steer")
	return nil
}

func TestResolveWritesTheSteerBeforeRunningTheVerb(t *testing.T) {
	s := New(t.TempDir())
	rec := &orderRecorder{}
	s.SetSteerWriter(rec.steerWriter)

	req := validRequest(mustID(t), "k:steer-order")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	if _, err := s.Resolve(context.Background(), req.ID, "leave", "tester", "try the other adapter", "", rec); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := []string{"steer", "verb"}
	if len(rec.seq) != 2 || rec.seq[0] != want[0] || rec.seq[1] != want[1] {
		t.Errorf("side effects ran in order %v, want %v — a verb that re-dispatches the issue "+
			"would start a run before the note it carries exists on disk", rec.seq, want)
	}
}

// TestResolveSkipsTheSteerWhenThereIsNone: the ordering change must not make
// the steer writer fire for a resolve that carried no text.
func TestResolveSkipsTheSteerWhenThereIsNone(t *testing.T) {
	s := New(t.TempDir())
	rec := &orderRecorder{}
	s.SetSteerWriter(rec.steerWriter)

	req := validRequest(mustID(t), "k:no-steer")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if _, err := s.Resolve(context.Background(), req.ID, "leave", "tester", "", "", rec); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(rec.seq) != 1 || rec.seq[0] != "verb" {
		t.Errorf("side effects = %v, want just the verb — no steer text means no steer write", rec.seq)
	}
}

// TestResolveStillReportsASteerError: SteerErr is part of the result contract
// and must survive the move.
func TestResolveStillReportsASteerError(t *testing.T) {
	s := New(t.TempDir())
	boom := errors.New("disk full")
	s.SetSteerWriter(func(*DecisionRequest, string) error { return boom })

	req := validRequest(mustID(t), "k:steer-err")
	if _, _, err := s.Raise(req); err != nil {
		t.Fatalf("Raise: %v", err)
	}

	res, err := s.Resolve(context.Background(), req.ID, "leave", "tester", "a note", "", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !errors.Is(res.SteerErr, boom) {
		t.Errorf("SteerErr = %v, want the writer's error — callers inspect it", res.SteerErr)
	}
	// A failed steer must NOT abort the resolution: the card is still resolved.
	if !res.Request.Lifecycle.State.IsTerminal() {
		t.Errorf("a failed steer left the card in %q — the resolution itself succeeded",
			res.Request.Lifecycle.State)
	}
}
