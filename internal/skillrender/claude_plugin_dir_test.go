package skillrender

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBundledClaudePluginDir(t *testing.T) {
	bundle := t.TempDir()
	exe := filepath.Join(bundle, "bin", "nightgauge")
	plugin := filepath.Join(bundle, "claude-plugins", "nightgauge")
	mustWrite := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(exe)

	if got := BundledClaudePluginDir(exe); got != "" {
		t.Errorf("no plugin beside the binary: got %q", got)
	}
	if err := os.MkdirAll(plugin, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := BundledClaudePluginDir(exe); got != "" {
		t.Errorf("a plugin with no hooks.json has no gates: got %q", got)
	}

	mustWrite(filepath.Join(plugin, "hooks", "hooks.json"))
	resolved, err := filepath.EvalSymlinks(plugin)
	if err != nil {
		t.Fatal(err)
	}
	if got := BundledClaudePluginDir(exe); got != resolved {
		t.Errorf("bundle: got %q, want %q", got, resolved)
	}

	shim := filepath.Join(t.TempDir(), "nightgauge")
	if err := os.Symlink(exe, shim); err != nil {
		t.Fatal(err)
	}
	if got := BundledClaudePluginDir(shim); got != resolved {
		t.Errorf("through a symlink: got %q, want %q", got, resolved)
	}
	if got := BundledClaudePluginDir(""); got != "" {
		t.Errorf("no executable: got %q", got)
	}
}
