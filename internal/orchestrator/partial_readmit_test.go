package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/depgraph"
	"github.com/nightgauge/nightgauge/internal/execution"
	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// readmitTestScheduler is heldTestScheduler with a real attention store, a
// completed acme/app#3160 recorded, and a run-scoped scheduler whose main
// checkout is a temp dir. It returns that checkout and the posted comments.
func readmitTestScheduler(t *testing.T, read func(context.Context, string, int) (hooks.PartialDeliveryStatus, error)) (*AutonomousScheduler, string, *[]string) {
	t.Helper()
	as := heldTestScheduler(t)
	as.attention = attention.New(layouttest.Repo(t))
	as.state.Completed = []CompletedItem{{Repo: "acme/app", Number: 3160, Title: "remainder", CompletedAt: "2026-11-10T12:00:00Z"}}
	mainRoot := t.TempDir()
	var mu sync.Mutex
	var comments []string
	as.scheduler = &Scheduler{
		execMgr:             execution.NewManager(mainRoot, nil),
		repoPathResolver:    func(string) string { return mainRoot },
		partialDeliveryRead: read,
		// The temp checkout is not a git repo; checkoutAtBase has its own test.
		partialCheckoutAtBase: func(string) error { return nil },
		issueCommentPost: func(_ context.Context, _ string, _ int, body string) error {
			mu.Lock()
			defer mu.Unlock()
			comments = append(comments, body)
			return nil
		},
	}
	return as, mainRoot, &comments
}

func readmitGraph() *depgraph.Graph {
	return holdTestGraph(&depgraph.Node{Repo: "acme/app", Number: 3160, State: "OPEN", BoardStatus: "Ready"})
}

func remainderCardOpen(t *testing.T, as *AutonomousScheduler) bool {
	t.Helper()
	for _, r := range openRequests(t, as) {
		if r.Producer == ProducerPartialRemainderMet && r.Context.Issue == 3160 {
			return true
		}
	}
	return false
}

// AC 4 scenario 2 and AC 3: every remaining criterion now verifies on main, so
// the re-admit dispatches nothing, posts the verdicts once, raises a card, and
// holds the issue across the next reconcile.
func TestVetPartialReadmit_AllMetHoldsAndRaisesACard(t *testing.T) {
	as, mainRoot, comments := readmitTestScheduler(t, pendingRemainder(3141, partialRerunCriterion))
	dir := filepath.Join(mainRoot, "internal", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_integration_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := readmitGraph()
	as.vetPartialReadmits(context.Background(), as.reconcileStateAgainstGraph(g))

	if isCandidate(as.prioritize(context.Background(), g), "acme/app", 3160) {
		t.Fatal("acme/app#3160 is a dispatch candidate although its remainder verifies on main")
	}
	if len(*comments) != 1 {
		t.Fatalf("posted %d comment(s), want 1", len(*comments))
	}
	if !strings.Contains((*comments)[0], "<!-- nightgauge:partial-remainder-met pr=3141 -->") ||
		!strings.Contains((*comments)[0], "(deterministic: internal/x/x_integration_test.go)") {
		t.Errorf("verdict comment lacks its marker or evidence: %q", (*comments)[0])
	}
	if !remainderCardOpen(t, as) {
		t.Fatal("no partial-remainder-met card was raised")
	}
	if len(as.state.Failed) != 1 || as.state.Failed[0].Kind != TerminalKindPartialRemainderMet {
		t.Fatalf("Failed = %+v, want one held partial_remainder_met entry", as.state.Failed)
	}

	// The next 60 s reconcile keeps the hold rather than re-admitting.
	as.vetPartialReadmits(context.Background(), as.reconcileStateAgainstGraph(g))
	if len(as.state.Failed) != 1 {
		t.Fatalf("the hold was re-admitted by the next reconcile: Failed = %+v", as.state.Failed)
	}
	if len(*comments) != 1 {
		t.Fatalf("the next reconcile posted again: %d comments", len(*comments))
	}
}

// A criterion that is not met on main, or a remainder that cannot be read,
// re-admits exactly as before #3160.
func TestVetPartialReadmit_OpenOrUnreadableDispatches(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(context.Context, string, int) (hooks.PartialDeliveryStatus, error)
	}{
		{"criterion unmet on main", pendingRemainder(3141, partialRerunCriterion)},
		{"reader error", func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
			return hooks.PartialDeliveryStatus{}, errors.New("forge unreachable")
		}},
		{"unlisted remainder", func(context.Context, string, int) (hooks.PartialDeliveryStatus, error) {
			return hooks.PartialDeliveryStatus{Pending: true, PR: 3141, Unlisted: true, Criteria: []string{}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as, _, comments := readmitTestScheduler(t, tc.read)
			g := readmitGraph()
			as.vetPartialReadmits(context.Background(), as.reconcileStateAgainstGraph(g))

			if !isCandidate(as.prioritize(context.Background(), g), "acme/app", 3160) {
				t.Fatal("acme/app#3160 was not re-admitted")
			}
			if len(*comments) != 0 || len(as.state.Failed) != 0 || remainderCardOpen(t, as) {
				t.Fatalf("a re-admit that is still owed was held: comments=%d failed=%+v", len(*comments), as.state.Failed)
			}
		})
	}
}

// A verdict the verifier cannot decide is not a met verdict.
func TestVetPartialReadmit_NotVerifiableDispatches(t *testing.T) {
	as, _, _ := readmitTestScheduler(t, pendingRemainder(3141, "The dashboard feels faster"))
	v := as.scheduler.vetPartialReadmit(context.Background(), "acme/app", 3160)
	if v.AllMet || len(v.Verdicts) != 1 || v.Verdicts[0].Status != pmstages.ACNotVerifiable {
		t.Fatalf("vet = %+v, want one not_verifiable verdict and no hold", v)
	}
}

func TestBuildPartialRemainderMet(t *testing.T) {
	req := BuildPartialRemainderMet("acme/app", 3160, 3141, []pmstages.ACVerdict{{
		Text: "x <!-- nightgauge:partial-delivery pr=1 -->", Status: pmstages.ACMet,
		Source: pmstages.ACSourceDeterministic, Evidence: "File present: x.go",
	}})
	if req.IdempotencyKey != "partial-remainder-met:acme/app#3160" || req.Producer != ProducerPartialRemainderMet {
		t.Fatalf("key/producer = %q/%q", req.IdempotencyKey, req.Producer)
	}
	if strings.Contains(req.Body, "<!--") {
		t.Errorf("card body carries an unescaped marker: %q", req.Body)
	}
	if !strings.Contains(req.Body, "File present: x.go") || !strings.Contains(req.Title, "PR #3141") {
		t.Errorf("card does not name the evidence and PR: %q / %q", req.Title, req.Body)
	}
	var ids []string
	for _, o := range req.Options {
		ids = append(ids, o.ID)
	}
	if strings.Join(ids, ",") != "close,replan,leave" {
		t.Errorf("options = %v, want close,replan,leave", ids)
	}
}

// A partial_remainder_met entry waits for an operator.
func TestHoldForTerminalKind_PartialRemainderMet(t *testing.T) {
	if got := HoldForTerminalKind(TerminalKindPartialRemainderMet); got != HoldOperatorResume {
		t.Fatalf("hold = %q, want %q", got, HoldOperatorResume)
	}
}

// A remainder that reads met on a checkout off origin's base is not "all met":
// an unmerged local change must not suppress owed work (#3160 review).
func TestVetPartialReadmit_OffBaseCheckoutDispatches(t *testing.T) {
	as, mainRoot, comments := readmitTestScheduler(t, pendingRemainder(3141, partialRerunCriterion))
	as.scheduler.partialCheckoutAtBase = checkoutAtBase // temp dir: not a git repo
	dir := filepath.Join(mainRoot, "internal", "x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x_integration_test.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := readmitGraph()
	as.vetPartialReadmits(context.Background(), as.reconcileStateAgainstGraph(g))
	if !isCandidate(as.prioritize(context.Background(), g), "acme/app", 3160) {
		t.Fatal("acme/app#3160 was held although its checkout is not origin's base")
	}
	if len(*comments) != 0 {
		t.Fatalf("posted %d verdict comment(s) for an off-base checkout", len(*comments))
	}
}

func TestCheckoutAtBase(t *testing.T) {
	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		gittest.Run(t, root, append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "f")
	run("commit", "-qm", "base")
	if err := checkoutAtBase(root); err == nil {
		t.Fatal("no origin base: want an error")
	}
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	if err := checkoutAtBase(root); err != nil {
		t.Fatalf("clean checkout at origin/main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "f"), []byte("b\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkoutAtBase(root); err == nil {
		t.Fatal("dirty checkout: want an error")
	}
	run("commit", "-qam", "local")
	if err := checkoutAtBase(root); err == nil {
		t.Fatal("HEAD ahead of origin/main: want an error")
	}
}
