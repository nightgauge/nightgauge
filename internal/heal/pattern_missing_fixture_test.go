package heal

import (
	"strings"
	"testing"
)

func TestMissingFixture_Matches_PositiveCases(t *testing.T) {
	p := NewMissingFixture()
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "ENOENT under test/fixtures",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "vitest users.test.ts",
				Details:        "ENOENT: no such file or directory, open 'test/fixtures/users.json'",
			}},
		},
		{
			name: "no such file under __fixtures__",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "jest auth",
				Details:        "no such file packages/auth/__fixtures__/session.json",
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !p.Matches(c.f) {
				t.Fatalf("expected match")
			}
		})
	}
}

func TestMissingFixture_Matches_NegativeCases(t *testing.T) {
	p := NewMissingFixture()
	cases := []struct {
		name string
		f    []BaselineFailure
	}{
		{
			name: "regression class",
			f: []BaselineFailure{{
				Classification: "regression",
				Name:           "fixture missing",
				Details:        "ENOENT: test/fixtures/users.json",
			}},
		},
		{
			name: "missing phrase without fixtures path",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "build",
				Details:        "ENOENT: no such file dist/index.js",
			}},
		},
		{
			name: "fixtures path without missing phrase",
			f: []BaselineFailure{{
				Classification: "inherited",
				Name:           "fixture shape",
				Details:        "AssertionError: test/fixtures/users.json shape mismatch",
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

func TestMissingFixture_GenerateFix_SinglePath_CreatesPlaceholder(t *testing.T) {
	p := NewMissingFixture()
	failures := []BaselineFailure{{
		Classification: "inherited",
		Name:           "vitest users.test.ts",
		Details:        "ENOENT: no such file or directory, open 'test/fixtures/users.json'",
	}}
	fix, ok := p.GenerateFix(failures)
	if !ok {
		t.Fatalf("expected deterministic fix produced")
	}
	if len(fix.FilesToCreate) != 1 {
		t.Fatalf("expected exactly one file to create; got %d", len(fix.FilesToCreate))
	}
	if fix.FilesToCreate[0].Path != "test/fixtures/users.json" {
		t.Errorf("expected fixture path; got %q", fix.FilesToCreate[0].Path)
	}
	if fix.FilesToCreate[0].Content != "" {
		t.Errorf("expected empty placeholder content; got %q", fix.FilesToCreate[0].Content)
	}
	if !contains(fix.PRLabels, "pipeline-heal:auto") {
		t.Errorf("expected pipeline-heal:auto label on deterministic fix; got %v", fix.PRLabels)
	}
	if fix.DiffLineEstimate < 1 {
		t.Errorf("expected DiffLineEstimate >= 1")
	}
}

func TestMissingFixture_GenerateFix_MultiplePaths_NeedsReview(t *testing.T) {
	p := NewMissingFixture()
	failures := []BaselineFailure{
		{
			Classification: "inherited",
			Name:           "vitest users.test.ts",
			Details:        "ENOENT: test/fixtures/users.json",
		},
		{
			Classification: "inherited",
			Name:           "vitest orgs.test.ts",
			Details:        "ENOENT: test/fixtures/orgs.json",
		},
	}
	fix, ok := p.GenerateFix(failures)
	if ok {
		t.Fatalf("expected ok=false (multiple paths cannot be auto-fixed)")
	}
	if !contains(fix.PRLabels, "pipeline-heal:needs-review") {
		t.Errorf("expected needs-review label; got %v", fix.PRLabels)
	}
	if !strings.Contains(fix.PRBody, "test/fixtures/users.json") || !strings.Contains(fix.PRBody, "test/fixtures/orgs.json") {
		t.Errorf("body should list both paths; got: %s", fix.PRBody)
	}
}
