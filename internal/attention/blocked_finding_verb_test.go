package attention

import (
	"context"
	"errors"
	"testing"
)

// blocked.clearFinding is the ONLY retraction for a hold that otherwise defers
// an issue at pickup on every dispatch, forever (#1147). These tests pin the
// four properties the verb promises, and each is a refusal rather than a
// happy-path assertion, because every one of them is a way the durable hold
// could be lifted for an issue nobody clicked on.

type recordingClearer struct {
	calls []struct {
		repo  string
		issue int
	}
	err error
}

func (c *recordingClearer) ClearBlockedFinding(_ context.Context, repo string, issue int) error {
	c.calls = append(c.calls, struct {
		repo  string
		issue int
	}{repo, issue})
	return c.err
}

func clearRequest(repo string, issue int) *DecisionRequest {
	return &DecisionRequest{
		ID:       "dr_test",
		Producer: "out-of-scope-blocker",
		Context:  Context{Repo: repo, Issue: issue},
	}
}

var clearOption = Option{ID: "cleared", Verb: VerbBlockedFindingClear}

func TestClearBlockedFindingUsesTheRequestsOwnTarget(t *testing.T) {
	c := &recordingClearer{}
	req := clearRequest("octocat/acme", 42)

	if err := ExecuteClearBlockedFinding(context.Background(), c, req, clearOption,
		[]string{"octocat/acme"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(c.calls) != 1 || c.calls[0].repo != "octocat/acme" || c.calls[0].issue != 42 {
		t.Fatalf("cleared %+v, want one call for octocat/acme#42", c.calls)
	}
}

// The target has TWO coordinates, and neither may come from the resolving
// surface. A caller-supplied issue number would let any local process lift the
// hold on an arbitrary issue in a configured repo and send it straight back
// into the wall it stopped at.
func TestClearBlockedFindingAcceptsNoArguments(t *testing.T) {
	c := &recordingClearer{}
	opt := Option{ID: "cleared", Verb: VerbBlockedFindingClear,
		Args: map[string]any{"issueNumber": 99}}

	err := ExecuteClearBlockedFinding(context.Background(), c, clearRequest("octocat/acme", 42),
		opt, []string{"octocat/acme"})

	if !errors.Is(err, ErrVerbArgsNotAccepted) {
		t.Fatalf("err = %v, want ErrVerbArgsNotAccepted", err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("cleared %+v despite refusing the args", c.calls)
	}
}

func TestClearBlockedFindingRefusesAnUnconfiguredRepo(t *testing.T) {
	c := &recordingClearer{}

	err := ExecuteClearBlockedFinding(context.Background(), c, clearRequest("attacker/evil", 42),
		clearOption, []string{"octocat/acme"})

	if !errors.Is(err, ErrVerbTargetNotConfigured) {
		t.Fatalf("err = %v, want ErrVerbTargetNotConfigured", err)
	}
	if len(c.calls) != 0 {
		t.Fatalf("cleared %+v for an unconfigured repo", c.calls)
	}
}

func TestClearBlockedFindingRefusesAnIncompleteTarget(t *testing.T) {
	for name, req := range map[string]*DecisionRequest{
		"no repo":  clearRequest("", 42),
		"no issue": clearRequest("octocat/acme", 0),
	} {
		t.Run(name, func(t *testing.T) {
			c := &recordingClearer{}
			if err := ExecuteClearBlockedFinding(context.Background(), c, req, clearOption,
				[]string{"octocat/acme"}); err == nil {
				t.Fatal("want an error for an incomplete target")
			}
			if len(c.calls) != 0 {
				t.Fatalf("cleared %+v from an incomplete target", c.calls)
			}
		})
	}
}

// A mis-dispatched arm must not silently retract a hold.
func TestClearBlockedFindingRefusesAnotherVerbsOption(t *testing.T) {
	c := &recordingClearer{}

	err := ExecuteClearBlockedFinding(context.Background(), c, clearRequest("octocat/acme", 42),
		Option{ID: "cleared", Verb: VerbNoop}, []string{"octocat/acme"})

	if err == nil {
		t.Fatal("want an error when the option binds a different verb")
	}
	if len(c.calls) != 0 {
		t.Fatalf("cleared %+v for a foreign verb", c.calls)
	}
}

// The store CAS-resolves only after the verb returns nil. A surface that cannot
// perform the clear must fail loudly, or the resolution consumes the card and
// leaves the issue deferring with its one affordance gone.
func TestClearBlockedFindingWithoutACapabilityFailsLoudly(t *testing.T) {
	if err := ExecuteClearBlockedFinding(context.Background(), nil,
		clearRequest("octocat/acme", 42), clearOption, []string{"octocat/acme"}); err == nil {
		t.Fatal("a surface without the capability must not report success")
	}
}
