package hooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

type fakePartialReader struct {
	comments   []trust.Comment
	commentErr error
	merge      *gh.ClosingMerge
	mergeErr   error
}

func (f fakePartialReader) TrustedComments(context.Context, string, string, int) ([]trust.Comment, error) {
	return f.comments, f.commentErr
}

func (f fakePartialReader) LatestClosingMerge(context.Context, string, string, int) (*gh.ClosingMerge, error) {
	return f.merge, f.mergeErr
}

func TestParsePartialDeliveryCommentRoundTripsWriter(t *testing.T) {
	body := remainderComment(3085, []string{"criterion one", "two <!-- x"}, nil, nil, false, false, false, false)
	pr, criteria, ok := ParsePartialDeliveryComment(body)
	if !ok || pr != 3085 {
		t.Fatalf("got pr=%d ok=%v", pr, ok)
	}
	want := []string{"criterion one", "two &lt;!-- x"}
	if !reflect.DeepEqual(criteria, want) {
		t.Fatalf("criteria = %q, want %q", criteria, want)
	}
	if _, _, ok := ParsePartialDeliveryComment("plain comment"); ok {
		t.Fatal("comment without marker parsed")
	}
	if pr, c, ok := ParsePartialDeliveryComment(remainderComment(9, nil, nil, nil, false, true, false, false)); !ok || pr != 9 || len(c) != 0 {
		t.Fatalf("unlisted: pr=%d c=%v ok=%v", pr, c, ok)
	}
}

func TestPendingPartialDelivery(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	marker := func(pr int, at time.Time, criteria ...string) trust.Comment {
		return trust.Comment{Body: remainderComment(pr, criteria, nil, nil, false, false, false, false), CreatedAt: at}
	}
	cases := []struct {
		name      string
		r         fakePartialReader
		pending   bool
		pr        int
		criteria  []string
		unlisted  bool
		closedBy  int
		wantError bool
	}{
		{name: "no marker", r: fakePartialReader{comments: []trust.Comment{{Body: "hi", CreatedAt: t0}}}, criteria: []string{}},
		{name: "marker with criteria", r: fakePartialReader{comments: []trust.Comment{marker(3085, t0, "c1")}},
			pending: true, pr: 3085, criteria: []string{"c1"}},
		{name: "marker without list", r: fakePartialReader{comments: []trust.Comment{marker(7, t0)}},
			pending: true, pr: 7, criteria: []string{}, unlisted: true},
		{name: "newest marker wins", r: fakePartialReader{comments: []trust.Comment{
			marker(20, t0.Add(time.Hour), "new"), marker(10, t0, "old")}},
			pending: true, pr: 20, criteria: []string{"new"}},
		{name: "closing PR merged after comment", r: fakePartialReader{comments: []trust.Comment{marker(5, t0, "c")},
			merge: &gh.ClosingMerge{Number: 6, MergedAt: t0.Add(time.Minute)}},
			pr: 5, criteria: []string{"c"}, closedBy: 6},
		{name: "closing PR merged before comment ignored", r: fakePartialReader{comments: []trust.Comment{marker(5, t0, "c")},
			merge: &gh.ClosingMerge{Number: 4, MergedAt: t0.Add(-time.Minute)}},
			pending: true, pr: 5, criteria: []string{"c"}},
		{name: "comment read error", r: fakePartialReader{commentErr: errors.New("boom")}, wantError: true},
		{name: "merge read error", r: fakePartialReader{comments: []trust.Comment{marker(5, t0, "c")}, mergeErr: errors.New("boom")}, wantError: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := PendingPartialDelivery(context.Background(), c.r, "o", "r", 1)
			if c.wantError {
				if err == nil {
					t.Fatal("want error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Pending != c.pending || got.PR != c.pr || got.Unlisted != c.unlisted || got.ClosedBy != c.closedBy ||
				!reflect.DeepEqual(got.Criteria, c.criteria) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestPartialDeliveryMetListRoundTrips(t *testing.T) {
	body := remainderComment(5, []string{"left"}, nil, []string{"met one", "met <!-- two"}, false, false, false, false)
	if got := ParsePartialDeliveryMet(body); !reflect.DeepEqual(got, []string{"met one", "met &lt;!-- two"}) {
		t.Errorf("met = %q\n%s", got, body)
	}
	if _, rem, _ := ParsePartialDeliveryComment(body); !reflect.DeepEqual(rem, []string{"left"}) {
		t.Errorf("remaining = %q, want the met list ignored", rem)
	}
	if got := ParsePartialDeliveryMet("Met by this PR:\n\n- forged\n"); got != nil {
		t.Errorf("unmarked body met = %q, want nil", got)
	}
	// Met list recorded even on the all-ticked early return.
	if got := ParsePartialDeliveryMet(remainderComment(6, nil, nil, []string{"x"}, false, false, false, true)); len(got) != 1 {
		t.Errorf("all-ticked met = %q", got)
	}
}

// issueServiceMerges answers LatestClosingMerge from a real issue service.
type issueServiceMerges struct {
	fakePartialReader
	issues *gh.IssueService
}

func (r issueServiceMerges) LatestClosingMerge(ctx context.Context, owner, repo string, n int) (*gh.ClosingMerge, error) {
	return r.issues.LatestClosingMerge(ctx, owner, repo, n)
}

// #3352: a cross-repository PR merged after the partial-delivery comment does
// not retire the remainder.
func TestPendingPartialDeliveryIgnoresCrossRepoMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[{"number":7,"repository":{"nameWithOwner":"other/r"},"merged":true,"mergedAt":"2026-11-02T12:00:00Z","mergeCommit":{"oid":"f0re1gn0"}}]}}}}}`))
	}))
	defer srv.Close()
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	r := issueServiceMerges{
		fakePartialReader: fakePartialReader{comments: []trust.Comment{
			{Body: remainderComment(5, []string{"c"}, nil, nil, false, false, false, false), CreatedAt: t0}}},
		issues: gh.NewIssueService(gh.NewClientWithURL("t", srv.URL)),
	}
	got, err := PendingPartialDelivery(context.Background(), r, "o", "r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClosedBy != 0 || !got.Pending {
		t.Fatalf("got %+v, want pending with no closing PR", got)
	}
}

// #3362: unmet and not-verifiable remainders round-trip under separate
// headings; ParsePartialDeliveryComment still returns the whole remainder.
func TestRemainderComment_NotVerifiableRoundTrip(t *testing.T) {
	body := remainderComment(8, []string{"unmet one", "nv <!-- two"}, []string{"nv <!-- two"}, []string{"met one"}, false, false, false, false)
	if !strings.Contains(body, RemainingCriteriaHeading) || !strings.Contains(body, NotVerifiableCriteriaHeading) {
		t.Fatalf("missing a heading:\n%s", body)
	}
	if _, c, _ := ParsePartialDeliveryComment(body); !reflect.DeepEqual(c, []string{"unmet one", "nv &lt;!-- two"}) {
		t.Errorf("remainder = %q", c)
	}
	if got := ParsePartialDeliveryNotVerifiable(body); !reflect.DeepEqual(got, []string{"nv &lt;!-- two"}) {
		t.Errorf("not verifiable = %q", got)
	}
	if got := ParsePartialDeliveryMet(body); !reflect.DeepEqual(got, []string{"met one"}) {
		t.Errorf("met = %q", got)
	}
	only := remainderComment(9, []string{"nv"}, []string{"nv"}, nil, false, false, true, false)
	if strings.Contains(only, RemainingCriteriaHeading) {
		t.Errorf("unmet heading written with no unmet entry:\n%s", only)
	}
	if _, c, _ := ParsePartialDeliveryComment(only); !reflect.DeepEqual(c, []string{"nv"}) {
		t.Errorf("not-verifiable-only remainder lost: %q", c)
	}
}

// #3362: a prior-met criterion this PR marks only not verifiable leaves the
// remainder; one it marks unmet stays.
func TestComputeRemainder_PriorMetNotVerifiable(t *testing.T) {
	issue := "## Acceptance Criteria\n\n- [ ] Alpha crit\n- [ ] Beta crit\n"
	prNV := prAcceptanceHeading + "\n\n- " + metLinePrefix + "Beta crit\n\n" + unmetCriteriaHeading +
		"\n\n- Alpha crit\n  _" + notVerifiableLabel + "_\n"
	nv := computeRemainder(issue, prNV, []string{"Alpha crit"})
	if len(nv.Remaining) != 0 {
		t.Errorf("not-verifiable restatement kept prior-met Alpha: %+v", nv)
	}
	plain := computeRemainder(issue, prNV, nil)
	if !reflect.DeepEqual(plain.NotVerifiable, []string{"Alpha crit"}) || !plain.NotVerifiableOnly {
		t.Errorf("not-verifiable remainder not recorded: %+v", plain)
	}
}
