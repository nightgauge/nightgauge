package heal

import (
	"strings"
	"testing"
)

func TestMissingSeedUpdate_Matches_PositiveCases(t *testing.T) {
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "seed keyword + db path",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "vitest packages/db/seed.test.ts > rebuilds seeds",
				Details:        "Error: missing seed row for users at packages/db/seed.ts:42",
			}},
		},
		{
			name: "fixture keyword + drizzle path",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "vitest drizzle/seed.test",
				Details:        "fixture mismatch for drizzle/migrations/0042_add_org.sql",
			}},
		},
	}
	p := NewMissingSeedUpdate()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !p.Matches(c.f) {
				t.Fatalf("expected match")
			}
		})
	}
}

func TestMissingSeedUpdate_Matches_NegativeCases(t *testing.T) {
	p := NewMissingSeedUpdate()
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "no failures",
			f:    nil,
		},
		{
			name: "regression classification",
			f: []BaselineFailure{{
				Classification: "regression",
				Name:           "packages/db/seed.test",
				Details:        "Error: missing seed row",
			}},
		},
		{
			name: "keyword without db path",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "ui/seed",
				Details:        "Error: seed dropdown not visible",
			}},
		},
		{
			name: "db path without keyword",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "packages/db/foo.test",
				Details:        "TypeError: undefined is not a function",
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if p.Matches(c.f) {
				t.Fatalf("expected no match")
			}
		})
	}
}

func TestMissingSeedUpdate_GenerateFix_ReturnsNeedsReview(t *testing.T) {
	p := NewMissingSeedUpdate()
	failures := []BaselineFailure{{
		Classification: "inherited",
		Name:           "packages/db/seed.test",
		Details:        "Error: missing seed for users",
	}}
	fix, ok := p.GenerateFix(failures)
	if ok {
		t.Fatalf("expected ok=false (deterministic fix not generated)")
	}
	if !contains(fix.PRLabels, "pipeline-heal:needs-review") {
		t.Errorf("expected needs-review label; got %v", fix.PRLabels)
	}
	if !contains(fix.PRLabels, "pattern:"+p.Slug()) {
		t.Errorf("expected pattern label; got %v", fix.PRLabels)
	}
	if !strings.Contains(fix.PRBody, "packages/db/seed.test") {
		t.Errorf("body must name the failing test; got: %s", fix.PRBody)
	}
}

func contains(slice []string, want string) bool {
	for _, s := range slice {
		if s == want {
			return true
		}
	}
	return false
}
