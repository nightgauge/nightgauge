package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestReleaseBinaryCarriesNoScriptPayload (#3383): the nightgauge CLI, built
// with the release flags, carries none of the script payloads that moved
// Defender's script model on the Linux amd64 binary (#3192). The OpenCode
// plugin and the pre-push hook ship as files beside the binary
// (internal/shippedfiles), and the managed OpenCode install runs in Go.
func TestReleaseBinaryCarriesNoScriptPayload(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the full CLI")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "nightgauge")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags=-compressdwarf=false", "-o", out, "./cmd/nightgauge")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the release CLI: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Split so this file's own source never spells the payloads whole.
	for _, s := range []string{"#!/bin/" + "bash", "child_" + "process", "curl -" + "fsSL"} {
		if bytes.Contains(data, []byte(s)) {
			t.Errorf("the release binary carries %q; script payloads ship beside it, not in it", s)
		}
	}
}
