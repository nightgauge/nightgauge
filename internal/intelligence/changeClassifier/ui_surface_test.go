package changeClassifier

import "testing"

func TestTouchesUISurface(t *testing.T) {
	repos := DefaultUIBearingRepos()

	tests := []struct {
		name  string
		files []string
		repo  string
		want  bool
	}{
		{"dashboard source touched", []string{"src/pages/Login.tsx"}, "acme-dashboard", true},
		{"dashboard public asset touched", []string{"public/favicon.svg"}, "acme-dashboard", true},
		{"dashboard docs-only change", []string{"README.md"}, "acme-dashboard", false},
		{"dashboard config-only change", []string{"package.json"}, "acme-dashboard", false},
		{"dashboard non-ui source", []string{"scripts/deploy.sh"}, "acme-dashboard", false},
		{"empty diff", nil, "acme-dashboard", false},

		{"acmeweb app source touched", []string{"src/app/page.tsx"}, "acmeweb", true},
		{"acme-site layout touched", []string{"layouts/index.html"}, "acme-site", true},
		{"acme-site content touched", []string{"content/_index.md"}, "acme-site", false}, // *.md is DocsOnly first
		{"flutter dart touched", []string{"lib/main.dart"}, "acme-mobile", true},
		{"flutter unrelated file", []string{"android/app/build.gradle"}, "acme-mobile", false},

		{"unregistered repo never UI-bearing", []string{"src/index.ts"}, "nightgauge", false},
		{"mixed touching dashboard ui", []string{"README.md", "src/App.tsx"}, "acme-dashboard", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := TouchesUISurface(tt.files, tt.repo, repos)
			if got != tt.want {
				t.Errorf("TouchesUISurface(%v, %q) = %v (%s), want %v", tt.files, tt.repo, got, reason, tt.want)
			}
			if reason == "" {
				t.Errorf("TouchesUISurface(%v, %q) returned empty reason", tt.files, tt.repo)
			}
		})
	}
}

func TestDefaultUIBearingReposKnownRepos(t *testing.T) {
	repos := DefaultUIBearingRepos()
	for _, name := range []string{"acme-dashboard", "acmeweb", "acme-site", "acme-mobile"} {
		if _, ok := repos[name]; !ok {
			t.Errorf("DefaultUIBearingRepos() missing expected repo %q", name)
		}
	}
	if _, ok := repos["nightgauge"]; ok {
		t.Errorf("DefaultUIBearingRepos() should not register the pipeline repo itself as UI-bearing")
	}
}
