package hooks

import (
	"context"
	"errors"
	"reflect"
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
	body := remainderComment(3085, []string{"criterion one", "two <!-- x"}, false, false, false, false)
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
	if pr, c, ok := ParsePartialDeliveryComment(remainderComment(9, nil, false, true, false, false)); !ok || pr != 9 || len(c) != 0 {
		t.Fatalf("unlisted: pr=%d c=%v ok=%v", pr, c, ok)
	}
}

func TestPendingPartialDelivery(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	marker := func(pr int, at time.Time, criteria ...string) trust.Comment {
		return trust.Comment{Body: remainderComment(pr, criteria, false, false, false, false), CreatedAt: at}
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
