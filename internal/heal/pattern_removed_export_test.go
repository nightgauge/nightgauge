package heal

import (
	"strings"
	"testing"
)

func TestRemovedExport_Matches_PositiveCases(t *testing.T) {
	p := NewRemovedExport()
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "TS no exported member from @nightgauge/",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "tsc",
				Details:        "Module '\"@nightgauge/sdk\"' has no exported member 'createPipeline'.",
			}},
		},
		{
			name: "module not found in packages/",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "vitest",
				Details:        "Module not found: Can't resolve '../helpers' in packages/extension/src",
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !p.Matches(c.f) {
				t.Fatalf("expected match for %s", c.name)
			}
		})
	}
}

func TestRemovedExport_Matches_NegativeCases(t *testing.T) {
	p := NewRemovedExport()
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "regression class",
			f: []BaselineFailure{{
				Classification: "regression",
				Name:           "tsc",
				Details:        "Module '\"@nightgauge/sdk\"' has no exported member 'X'.",
			}},
		},
		{
			name: "export phrase but no monorepo path",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "tsc",
				Details:        "Module 'react' has no exported member 'FooBar'.",
			}},
		},
		{
			name: "monorepo path but no export phrase",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "vitest packages/sdk",
				Details:        "AssertionError: expected 2 to equal 3",
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

func TestRemovedExport_GenerateFix_NeedsReview(t *testing.T) {
	p := NewRemovedExport()
	failures := []BaselineFailure{{
		Classification: "inherited",
		Name:           "tsc",
		Details:        "Module '\"@nightgauge/sdk\"' has no exported member 'createPipeline'.",
	}}
	fix, ok := p.GenerateFix(failures)
	if ok {
		t.Fatalf("expected ok=false")
	}
	if !contains(fix.PRLabels, "pipeline-heal:needs-review") {
		t.Errorf("expected needs-review label; got %v", fix.PRLabels)
	}
	if !strings.Contains(fix.PRBody, "createPipeline") && !strings.Contains(fix.PRBody, "tsc") {
		t.Errorf("body should reference the failure; got: %s", fix.PRBody)
	}
}
