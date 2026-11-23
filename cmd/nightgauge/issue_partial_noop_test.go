package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/pkg/types"
)

const noopCrit = "`ExcludePattern` keeps its current value"

func noopGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gittest.Run(t, dir, args...)
}

// noopRepo builds a repo whose HEAD is a merge commit that changed only a
// sibling of `ExcludePattern`, makes it the working directory, and returns
// the merge SHA.
func noopRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	noopGit(t, dir, "init", "-q", "-b", "main")
	noopGit(t, dir, "config", "user.email", "test@example.com")
	noopGit(t, dir, "config", "user.name", "Test")
	noopGit(t, dir, "config", "commit.gpgsign", "false")
	write("scan/scan.go", "package scan\n\nconst ExcludePattern = `^---`\n\nfunc match() bool { return true }\n")
	noopGit(t, dir, "add", "-A")
	noopGit(t, dir, "commit", "-qm", "base")
	noopGit(t, dir, "checkout", "-q", "-b", "feat")
	write("scan/scan.go", "package scan\n\nconst ExcludePattern = `^---`\n\nfunc match() bool { return false }\n")
	noopGit(t, dir, "commit", "-qam", "change")
	noopGit(t, dir, "checkout", "-q", "main")
	write("README.md", "x\n")
	noopGit(t, dir, "add", "-A")
	noopGit(t, dir, "commit", "-qm", "main")
	noopGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feat")
	t.Chdir(dir)
	return noopGit(t, dir, "rev-parse", "HEAD")
}

type noopIssues struct{ closed, comments int }

func (f *noopIssues) AddComment(context.Context, string, string) error { f.comments++; return nil }
func (f *noopIssues) EditIssue(context.Context, string, string) (*types.Issue, error) {
	return &types.Issue{}, nil
}
func (f *noopIssues) CloseIssue(context.Context, string) error { f.closed++; return nil }

// stubCloseFactory replaces partialCloseDepsFactory for one test and counts
// its calls. The target's body reads as plain (no Delivery-order refs).
func stubCloseFactory(t *testing.T, issues *noopIssues, boardErr, err error) *int {
	t.Helper()
	return stubCloseFactoryBody(t, issues, "", true, boardErr, err)
}

// stubCloseFactoryBody is stubCloseFactory with the target's body and
// whether it was read set (#3510).
func stubCloseFactoryBody(t *testing.T, issues *noopIssues, body string, bodyRead bool, boardErr, err error) *int {
	t.Helper()
	calls := 0
	prev := partialCloseDepsFactory
	partialCloseDepsFactory = func(_ context.Context, _, _ string, number, pr int) (orchestrator.PartialCloseDeps, orchestrator.PartialCloseTarget, error, error) {
		calls++
		return orchestrator.PartialCloseDeps{Issues: issues},
			orchestrator.PartialCloseTarget{Number: number, NodeID: "I_1", PR: pr, Body: body, BodyRead: bodyRead}, boardErr, err
	}
	t.Cleanup(func() { partialCloseDepsFactory = prev })
	return &calls
}

func runNoop(t *testing.T, a partialNoOpArgs) (partialNoOpOutput, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := runIssuePartialNoOp(context.Background(), &stdout, &stderr, a); err != nil {
		t.Fatal(err)
	}
	var out partialNoOpOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	return out, stderr.String()
}

// Serial: swaps package var partialCloseDepsFactory and changes directory.
func TestIssuePartialNoOp_ApplyClose(t *testing.T) {
	merge := noopRepo(t)
	ws := partialDeliveryWorkspace(context.Background())
	if err := orchestrator.WritePartialDeliveryContext(ws, 3423, orchestrator.PartialDeliveryFile{
		PR: 3422, Criteria: []string{noopCrit}, MergeSHA: merge,
	}); err != nil {
		t.Fatal(err)
	}
	apply := partialNoOpArgs{number: 3423, pr: 3422, criteria: 1, applyClose: true, owner: "o", repo: "r"}

	issues := &noopIssues{}
	calls := stubCloseFactory(t, issues, nil, nil)
	out, _ := runNoop(t, partialNoOpArgs{number: 3423, pr: 3422, criteria: 1})
	if out.Action != orchestrator.PartialNoOpActionClose || out.Applied || *calls != 0 || out.Remainder == nil || !out.Remainder.AllMet {
		t.Fatalf("without --apply-close: %+v, factory calls %d — want close, unapplied, no factory call", out, *calls)
	}

	out, _ = runNoop(t, apply)
	if out.Action != orchestrator.PartialNoOpActionClose || !out.Applied || out.Close == nil || !out.Close.Closed || issues.closed != 1 {
		t.Fatalf("all met with --apply-close: %+v, closed %d — want applied", out, issues.closed)
	}

	issues = &noopIssues{}
	stubCloseFactory(t, issues, errors.New("resolve project: none declared"), nil)
	if out, _ = runNoop(t, apply); !out.Applied || out.BoardError == "" || issues.closed != 1 {
		t.Fatalf("board unresolved: %+v — want the close applied and board_error set", out)
	}

	issues = &noopIssues{}
	stubCloseFactory(t, issues, nil, errors.New("forge down"))
	if out, _ = runNoop(t, apply); out.Action != orchestrator.PartialNoOpActionBook || out.Applied ||
		out.Detail != orchestrator.PartialCloseFailedDetail(3423, 3422, errors.New("forge down")) || issues.closed != 0 {
		t.Fatalf("factory error: %+v — want book with the shared failure detail", out)
	}

	// #3510: a Delivery order that says refs, or an unread body, holds the
	// close: nothing is written and the run books with the held detail.
	for name, tc := range map[string]struct {
		body     string
		bodyRead bool
		held     string
	}{
		"refs":   {"## Delivery order\n\nEach PR refs #3423.\n", true, orchestrator.HoldDeliveryOrderRefs},
		"unread": {"", false, orchestrator.HoldIssueBodyUnread},
	} {
		issues = &noopIssues{}
		stubCloseFactoryBody(t, issues, tc.body, tc.bodyRead, nil, nil)
		if out, _ = runNoop(t, apply); out.Action != orchestrator.PartialNoOpActionBook || out.Applied ||
			out.Close == nil || out.Close.Held != tc.held || out.Close.Closed ||
			out.Detail != orchestrator.PartialCloseHeldDetail(3423, 3422, tc.held) || issues.closed != 0 || issues.comments != 0 {
			t.Fatalf("%s: %+v, closed %d comments %d — want book, held %q, nothing written", name, out, issues.closed, issues.comments, tc.held)
		}
	}
	issues = &noopIssues{}
	stubCloseFactoryBody(t, issues, "## Delivery order\n\nOne PR closes it.\n", true, nil, nil)
	if out, _ = runNoop(t, apply); !out.Applied || out.Close == nil || !out.Close.Closed || out.Close.Held != "" || issues.closed != 1 {
		t.Fatalf("plain Delivery order: %+v — want closed as before", out)
	}

	noRepo := apply
	noRepo.owner, noRepo.repo = "", ""
	if out, _ = runNoop(t, noRepo); out.Action != orchestrator.PartialNoOpActionBook || !strings.Contains(out.Detail, "closing failed") {
		t.Fatalf("no owner/repo: %+v — want book", out)
	}

	otherPR := apply
	otherPR.pr = 99
	issues = &noopIssues{}
	calls = stubCloseFactory(t, issues, nil, nil)
	if out, stderr := runNoop(t, otherPR); out.Action != orchestrator.PartialNoOpActionBook || *calls != 0 ||
		!strings.Contains(stderr, "names PR #3422") {
		t.Fatalf("record names another PR: %+v (%s) — want book with no close", out, stderr)
	}

	if err := orchestrator.RemovePartialDeliveryContext(ws, 3423); err != nil {
		t.Fatal(err)
	}
	if out, stderr := runNoop(t, apply); out.Action != orchestrator.PartialNoOpActionBook || *calls != 0 ||
		!strings.Contains(stderr, "no partial-delivery record") {
		t.Fatalf("missing record: %+v (%s) — want book with no close", out, stderr)
	}
}
