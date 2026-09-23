package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/scaffold"
)

func runConfigInit(t *testing.T, outPath string) map[string]any {
	t.Helper()
	cmd := rootCmd()
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"config", "init", "--owner", "nightgauge", "--out", outPath, "--force", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init failed: %v\nstderr: %s", err, stderr.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("JSON output invalid: %v\n%s", err, stdout.String())
	}
	return payload
}

func ignoreAction(t *testing.T, payload map[string]any) string {
	t.Helper()
	rules, ok := payload["ignore_rules"].(map[string]any)
	if !ok {
		t.Fatalf("no ignore_rules in %v", payload)
	}
	action, _ := rules["action"].(string)
	return action
}

// #2026: a clone set up by the CLI alone gets the .nightgauge/ ignore rules.
func TestConfigInitCmd_EnsuresIgnoreRules(t *testing.T) {
	t.Run("untracked: writes the embedded template", func(t *testing.T) {
		root := gittest.InitRepo(t, t.TempDir(), "-q")
		outPath := filepath.Join(root, ".nightgauge", "config.yaml")

		if got := ignoreAction(t, runConfigInit(t, outPath)); got != string(scaffold.IgnoreCreated) {
			t.Fatalf("action = %q, want created", got)
		}
		body, err := os.ReadFile(filepath.Join(root, ".nightgauge", ".gitignore"))
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != scaffold.GitignoreTemplate {
			t.Fatal(".nightgauge/.gitignore differs from the embedded template")
		}
		if got := ignoreAction(t, runConfigInit(t, outPath)); got != string(scaffold.IgnoreCurrent) {
			t.Fatalf("second run: action = %q, want current", got)
		}
	})

	t.Run("tracked and older: committed file untouched", func(t *testing.T) {
		root := gittest.InitRepo(t, t.TempDir(), "-q")
		ignorePath := filepath.Join(root, ".nightgauge", ".gitignore")
		if err := os.MkdirAll(filepath.Dir(ignorePath), 0o755); err != nil {
			t.Fatal(err)
		}
		committed := "# nightgauge-gitignore-version: 10\n"
		if err := os.WriteFile(ignorePath, []byte(committed), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, root, "add", "-A")
		gittest.Run(t, root, "commit", "-q", "-m", "init")

		payload := runConfigInit(t, filepath.Join(root, ".nightgauge", "config.yaml"))
		if got := ignoreAction(t, payload); got != string(scaffold.IgnoreDeferred) {
			t.Fatalf("action = %q, want deferred", got)
		}
		if body, _ := os.ReadFile(ignorePath); string(body) != committed {
			t.Fatal("tracked .nightgauge/.gitignore was edited")
		}
	})

	t.Run("custom --out elsewhere: no ignore rules", func(t *testing.T) {
		payload := runConfigInit(t, filepath.Join(t.TempDir(), "config.yaml"))
		if _, ok := payload["ignore_rules"]; ok {
			t.Fatalf("unexpected ignore_rules: %v", payload)
		}
	})
}
