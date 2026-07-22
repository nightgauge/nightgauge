package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFramework(t *testing.T) {
	t.Run("flutter", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "pubspec.yaml")
		if got := Framework(dir); got != FrameworkFlutter {
			t.Errorf("got %s, want flutter", got)
		}
	})
	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "go.mod")
		if got := Framework(dir); got != FrameworkGo {
			t.Errorf("got %s, want go", got)
		}
	})
	t.Run("node", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "package.json")
		if got := Framework(dir); got != FrameworkNode {
			t.Errorf("got %s, want node", got)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		if got := Framework(t.TempDir()); got != FrameworkUnknown {
			t.Errorf("got %s, want unknown", got)
		}
	})
	t.Run("flutter wins over go and node", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "pubspec.yaml")
		write(t, dir, "go.mod")
		write(t, dir, "package.json")
		if got := Framework(dir); got != FrameworkFlutter {
			t.Errorf("got %s, want flutter", got)
		}
	})
}
