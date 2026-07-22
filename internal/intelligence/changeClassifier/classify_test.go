package changeClassifier

import "testing"

func TestClassifyDefault(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  Classification
	}{
		{"empty", nil, Empty},
		{"empty strings only", []string{"", ""}, Empty},

		{"single root markdown", []string{"README.md"}, DocsOnly},
		{"docs dir", []string{"docs/CONFIGURATION.md", "docs/spikes/1-x.md"}, DocsOnly},
		{"nested markdown anywhere", []string{"packages/x/docs/guide.md"}, DocsOnly},
		{"changelog + license", []string{"CHANGELOG.md", "LICENSE"}, DocsOnly},
		{"mdx", []string{"site/page.mdx"}, DocsOnly},

		{"nightgauge config", []string{".nightgauge/config.yaml"}, ConfigOnly},
		{"github workflows", []string{".github/workflows/ci.yml"}, ConfigOnly},
		{"root json", []string{"package.json"}, ConfigOnly},
		{"tsconfig", []string{"tsconfig.base.json"}, ConfigOnly},
		{"yaml anywhere", []string{"configs/codex/model.yaml"}, ConfigOnly},

		{"go source", []string{"internal/orchestrator/scheduler.go"}, Source},
		{"ts source", []string{"packages/nightgauge-vscode/src/extension.ts"}, Source},
		{"multiple source", []string{"cmd/nightgauge/main.go", "internal/x/y.go"}, Source},

		{"docs + source = mixed", []string{"docs/x.md", "internal/x.go"}, Mixed},
		{"config + source = mixed", []string{".github/workflows/ci.yml", "internal/x.go"}, Mixed},
		{"docs + config = mixed", []string{"README.md", ".nightgauge/config.yaml"}, Mixed},
		{"all three = mixed", []string{"README.md", "package.json", "main.go"}, Mixed},

		// A markdown file that also lives under .github/ must still classify as
		// docs (docs precedence over config), so a .github docs-only change is
		// docs_only, not config_only.
		{"github markdown is docs", []string{".github/ISSUE_TEMPLATE.md"}, DocsOnly},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyDefault(tt.files); got != tt.want {
				t.Errorf("ClassifyDefault(%v) = %q, want %q", tt.files, got, tt.want)
			}
		})
	}
}

func TestClassificationTrivial(t *testing.T) {
	trivial := map[Classification]bool{
		DocsOnly:   true,
		ConfigOnly: true,
		Source:     false,
		Mixed:      false,
		Empty:      false,
	}
	for c, want := range trivial {
		if c.Trivial() != want {
			t.Errorf("%q.Trivial() = %v, want %v", c, c.Trivial(), want)
		}
	}
}

func TestClassifyCustomPatterns(t *testing.T) {
	// A repo that treats a custom proto dir as config and ignores the defaults.
	p := ClassPatterns{
		Docs:   []string{"handbook/**"},
		Config: []string{"proto/**"},
	}
	if got := Classify([]string{"handbook/intro.txt"}, p); got != DocsOnly {
		t.Errorf("custom docs glob: got %q, want docs_only", got)
	}
	if got := Classify([]string{"proto/api.proto"}, p); got != ConfigOnly {
		t.Errorf("custom config glob: got %q, want config_only", got)
	}
	// README.md is NOT docs under these custom patterns (no default fallback) →
	// it falls through to source.
	if got := Classify([]string{"README.md"}, p); got != Source {
		t.Errorf("custom patterns should not match default README: got %q, want source", got)
	}
}
