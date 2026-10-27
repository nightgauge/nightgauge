package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hashFile(t *testing.T, path string) [32]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(raw)
}

// The verb proposes a registry patch and never writes the registry (#83).
func TestModelWatchClassifyLeavesRegistryUntouched(t *testing.T) {
	registries := []string{
		filepath.Join("..", "..", "internal", "models", "model-registry.json"),
		filepath.Join("..", "..", "packages", "nightgauge-sdk", "src", "eval", "model-registry.json"),
	}
	before := map[string][32]byte{}
	for _, r := range registries {
		before[r] = hashFile(t, r)
	}
	fixtures := filepath.Join("..", "..", "internal", "cmd", "modelwatch", "testdata")
	for _, name := range []string{"opus-5", "gpt-5-x", "no-change"} {
		args := []string{"classify", "--input", filepath.Join(fixtures, name)}
		if _, err := os.Stat(filepath.Join(fixtures, name, "registry.json")); err == nil {
			args = append(args, "--registry", filepath.Join(fixtures, name, "registry.json"))
		}
		cmd := modelWatchCmd()
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if name == "opus-5" && !strings.Contains(out.String(), "claude-opus-5") {
			t.Errorf("opus-5 human output missing the model: %s", out.String())
		}
	}
	for _, r := range registries {
		if hashFile(t, r) != before[r] {
			t.Errorf("%s changed during classify", r)
		}
	}
}

func TestModelWatchClassifyHasNoApply(t *testing.T) {
	cmd := modelWatchCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"classify", "--input", ".", "--apply"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown flag: --apply") {
		t.Errorf("--apply err = %v, want unknown flag", err)
	}
}
