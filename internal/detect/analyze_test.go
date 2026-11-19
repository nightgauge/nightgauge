package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func writeScript(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FlutterAnalyzeScript), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFlutterAnalyzeCommand(t *testing.T) {
	withScript := t.TempDir()
	writeScript(t, withScript)
	bare := t.TempDir()
	cases := []struct {
		name, dir, configured, want string
	}{
		{"config wins over script", withScript, "make analyze", "make analyze"},
		{"script when present", withScript, "", flutterAnalyzeScriptCommand},
		{"blank config ignored", withScript, "  ", flutterAnalyzeScriptCommand},
		{"default when neither", bare, "", DefaultFlutterAnalyze},
	}
	for _, c := range cases {
		if got := FlutterAnalyzeCommand(c.dir, c.configured); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
