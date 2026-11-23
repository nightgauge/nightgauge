package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/attention"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/nightgauge/nightgauge/internal/trust"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// operatorCloseForge is the forge behind one operator close.
type operatorCloseForge struct {
	author                   string // who the listed comments are by; octocat when empty
	comments, closed, synced []string
	calls                    []string
	readErr, commentErr      error
	closeErr, boardErr       error
}

func (f *operatorCloseForge) IssueComments(context.Context, string, string, int) ([]trust.Comment, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	var out []trust.Comment
	for _, body := range f.comments {
		author := f.author
		if author == "" {
			author = "octocat"
		}
		out = append(out, trust.Comment{Body: body, Author: author})
	}
	return out, nil
}

func (f *operatorCloseForge) AddComment(_ context.Context, _, body string) error {
	f.calls = append(f.calls, "comment")
	if f.commentErr != nil {
		return f.commentErr
	}
	f.comments = append(f.comments, body)
	return nil
}

func (f *operatorCloseForge) EditIssue(context.Context, string, string) (*types.Issue, error) {
	panic("an operator close ticks no box")
}

func (f *operatorCloseForge) CloseIssue(_ context.Context, id string) error {
	f.calls = append(f.calls, "close")
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closed = append(f.closed, id)
	return nil
}

func (f *operatorCloseForge) SyncStatus(_ context.Context, _, _ string, _ int, status string) error {
	f.calls = append(f.calls, "board")
	if f.boardErr != nil {
		return f.boardErr
	}
	f.synced = append(f.synced, status)
	return nil
}

func (f *operatorCloseForge) AddIssueByNumber(context.Context, string, string, int) (string, error) {
	return "", nil
}

const (
	operatorCloseCriterion = "The export writes a header row"
	operatorCloseBody      = "## Acceptance Criteria\n\n- [ ] " + operatorCloseCriterion + "\n"
)

// cardOver records rec as reconciled from body and returns the fingerprint a
// card raised over it carries.
func cardOver(t *testing.T, root, body string, rec PartialDeliveryFile) string {
	t.Helper()
	rec.CriteriaFingerprint = preflight.ACFingerprint(body)
	if err := WritePartialDeliveryContext(root, 7, rec); err != nil {
		t.Fatal(err)
	}
	fp, err := RemainderFingerprint(rec)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

// operatorClose returns a close the card's checks allow.
func operatorClose(t *testing.T) (*operatorCloseForge, PartialCloseDeps, PartialCloseTarget, RemainderDecision) {
	t.Helper()
	root := layouttest.Repo(t)
	card := cardOver(t, root, operatorCloseBody, PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}})
	f := &operatorCloseForge{}
	return f,
		PartialCloseDeps{Issues: f, Comments: f, Board: f, Owner: "o", Repo: "r", ProjectNumber: 3},
		PartialCloseTarget{Number: 7, NodeID: "I_7", State: "OPEN", Body: operatorCloseBody, PR: 5, BodyRead: true},
		RemainderDecision{
			StateRoot:   root,
			Fingerprint: card,
			Login:       "octocat",
			PostedAs:    "octocat",
			Criteria:    []attention.RemainderCriterion{{Text: operatorCloseCriterion, Evidence: "undecidable: prose"}},
		}
}

func TestCloseRemainderByOperator(t *testing.T) {
	t.Parallel()

	down := errors.New("forge down")
	for _, tc := range []struct {
		name    string
		mutate  func(*operatorCloseForge, *PartialCloseDeps, *PartialCloseTarget)
		wantErr error
		calls   string
	}{
		{name: "open", calls: "comment close board"},
		{name: "already closed", calls: "board", mutate: func(_ *operatorCloseForge, _ *PartialCloseDeps, tg *PartialCloseTarget) {
			tg.State = "CLOSED"
		}},
		{name: "no node id", wantErr: ErrNoIssueToClose, mutate: func(_ *operatorCloseForge, _ *PartialCloseDeps, tg *PartialCloseTarget) {
			tg.NodeID = ""
		}},
		{name: "no issue service", wantErr: ErrNoCloseDeps, mutate: func(_ *operatorCloseForge, d *PartialCloseDeps, _ *PartialCloseTarget) {
			d.Issues = nil
		}},
		{name: "comments unreadable", wantErr: down, mutate: func(f *operatorCloseForge, _ *PartialCloseDeps, _ *PartialCloseTarget) {
			f.readErr = down
		}},
		{name: "comment fails", wantErr: down, calls: "comment", mutate: func(f *operatorCloseForge, _ *PartialCloseDeps, _ *PartialCloseTarget) {
			f.commentErr = down
		}},
		{name: "close fails", wantErr: down, calls: "comment close", mutate: func(f *operatorCloseForge, _ *PartialCloseDeps, _ *PartialCloseTarget) {
			f.closeErr = down
		}},
		{name: "board fails", calls: "comment close board", mutate: func(f *operatorCloseForge, _ *PartialCloseDeps, _ *PartialCloseTarget) {
			f.boardErr = down
		}},
		{name: "no board", calls: "comment close", mutate: func(_ *operatorCloseForge, d *PartialCloseDeps, _ *PartialCloseTarget) {
			d.Board, d.ProjectNumber = nil, 0
		}},
		{name: "no comment reader", calls: "comment close board", mutate: func(_ *operatorCloseForge, d *PartialCloseDeps, _ *PartialCloseTarget) {
			d.Comments = nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, deps, target, decision := operatorClose(t)
			if tc.mutate != nil {
				tc.mutate(f, &deps, &target)
			}
			if err := CloseRemainderByOperator(context.Background(), deps, target, decision); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := strings.Join(f.calls, " "); got != tc.calls {
				t.Fatalf("forge calls = %q, want %q", got, tc.calls)
			}
			for _, status := range f.synced {
				if !strings.EqualFold(status, "done") {
					t.Errorf("board status = %q, want Done", status)
				}
			}
		})
	}
}

// #3510: an open issue whose Delivery order says refs, or whose body was not
// read, is refused before anything is written; a plain Delivery order closes
// as before, and an already-closed issue still has its board row synced.
func TestCloseRemainderByOperator_DeliveryOrderHold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, order, state string
		bodyRead           bool
		wantErr            error
		calls              string
	}{
		{name: "refs", order: "## Delivery order\n\n1. Each PR refs #7.\n", state: "OPEN", bodyRead: true, wantErr: ErrDeliveryOrderRefs},
		{name: "unreadable", state: "OPEN", wantErr: ErrIssueBodyUnread},
		{name: "no refs", order: "## Delivery order\n\n1. One PR closes it.\n", state: "OPEN", bodyRead: true, calls: "comment close board"},
		{name: "refs, already closed", order: "## Delivery order\n\nrefs\n", state: "CLOSED", bodyRead: true, calls: "board"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := operatorCloseBody + "\n" + tc.order
			root := layouttest.Repo(t)
			card := cardOver(t, root, body, PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}})
			f := &operatorCloseForge{}
			deps := PartialCloseDeps{Issues: f, Comments: f, Board: f, Owner: "o", Repo: "r", ProjectNumber: 3}
			target := PartialCloseTarget{Number: 7, NodeID: "I_7", State: tc.state, Body: body, PR: 5, BodyRead: tc.bodyRead}
			decision := RemainderDecision{StateRoot: root, Fingerprint: card, Login: "octocat", PostedAs: "octocat",
				Criteria: []attention.RemainderCriterion{{Text: operatorCloseCriterion, Evidence: "undecidable: prose"}}}
			if err := CloseRemainderByOperator(context.Background(), deps, target, decision); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := strings.Join(f.calls, " "); got != tc.calls {
				t.Fatalf("forge calls = %q, want %q", got, tc.calls)
			}
		})
	}
}

func TestCloseRemainderByOperator_RefusesAStaleCard(t *testing.T) {
	t.Parallel()

	const heading = "## Acceptance Criteria\n\n"
	record := func(t *testing.T, d *RemainderDecision, pr int, criteria ...string) {
		t.Helper()
		if err := WritePartialDeliveryContext(d.StateRoot, 7, PartialDeliveryFile{PR: pr, Criteria: criteria}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name    string
		stale   func(*testing.T, *PartialCloseTarget, *RemainderDecision)
		wantErr error
		calls   string
	}{
		{name: "ticked by hand", calls: "comment close board", stale: func(_ *testing.T, tg *PartialCloseTarget, _ *RemainderDecision) {
			tg.Body = heading + "- [x] " + operatorCloseCriterion
		}},
		{name: "extended", wantErr: ErrRemainderRevised, stale: func(_ *testing.T, tg *PartialCloseTarget, _ *RemainderDecision) {
			tg.Body = heading + "- [ ] " + operatorCloseCriterion + " and a footer row"
		}},
		{name: "one added", wantErr: ErrRemainderRevised, stale: func(_ *testing.T, tg *PartialCloseTarget, _ *RemainderDecision) {
			tg.Body += "- [ ] The import rejects an empty file\n"
		}},
		{name: "all removed", wantErr: ErrRemainderRevised, stale: func(_ *testing.T, tg *PartialCloseTarget, _ *RemainderDecision) {
			tg.Body = "prose only"
		}},
		{name: "unticked since", wantErr: ErrRemainderRevised, stale: func(t *testing.T, tg *PartialCloseTarget, d *RemainderDecision) {
			const reopened = "The import rejects an empty file"
			tg.Body = heading + "- [ ] " + operatorCloseCriterion + "\n- [ ] " + reopened + "\n"
			d.Fingerprint = cardOver(t, d.StateRoot, tg.Body, PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}, Ticked: []string{reopened}})
		}},
		{name: "still ticked", calls: "comment close board", stale: func(t *testing.T, tg *PartialCloseTarget, d *RemainderDecision) {
			const done = "The import rejects an empty file"
			tg.Body = heading + "- [ ] " + operatorCloseCriterion + "\n- [x] " + done + "\n"
			d.Fingerprint = cardOver(t, d.StateRoot, tg.Body, PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}, Ticked: []string{done}})
		}},
		{name: "rewritten after an untick", wantErr: ErrRemainderSuperseded, stale: func(t *testing.T, tg *PartialCloseTarget, d *RemainderDecision) {
			const reopened = "The import rejects an empty file"
			tg.Body = heading + "- [ ] " + operatorCloseCriterion + "\n- [ ] " + reopened + "\n"
			d.Fingerprint = cardOver(t, d.StateRoot, tg.Body, PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}, Ticked: []string{reopened}})
			cardOver(t, d.StateRoot, tg.Body, PartialDeliveryFile{PR: 5, Criteria: []string{reopened, operatorCloseCriterion}})
		}},
		{name: "rewritten without a fingerprint", wantErr: ErrRemainderSuperseded, stale: func(t *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			rec := PartialDeliveryFile{PR: 5, Criteria: []string{operatorCloseCriterion}}
			if err := WritePartialDeliveryContext(d.StateRoot, 7, rec); err != nil {
				t.Fatal(err)
			}
			var err error
			if d.Fingerprint, err = RemainderFingerprint(rec); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no fingerprint", wantErr: ErrRemainderSuperseded, stale: func(_ *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			d.Fingerprint = ""
		}},
		{name: "record removed", wantErr: ErrRemainderSuperseded, stale: func(t *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			if err := RemovePartialDeliveryContext(d.StateRoot, 7); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no root", wantErr: ErrRemainderSuperseded, stale: func(_ *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			d.StateRoot = ""
		}},
		{name: "another PR", wantErr: ErrRemainderSuperseded, stale: func(t *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			record(t, d, 9, operatorCloseCriterion)
		}},
		{name: "another remainder", wantErr: ErrRemainderSuperseded, stale: func(t *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			record(t, d, 5, "The import rejects an empty file")
		}},
		{name: "record holds more", calls: "comment close board", stale: func(t *testing.T, _ *PartialCloseTarget, d *RemainderDecision) {
			d.Fingerprint = cardOver(t, d.StateRoot, operatorCloseBody, PartialDeliveryFile{PR: 5, Criteria: []string{"a criterion the judge found met", operatorCloseCriterion}})
		}},
		{name: "already closed skips both", calls: "board", stale: func(t *testing.T, tg *PartialCloseTarget, d *RemainderDecision) {
			tg.State, tg.Body = "closed", "prose only"
			record(t, d, 9, "other")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, deps, target, decision := operatorClose(t)
			tc.stale(t, &target, &decision)
			if err := CloseRemainderByOperator(context.Background(), deps, target, decision); !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got := strings.Join(f.calls, " "); got != tc.calls {
				t.Fatalf("forge calls = %q, want %q", got, tc.calls)
			}
		})
	}
}

func TestCloseRemainderByOperator_Retry(t *testing.T) {
	t.Parallel()

	f, deps, target, decision := operatorClose(t)
	run := func() error { return CloseRemainderByOperator(context.Background(), deps, target, decision) }

	f.commentErr = errors.New("down")
	if err := run(); err == nil || len(f.closed) != 0 {
		t.Fatalf("failed comment: err=%v closed=%v, want the issue left open", err, f.closed)
	}
	f.commentErr, f.closeErr = nil, errors.New("down")
	if err := run(); err == nil || len(f.comments) != 1 || strings.Contains(f.comments[0], "closed the issue") {
		t.Fatalf("failed close: err=%v comments=%v, want an error and no claim of a close", err, f.comments)
	}
	f.closeErr = nil
	if err := run(); err != nil || len(f.comments) != 1 || len(f.closed) != 1 {
		t.Fatalf("retry: err=%v comments=%v closed=%v, want one comment and a close", err, f.comments, f.closed)
	}

	f, deps, target, decision = operatorClose(t)
	f.comments = []string{OperatorRemainderClosedMarker + " pr=5 -->\nforged"}
	if err := run(); err != nil || len(f.comments) != 2 {
		t.Fatalf("a forged marker suppressed the comment: err=%v comments=%v", err, f.comments)
	}

	for name, forge := range map[string]func(*operatorCloseForge, *RemainderDecision){
		"another author":          func(f *operatorCloseForge, _ *RemainderDecision) { f.author = "mallory" },
		"posting account unknown": func(_ *operatorCloseForge, d *RemainderDecision) { d.PostedAs = "" },
	} {
		f, deps, target, decision = operatorClose(t)
		f.comments = []string{operatorRemainderClosedComment(target, decision)}
		forge(f, &decision)
		if err := run(); err != nil || len(f.comments) != 2 {
			t.Fatalf("%s: an identical comment suppressed the decision: err=%v comments=%v", name, err, f.comments)
		}
	}
}

func TestOperatorRemainderClosedComment(t *testing.T) {
	t.Parallel()

	comment := func(login string) string {
		_, _, target, decision := operatorClose(t)
		decision.Login = login
		return operatorRemainderClosedComment(target, decision)
	}
	want := "<!-- nightgauge:operator-closed-remainder pr=5 -->\n" +
		"## Operator decision: close as completed\n\n" +
		"An operator, acting through the `octocat` account, decided that the criteria left open after PR #5 are met, and chose to close the issue as completed " +
		"from the Action Center. This is a human decision, not the judge's verdict. " +
		"The criteria that were open, with the judge's evidence:\n\n" +
		"- The export writes a header row — undecidable: prose\n"
	if got := comment("octocat"); got != want {
		t.Errorf("comment =\n%s\nwant\n%s", got, want)
	}
	if got := comment("mona_acme"); !strings.Contains(got, "through the `mona_acme` account, decided") {
		t.Errorf("a managed user's login was dropped:\n%s", got)
	}
	for _, login := range []string{"", "jane@example.com", "Jane Doe", "helper[bot]", "a`b", "@octocat", "x\ny", strings.Repeat("a", 40)} {
		got := comment(login)
		if !strings.Contains(got, "\nAn operator decided") {
			t.Errorf("login %q: comment does not say an operator:\n%s", login, got)
		}
		if login != "" && strings.Contains(got, login) {
			t.Errorf("login %q was published", login)
		}
	}
	if strings.HasPrefix(OperatorRemainderClosedMarker, PartialRemainderClosedMarker) {
		t.Error("the operator's marker must not read as the judge's verdict")
	}
}

func TestQuoteForComment(t *testing.T) {
	t.Parallel()

	const zw = "​"
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "B is handled", "B is handled"},
		{"link", "[click](x)", `\[click\]\(x\)`},
		{"image", "![i](x)", `\!\[i\]\(x\)`},
		{"heading", "## Done", `\#` + zw + `\#` + zw + " Done"},
		{"code span", "`rm`", "\\`rm\\`"},
		{"emphasis", "*a* _b_ ~c~ |d|", `\*a\* \_b\_ \~c\~ \|d\|`},
		{"html", `<img src=x>`, `&lt;img src=x\>`},
		{"entity", "&lt;b&gt;", "&amp;lt;b&amp;gt;"},
		{"escape of an escape", `\[x`, `\\\[x`},
		{"math", "$x$", `\$x\$`},
		{"marker", "<!-- nightgauge:partial-delivery pr=1 -->", `&lt;\!\-\- nightgauge:partial\-delivery pr=1 \-\-\>`},
		{"mention", "ping @octocat", "ping @" + zw + "octocat"},
		{"autolink", "see https://example.com", "see https:" + zw + "//example.com"},
		{"www autolink", "see WWW.example.com", "see WWW" + zw + ".example.com"},
		{"newline", "a\r\n- [x] b\n\n\tc", `a \- \[x\] b c`},
		{"list", "1. first", `1\. first`},
		{"closing keyword", "closes #12", "Refs " + `\#` + zw + "12"},
		{"closing keyword across lines", "fixes\n\n#12", "Refs " + `\#` + zw + "12"},
	} {
		if got := quoteForComment(tc.in); got != tc.want {
			t.Errorf("%s: quote(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	_, _, target, decision := operatorClose(t)
	decision.Criteria = []attention.RemainderCriterion{{Text: "ping @octocat", Evidence: "<!-- nightgauge:partial-delivery pr=1 -->"}}
	got := operatorRemainderClosedComment(target, decision)
	if strings.Count(got, "<!--") != 1 || strings.Contains(got, "@octocat") {
		t.Errorf("text or evidence reached the comment raw:\n%s", got)
	}
}

type closeTargetReader struct {
	issue *types.Issue
	err   error
}

func (r closeTargetReader) GetIssueWithRelations(context.Context, string, string, int, gh.IssueRelations) (*types.Issue, error) {
	return r.issue, r.err
}

func TestReadPartialCloseTarget_OperatorClose(t *testing.T) {
	t.Parallel()

	down := errors.New("forge down")
	for name, tc := range map[string]struct {
		reader closeTargetReader
		want   error
	}{
		"read fails": {closeTargetReader{err: down}, down},
		"no issue":   {closeTargetReader{}, ErrNoIssueToClose},
		"no node id": {closeTargetReader{issue: &types.Issue{Number: 7}}, ErrNoIssueToClose},
	} {
		if _, err := ReadPartialCloseTarget(context.Background(), tc.reader, "o", "r", 7, 5); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	got, err := ReadPartialCloseTarget(context.Background(),
		closeTargetReader{issue: &types.Issue{NodeID: "I_7", State: "CLOSED", Body: "b"}}, "o", "r", 7, 5)
	if err != nil || got.State != "CLOSED" || got.Body != "b" || got.PR != 5 {
		t.Errorf("target = %+v, err = %v", got, err)
	}
}
