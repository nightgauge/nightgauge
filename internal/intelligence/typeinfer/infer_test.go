package typeinfer

import "testing"

func TestInfer(t *testing.T) {
	tests := []struct {
		name       string
		in         InferInput
		wantType   string
		wantSource string
	}{
		// --- explicit label always wins ---
		{
			name:       "type:bug label",
			in:         InferInput{Labels: []string{"type:bug"}},
			wantType:   "type:bug",
			wantSource: SourceLabel,
		},
		{
			name:       "type:feature label",
			in:         InferInput{Labels: []string{"type:feature"}},
			wantType:   "type:feature",
			wantSource: SourceLabel,
		},
		{
			name:       "type:docs label",
			in:         InferInput{Labels: []string{"type:docs"}},
			wantType:   "type:docs",
			wantSource: SourceLabel,
		},
		{
			name:       "type:refactor label",
			in:         InferInput{Labels: []string{"type:refactor"}},
			wantType:   "type:refactor",
			wantSource: SourceLabel,
		},
		{
			name:       "type:chore label",
			in:         InferInput{Labels: []string{"type:chore"}},
			wantType:   "type:chore",
			wantSource: SourceLabel,
		},
		{
			name:       "label uppercase normalized",
			in:         InferInput{Labels: []string{"Type:Bug"}},
			wantType:   "type:bug",
			wantSource: SourceLabel,
		},
		{
			name: "label takes precedence over keywords",
			in: InferInput{
				Labels: []string{"type:feature"},
				Title:  "fix the crash on startup",
				Body:   "stack trace attached",
			},
			wantType:   "type:feature",
			wantSource: SourceLabel,
		},
		{
			name: "non-classification type:* labels (e.g. type:epic) are ignored",
			in: InferInput{
				Labels: []string{"type:epic"},
				Title:  "fix login bug",
			},
			wantType:   "type:bug",
			wantSource: SourceKeyword,
		},

		// --- bug keywords (each shell-rule keyword from both consumers) ---
		{name: "bug keyword: bug", in: InferInput{Title: "Bug in auth flow"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: error", in: InferInput{Title: "TypeError on submit"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: exception", in: InferInput{Title: "exception thrown"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: crash", in: InferInput{Title: "App crash on launch"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: broken", in: InferInput{Title: "Login broken"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: fail", in: InferInput{Title: "Tests fail randomly"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: wrong", in: InferInput{Title: "Wrong icon shown"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: regression", in: InferInput{Title: "regression in 1.2"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: stack trace", in: InferInput{Title: "Stack trace on save"}, wantType: "type:bug", wantSource: SourceKeyword},
		{name: "bug keyword: fix", in: InferInput{Title: "fix retry loop"}, wantType: "type:bug", wantSource: SourceKeyword},

		// --- docs keywords ---
		{name: "docs keyword: doc", in: InferInput{Title: "update doc for sdk"}, wantType: "type:docs", wantSource: SourceKeyword},
		{name: "docs keyword: readme", in: InferInput{Title: "README needs install steps"}, wantType: "type:docs", wantSource: SourceKeyword},
		{name: "docs keyword: guide", in: InferInput{Title: "Add migration guide"}, wantType: "type:docs", wantSource: SourceKeyword},

		// --- refactor keywords ---
		{name: "refactor keyword: refactor", in: InferInput{Title: "Refactor token store"}, wantType: "type:refactor", wantSource: SourceKeyword},
		{name: "refactor keyword: clean", in: InferInput{Title: "clean up old code"}, wantType: "type:refactor", wantSource: SourceKeyword},
		{name: "refactor keyword: simplify", in: InferInput{Title: "simplify config loader"}, wantType: "type:refactor", wantSource: SourceKeyword},

		// --- chore keywords ---
		{name: "chore keyword: chore", in: InferInput{Title: "chore: bump deps"}, wantType: "type:chore", wantSource: SourceKeyword},
		{name: "chore keyword: maintain", in: InferInput{Title: "maintain CI scripts"}, wantType: "type:chore", wantSource: SourceKeyword},
		{name: "chore keyword: update dep", in: InferInput{Title: "update deps for security"}, wantType: "type:chore", wantSource: SourceKeyword},

		// --- body falls through to title (priority: body > title) ---
		{
			name: "body keyword wins over title",
			in: InferInput{
				Title: "ship feature",
				Body:  "this is a bug — crash on startup",
			},
			wantType:   "type:bug",
			wantSource: SourceKeyword,
		},
		{
			name: "title used when body is empty",
			in: InferInput{
				Title: "fix login crash",
				Body:  "",
			},
			wantType:   "type:bug",
			wantSource: SourceKeyword,
		},
		{
			name: "title used when body has no keywords",
			in: InferInput{
				Title: "refactor router",
				Body:  "see attached design",
			},
			wantType:   "type:refactor",
			wantSource: SourceKeyword,
		},

		// --- default fallback ---
		{
			name:       "no labels, no keywords → default feature",
			in:         InferInput{Title: "ship the new dashboard", Body: "see proposal"},
			wantType:   "type:feature",
			wantSource: SourceDefault,
		},
		{
			name:       "empty input → default feature",
			in:         InferInput{},
			wantType:   "type:feature",
			wantSource: SourceDefault,
		},

		// --- rule ordering: bug keywords beat overlapping refactor cues ---
		{
			name: "bug keyword wins when both bug and refactor keywords present",
			in: InferInput{
				Title: "refactor crashy parser",
			},
			wantType:   "type:bug",
			wantSource: SourceKeyword,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Infer(tt.in)
			if got.Type != tt.wantType || got.Source != tt.wantSource {
				t.Errorf("Infer(%+v) = {%q, %q}, want {%q, %q}",
					tt.in, got.Type, got.Source, tt.wantType, tt.wantSource)
			}
		})
	}
}

func TestInferStability(t *testing.T) {
	in := InferInput{
		Title:  "fix retry loop",
		Body:   "stack trace attached",
		Labels: []string{"priority:high"},
	}
	first := Infer(in)
	for i := 0; i < 5; i++ {
		got := Infer(in)
		if got != first {
			t.Fatalf("Infer is not pure: iteration %d returned %+v, expected %+v", i, got, first)
		}
	}
}
