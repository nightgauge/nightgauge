package layout

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestCloneLayoutResolvers pins every per-clone class to today's location,
// <root>/.nightgauge/<class>. It is the red/green pivot for the ADR-024 § 7
// move: the issue that relocates the classes edits the expectations here.
func TestCloneLayoutResolvers(t *testing.T) {
	root := t.TempDir()
	resolvers := []struct {
		class string
		fn    func(string) (string, error)
	}{
		{"pipeline", PipelineStateDir},
		{"plans", PlansDir},
		{"retros", RetrosDir},
		{"logs", CloneLogsDir},
	}
	for _, r := range resolvers {
		t.Run(r.class, func(t *testing.T) {
			got, err := r.fn(root)
			if err != nil {
				t.Fatalf("%s(%q): unexpected error: %v", r.class, root, err)
			}
			if want := filepath.Join(root, ".nightgauge", r.class); got != want {
				t.Errorf("%s(%q) = %q, want %q", r.class, root, got, want)
			}

			// An unclean absolute root resolves to the same clean path.
			if got2, err := r.fn(root + string(filepath.Separator) + "."); err != nil || got2 != got {
				t.Errorf("%s(unclean root) = %q, %v; want %q, nil", r.class, got2, err, got)
			}

			for _, bad := range []string{"", ".", "relative/repo", filepath.Join("..", "repo")} {
				dir, err := r.fn(bad)
				if !errors.Is(err, ErrRootNotAbsolute) {
					t.Errorf("%s(%q): err = %v, want ErrRootNotAbsolute", r.class, bad, err)
				}
				if dir != "" {
					t.Errorf("%s(%q) = %q on error, want empty", r.class, bad, dir)
				}
			}
		})
	}
}

// TestCloneLayoutDisplay pins each display form to its resolver's output, so
// help text that names a class directory cannot drift from where the class is.
func TestCloneLayoutDisplay(t *testing.T) {
	root := t.TempDir()
	for _, d := range []struct {
		class   string
		resolve func(string) (string, error)
		display string
	}{
		{"pipeline", PipelineStateDir, PipelineStateDisplay()},
		{"logs", CloneLogsDir, CloneLogsDisplay()},
	} {
		dir, err := d.resolve(root)
		if err != nil {
			t.Fatalf("%s: %v", d.class, err)
		}
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			t.Fatalf("%s: %v", d.class, err)
		}
		if want := filepath.ToSlash(rel); d.display != want {
			t.Errorf("%s display = %q, want %q (the resolver's path relative to the root)", d.class, d.display, want)
		}
	}
}
