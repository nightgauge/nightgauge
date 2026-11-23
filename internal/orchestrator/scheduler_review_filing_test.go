package orchestrator

import (
	"context"
	"testing"

	gh "github.com/nightgauge/nightgauge/internal/github"
)

// TestReviewFilingClient_LateBound (#2837): with no resolver the review
// filer's client lookup is (nil, nil), and a resolver wired after
// construction is honoured with owner/repo forwarded.
func TestReviewFilingClient_LateBound(t *testing.T) {
	t.Parallel()
	s := newEpicFilterFixture(t)
	if c, err := s.reviewFilingClient(context.Background(), "acme", "b"); c != nil || err != nil {
		t.Fatalf("no resolver: got (%v, %v), want (nil, nil)", c, err)
	}
	want := &gh.Client{}
	var gotOwner, gotRepo string
	s.WithClientResolver(func(_ context.Context, owner, repo string) (*gh.Client, error) {
		gotOwner, gotRepo = owner, repo
		return want, nil
	})
	c, err := s.reviewFilingClient(context.Background(), "acme", "b")
	if err != nil || c != want || gotOwner != "acme" || gotRepo != "b" {
		t.Fatalf("got (%p, %v) for %s/%s", c, err, gotOwner, gotRepo)
	}
}
