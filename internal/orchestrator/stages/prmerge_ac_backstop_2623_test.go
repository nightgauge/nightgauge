package stages

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/github"
)

// bodyGh is a ghClient that also implements prBodyClient (#2623). It records
// the order of edits and merges so a test can prove the edit lands first.
type bodyGh struct {
	queueGh
	prBody       string
	issueBody    string
	issueErr     error
	edited       []string
	events       []string
	useQueue     bool
	queueEnabled bool
}

func (b *bodyGh) PRBody(context.Context, int) (string, error) { return b.prBody, nil }
func (b *bodyGh) IssueBody(context.Context, int) (string, error) {
	return b.issueBody, b.issueErr
}
func (b *bodyGh) EditPRBody(_ context.Context, _ int, body string) error {
	b.edited = append(b.edited, body)
	b.events = append(b.events, "edit")
	b.prBody = body
	return nil
}
func (b *bodyGh) Merge(ctx context.Context, n int) error {
	b.events = append(b.events, "merge")
	return b.fakeGh.Merge(ctx, n)
}
func (b *bodyGh) EnqueueAndWait(ctx context.Context, st github.MergeQueueStatus, opts github.MergeQueueWaitOptions) (github.MergeQueueStatus, error) {
	b.events = append(b.events, "enqueue")
	return b.queueGh.EnqueueAndWait(ctx, st, opts)
}

// directBodyGh hides the merge-queue methods so the direct-merge path runs.
type directBodyGh struct{ *bodyGh }

func (d directBodyGh) View(ctx context.Context, n int) (PRViewSnapshot, error) {
	return d.bodyGh.View(ctx, n)
}
func (d directBodyGh) Merge(ctx context.Context, n int) error { return d.bodyGh.Merge(ctx, n) }
func (d directBodyGh) PRBody(ctx context.Context, n int) (string, error) {
	return d.bodyGh.PRBody(ctx, n)
}
func (d directBodyGh) IssueBody(ctx context.Context, n int) (string, error) {
	return d.bodyGh.IssueBody(ctx, n)
}
func (d directBodyGh) EditPRBody(ctx context.Context, n int, body string) error {
	return d.bodyGh.EditPRBody(ctx, n, body)
}

// writeBackstopContext writes ac-reconcile-7.json covering criteria and a
// pr-7.json that closes the issue, returning the workdir.
func writeBackstopContext(t *testing.T, criteria ...string) string {
	t.Helper()
	dir := initRepo(t)
	write := func(name string, v any) {
		p := pipelineContextPath(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(v)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var acs []map[string]any
	for i, c := range criteria {
		acs = append(acs, map[string]any{"index": i, "text": c})
	}
	write("ac-reconcile-7.json", map[string]any{"acceptance_criteria": acs})
	write("pr-7.json", map[string]any{
		"pr_number": 42, "pr_url": "https://github.com/o/r/pull/42", "closes_issue": true, "status": "open",
	})
	return dir
}

func newBackstopRunner(gh ghClient) *DeterministicRunner {
	r := newRunnerWith(gh, 42)
	r.knowledgeConformance = nil
	r.steeringGate = nil
	r.mergeabilityPollInterval = 0
	return r
}

func readClosesIssue(t *testing.T, dir string) any {
	t.Helper()
	data, err := os.ReadFile(pipelineContextPath(dir, "pr-7.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.Unmarshal(data, &doc)
	return doc["closes_issue"]
}

const fourACs = "- [ ] one\n- [ ] two\n- [ ] three\n- [ ] four\n"

// (c) A criterion added after reconciliation: the body says Refs #7 and
// lists it before the merge is issued; the merge still happens.
func TestACBackstop_UncoveredACDemotesBeforeMerge(t *testing.T) {
	dir := writeBackstopContext(t, "one", "two", "three", "four")
	b := &bodyGh{
		queueGh:   queueGh{fakeGh: fakeGh{preMerge: cleanOpen, postMerge: PRViewSnapshot{State: "MERGED"}}},
		prBody:    "Summary\n\nCloses #7\n",
		issueBody: fourACs + "- [ ] five added later\n- [x] six already done\n",
	}
	res, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != PathMerged || !res.PartialDelivery || res.PartialDeliveryReason != PartialDeliveryACUncovered {
		t.Fatalf("res = %+v", res)
	}
	if strings.Join(b.events, ",") != "edit,merge" {
		t.Errorf("events = %v, want edit then merge", b.events)
	}
	got := b.edited[0]
	if BodyClosesIssue(got, "o/r", 7) || !strings.Contains(got, "Refs #7") {
		t.Errorf("body still closes #7:\n%s", got)
	}
	if !strings.Contains(got, UnmetACHeading) || !strings.Contains(got, "- five added later") || strings.Contains(got, "six") {
		t.Errorf("unmet section wrong:\n%s", got)
	}
	if v := readClosesIssue(t, dir); v != false {
		t.Errorf("pr-7.json closes_issue = %v, want false", v)
	}
}

// The queue path gets the edit before enqueueing.
func TestACBackstop_EditPrecedesEnqueue(t *testing.T) {
	dir := writeBackstopContext(t, "one")
	b := &bodyGh{
		queueGh: queueGh{
			fakeGh: fakeGh{preMerge: cleanOpen},
			status: github.MergeQueueStatus{PRID: "PR_1", State: "OPEN", QueueRequired: true, BaseRef: "main"},
			final:  github.MergeQueueStatus{State: "MERGED"},
		},
		prBody:    "Fixes: #7",
		issueBody: "- [ ] one\n- [ ] two\n",
	}
	res, _ := newBackstopRunner(b).Run(context.Background(), 7, "", dir)
	if strings.Join(b.events, ",") != "edit,enqueue" || !res.PartialDelivery {
		t.Errorf("events = %v res = %+v", b.events, res)
	}
}

func TestACBackstop_NoEdit(t *testing.T) {
	cases := []struct {
		name      string
		prBody    string
		issueBody string
		issueErr  error
	}{
		{"all covered", "Closes #7", fourACs, nil},
		{"already refs", "Refs #7", fourACs + "- [ ] five\n", nil},
		{"other issue closed", "Closes #8", fourACs + "- [ ] five\n", nil},
		{"issue read error", "Closes #7", "", errors.New("boom")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeBackstopContext(t, "one", "two", "three", "four")
			b := &bodyGh{
				queueGh:   queueGh{fakeGh: fakeGh{preMerge: cleanOpen, postMerge: PRViewSnapshot{State: "MERGED"}}},
				prBody:    tc.prBody,
				issueBody: tc.issueBody,
				issueErr:  tc.issueErr,
			}
			res, _ := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir)
			if len(b.edited) != 0 || res.PartialDelivery || res.Path != PathMerged {
				t.Errorf("edited=%v res=%+v", b.edited, res)
			}
			if v := readClosesIssue(t, dir); v != true {
				t.Errorf("closes_issue = %v, want untouched", v)
			}
		})
	}
}

// A client without prBodyClient skips the backstop entirely.
func TestACBackstop_ClientWithoutInterface(t *testing.T) {
	gh := &fakeGh{preMerge: cleanOpen, postMerge: PRViewSnapshot{State: "MERGED"}}
	res, _ := newBackstopRunner(gh).Run(context.Background(), 7, "", writeBackstopContext(t))
	if res.PartialDelivery || res.Path != PathMerged {
		t.Errorf("res = %+v", res)
	}
}

func TestRewriteClosesToRefs(t *testing.T) {
	in := "Closes #7\nfixes o/r#7 and resolves #70\nClose #8\n" +
		"Resolves https://github.com/o/r/issues/7\nfixes https://github.com/o/r/issues/8\n" +
		"fixes other/repo#7 and closes https://github.com/other/repo/issues/7"
	want := "Refs #7\nRefs o/r#7 and resolves #70\nClose #8\n" +
		"Refs https://github.com/o/r/issues/7\nfixes https://github.com/o/r/issues/8\n" +
		"fixes other/repo#7 and closes https://github.com/other/repo/issues/7"
	if got := RewriteClosesToRefs(in, "o/r", 7); got != want {
		t.Errorf("got %q", got)
	}
}

func TestRewriteAllClosesToRefs(t *testing.T) {
	in := "Closes #7, fixes o/r#8, resolves https://github.com/o/r/issues/9, Refs #10, " +
		"closes https://ghes.example/o/r/issues/11"
	want := "Refs #7, Refs o/r#8, Refs https://github.com/o/r/issues/9, Refs #10, " +
		"Refs https://ghes.example/o/r/issues/11"
	got := RewriteAllClosesToRefs(in)
	if got != want {
		t.Errorf("got %q", got)
	}
	for _, n := range []int{7, 8, 9} {
		if BodyClosesIssue(got, "o/r", n) {
			t.Errorf("rewritten text still closes #%d: %q", n, got)
		}
	}
}

func TestAppendUnmetACs_ExtendsExistingSection(t *testing.T) {
	body := "x\n\n" + UnmetACHeading + "\n\nThis PR leaves #7 open. These criteria remain:\n\n- a\n\n## Next\ny\n"
	got := AppendUnmetACs(body, 7, []string{"b"})
	if strings.Count(got, UnmetACHeading) != 1 || !strings.Contains(got, "- a\n- b\n") || !strings.Contains(got, "## Next\ny") {
		t.Errorf("got:\n%s", got)
	}
}

// The backstop scopes closers to the repository in pr_url (#2687): a URL
// closer for o/r#7 is demoted, another repository's #7 is left alone, and a
// body closing only another repository's #7 is not demoted at all.
func TestACBackstop_ScopesClosersToPRRepository(t *testing.T) {
	run := func(prBody string) *bodyGh {
		t.Helper()
		dir := writeBackstopContext(t, "one", "two", "three", "four")
		b := &bodyGh{
			queueGh:   queueGh{fakeGh: fakeGh{preMerge: cleanOpen, postMerge: PRViewSnapshot{State: "MERGED"}}},
			prBody:    prBody,
			issueBody: fourACs + "- [ ] five added later\n",
		}
		if _, err := newBackstopRunner(directBodyGh{b}).Run(context.Background(), 7, "", dir); err != nil {
			t.Fatal(err)
		}
		return b
	}

	b := run("Closes https://github.com/o/r/issues/7\nFixes other/repo#7\n")
	if len(b.edited) != 1 {
		t.Fatalf("edits = %d, want 1", len(b.edited))
	}
	if got := b.edited[0]; !strings.Contains(got, "Refs https://github.com/o/r/issues/7") ||
		!strings.Contains(got, "Fixes other/repo#7") {
		t.Errorf("body:\n%s", got)
	}

	if b := run("Fixes https://github.com/other/repo/issues/7\n"); len(b.edited) != 0 {
		t.Errorf("a body closing only other/repo#7 was demoted: %q", b.edited)
	}
}

// With no repository to scope by, the backstop fails closed: a closer naming
// #7 in any repository is demoted rather than left to close the issue.
func TestRewriteClosesToRefs_UnknownRepoFailsClosed(t *testing.T) {
	in := "Fixes other/repo#7\nCloses https://github.com/o/r/issues/7\nCloses #8"
	want := "Refs other/repo#7\nRefs https://github.com/o/r/issues/7\nCloses #8"
	if got := RewriteClosesToRefs(in, "", 7); got != want {
		t.Errorf("got %q", got)
	}
	if !BodyClosesIssue("Fixes other/repo#7", "", 7) {
		t.Error("an unknown repository must count every closer of #7")
	}
}

func TestReadPRContextRepo(t *testing.T) {
	dir := writeBackstopContext(t)
	if got := readPRContextRepo(dir, 7); got != "o/r" {
		t.Errorf("readPRContextRepo = %q, want o/r", got)
	}
	if got := readPRContextRepo(dir, 8); got != "" {
		t.Errorf("missing pr-8.json: readPRContextRepo = %q, want empty", got)
	}
}
