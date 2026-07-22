package ci

import "testing"

func TestClassifyForCI(t *testing.T) {
	tests := []struct {
		name      string
		files     []string
		wantClass string
		wantHeavy bool
	}{
		{"docs only fast-tracks", []string{"docs/x.md", "README.md"}, "docs_only", false},
		{"empty fast-tracks", nil, "empty", false},
		{"source runs full", []string{"internal/x.go"}, "source", true},
		{"mixed runs full", []string{"docs/x.md", "internal/x.go"}, "mixed", true},
		// Config is deliberately NOT fast-tracked for CI: a .github/ or
		// package.json edit can need build+test, and the CI files themselves are
		// config — skipping would let an untested CI change merge.
		{"config runs full", []string{".github/workflows/ci.yml"}, "config_only", true},
		{"package.json runs full", []string{"package.json"}, "config_only", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyForCI(tt.files)
			if got.ChangeClass != tt.wantClass {
				t.Errorf("ChangeClass = %q, want %q", got.ChangeClass, tt.wantClass)
			}
			if got.RunHeavy != tt.wantHeavy {
				t.Errorf("RunHeavy = %v, want %v", got.RunHeavy, tt.wantHeavy)
			}
			// Every heavy job flag mirrors RunHeavy, and RunHeavy is their OR.
			for _, k := range HeavyJobKeys {
				if got.Jobs[k] != tt.wantHeavy {
					t.Errorf("Jobs[%q] = %v, want %v", k, got.Jobs[k], tt.wantHeavy)
				}
			}
		})
	}
}
