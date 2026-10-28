package orchestrator

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// closingMerges answers LatestClosingMerge by "owner/repo#N"; a key with no
// entry has no merged closing PR.
type closingMerges struct {
	byKey map[string]*gh.ClosingMerge
	err   error
}

func (c *closingMerges) LatestClosingMerge(_ context.Context, owner, repo string, number int) (*gh.ClosingMerge, error) {
	return c.byKey[owner+"/"+repo+"#"+strconv.Itoa(number)], c.err
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func marker(sha, created string) trust.Comment {
	return trust.Comment{Author: "bot", Body: forge.ValidationMarkerComment("main", sha), CreatedAt: at(created)}
}

func TestMarkerValidates(t *testing.T) {
	legacy := func(created string) trust.Comment {
		return trust.Comment{Body: "<!-- opencode-program:validated -->", CreatedAt: at(created)}
	}
	merge := &gh.ClosingMerge{Number: 2, SHA: "bbbb", MergedAt: at("2026-10-27T12:00:00Z")}
	tests := []struct {
		name   string
		cs     []trust.Comment
		latest *gh.ClosingMerge
		want   bool
	}{
		{"no marker", []trust.Comment{{Body: "lgtm"}}, merge, false},
		{"no closing merge", []trust.Comment{marker("aaaa", "2026-10-27T10:00:00Z")}, nil, true},
		{"marker names the latest merge", []trust.Comment{marker("BBBB", "2026-10-27T11:00:00Z")}, merge, true},
		{"(c) stale marker older than a newer merge is blocked", []trust.Comment{marker("aaaa", "2026-10-27T10:00:00Z")}, merge, false},
		{"other-SHA marker written after the merge passes", []trust.Comment{marker("cccc", "2026-10-27T13:00:00Z")}, merge, true},
		{"legacy marker before the merge", []trust.Comment{legacy("2026-10-27T10:00:00Z")}, merge, false},
		{"legacy marker after the merge", []trust.Comment{legacy("2026-10-27T13:00:00Z")}, merge, true},
		{"newest marker wins whatever the order", []trust.Comment{
			marker("aaaa", "2026-10-27T10:00:00Z"), marker("bbbb", "2026-10-27T12:30:00Z"), marker("0000", "2026-10-27T09:00:00Z"),
		}, merge, true},
		{"older marker for the merge loses to a newer stale one", []trust.Comment{
			marker("dddd", "2026-10-27T11:30:00Z"), marker("bbbb", "2026-10-27T11:00:00Z"),
		}, merge, false},
	}
	for _, tc := range tests {
		if got := MarkerValidates(tc.cs, tc.latest); got != tc.want {
			t.Errorf("%s: MarkerValidates = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The #2611 real case: the newest marker names dadc5798 (PR #2619), the
// issue was reopened and closed again by PR #2628 at 2346803d, merged after
// the marker. It needs a marker for 2346803d.
func TestMarkerValidates_Issue2611(t *testing.T) {
	cs := []trust.Comment{marker("dadc5798", "2026-10-27T21:00:00Z")}
	latest := &gh.ClosingMerge{Number: 2628, SHA: "2346803d", MergedAt: at("2026-10-27T23:52:45Z")}
	if MarkerValidates(cs, latest) {
		t.Fatal("#2611 validated by the dadc5798 marker; want it to need a marker for 2346803d")
	}
	cs = append(cs, marker("2346803d", "2026-10-27T23:58:00Z"))
	if !MarkerValidates(cs, latest) {
		t.Fatal("#2611 with a marker for 2346803d is not validated")
	}
}

// The dependency gate holds a program blocker whose marker is older than its
// latest closing merge, and fails closed on a nil or failing merge reader.
func TestResolveValidationByKey_StaleMarkerBlocks(t *testing.T) {
	comments := &markerComments{byKey: map[string][]trust.Comment{
		"o/a#1": {marker("dadc5798", "2026-10-27T21:00:00Z")},
	}}
	merges := &closingMerges{byKey: map[string]*gh.ClosingMerge{
		"o/a#1": {Number: 2628, SHA: "2346803d", MergedAt: at("2026-10-27T23:52:45Z")},
	}}
	known := map[string][]string{"o/a#1": {"program:x"}}
	if got := ResolveValidationByKey(context.Background(), nil, comments, merges, known, nil)["o/a#1"]; got != (ValidationState{Program: true}) {
		t.Errorf("stale marker = %+v, want program and not validated", got)
	}
	if _, ok := ResolveValidationByKey(context.Background(), nil, comments, nil, known, nil)["o/a#1"]; ok {
		t.Error("nil merge reader resolved the key; it must be left out")
	}
	failing := &closingMerges{err: errors.New("GraphQL 502")}
	if _, ok := ResolveValidationByKey(context.Background(), nil, comments, failing, known, nil)["o/a#1"]; ok {
		t.Error("failed merge read resolved the key; it must be left out")
	}
}
