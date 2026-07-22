package gates

import (
	"context"
	"testing"
)

func TestRelaxDecision(t *testing.T) {
	docsClasses := []string{"docs_only", "config_only"}

	t.Run("docs-only diff relaxes", func(t *testing.T) {
		relaxed, class := RelaxDecision([]string{"docs/x.md", "README.md"}, docsClasses)
		if !relaxed || class != "docs_only" {
			t.Errorf("got relaxed=%v class=%q, want true/docs_only", relaxed, class)
		}
	})

	t.Run("config-only diff relaxes when listed", func(t *testing.T) {
		relaxed, class := RelaxDecision([]string{".github/workflows/ci.yml"}, docsClasses)
		if !relaxed || class != "config_only" {
			t.Errorf("got relaxed=%v class=%q, want true/config_only", relaxed, class)
		}
	})

	t.Run("drift-revoke: source diff never relaxes", func(t *testing.T) {
		// A "docs" issue that actually edited source classifies as source and is
		// NOT relaxed — the classifier is the drift-revoke safety check.
		relaxed, class := RelaxDecision([]string{"docs/x.md", "internal/x.go"}, docsClasses)
		if relaxed || class != "mixed" {
			t.Errorf("got relaxed=%v class=%q, want false/mixed", relaxed, class)
		}
		relaxed2, class2 := RelaxDecision([]string{"internal/x.go"}, docsClasses)
		if relaxed2 || class2 != "source" {
			t.Errorf("got relaxed=%v class=%q, want false/source", relaxed2, class2)
		}
	})

	t.Run("config-disabled: empty relaxClasses never relaxes", func(t *testing.T) {
		relaxed, class := RelaxDecision([]string{"docs/x.md"}, nil)
		if relaxed {
			t.Errorf("empty relaxClasses must not relax (got class=%q)", class)
		}
		if class != "docs_only" {
			t.Errorf("class still reported for audit, got %q", class)
		}
	})

	t.Run("config-only NOT relaxed when only docs_only listed", func(t *testing.T) {
		relaxed, class := RelaxDecision([]string{".github/workflows/ci.yml"}, []string{"docs_only"})
		if relaxed || class != "config_only" {
			t.Errorf("got relaxed=%v class=%q, want false/config_only", relaxed, class)
		}
	})
}

func TestRelaxedContext(t *testing.T) {
	ctx := context.Background()
	if Relaxed(ctx) {
		t.Error("default context must not be relaxed")
	}
	if !Relaxed(WithRelaxed(ctx, true)) {
		t.Error("WithRelaxed(true) must report relaxed")
	}
	if Relaxed(WithRelaxed(ctx, false)) {
		t.Error("WithRelaxed(false) must not report relaxed")
	}
}
