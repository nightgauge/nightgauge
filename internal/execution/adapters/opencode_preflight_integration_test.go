//go:build opencode_integration

package adapters

// Built with the opencode_integration tag, the package's tests run the real
// opencode binary, so TestMain leaves PATH as it is. The tests in this file
// run it only through OpenCodeProbe: a throwaway HOME, TMPDIR and four XDG
// directories, no credential, the probe's timeout, and its process group
// killed once it exits:
//
//	go test -tags opencode_integration ./internal/execution/adapters/ -run 'OnTheBinary' -count=1

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/config"
)

func init() { openCodeIntegrationBuild = true }

// realOpenCode is the opencode on PATH, the binary these tests probe.
func realOpenCode(t *testing.T) OpenCodeBinary {
	t.Helper()
	bin, err := ResolveOpenCodeBinary("", exec.LookPath)
	if err != nil {
		t.Skipf("no opencode to probe: %v", err)
	}
	return bin
}

// TestOpenCodeSelfTestPassesOnTheBinary: the self-test a dispatch above
// max-tested runs passes on the installed binary for a hosted stage's
// per-run config, and records the pass. `debug config` keeps every key of the
// config with project config off, and `run --help`, which 1.18.30 prints on
// stderr, defines every flag BuildCommand emits.
func TestOpenCodeSelfTestPassesOnTheBinary(t *testing.T) {
	bin := realOpenCode(t)
	version, err := OpenCodeVersionOf(bin.Path)
	if err != nil {
		t.Fatal(err)
	}
	m := openCodeManifestForTest(t)
	home := t.TempDir()
	t.Setenv("ANTHROPIC_API_KEY", "set-by-the-test")
	p := OpenCodeVersionPolicy{Binary: bin, Version: version, MinVersion: m.MinVersion, MaxTested: m.MaxTested, AboveMaxTested: true}
	run := RunOptions{Stage: "feature-dev", Model: "anthropic/claude-sonnet-5", WorktreeDir: t.TempDir()}

	var selfTestErr error
	captureAdapterStderr(t, func() {
		selfTestErr = NewOpenCodeAdapter().runOpenCodeSelfTest(home, p, run, config.OpenCodeConfig{})
	})
	if selfTestErr != nil {
		t.Fatalf("the self-test failed on opencode %s (%s): %v", version, bin.Path, selfTestErr)
	}
	if entries, _ := os.ReadDir(openCodeSelfTestDir(home)); len(entries) != 1 {
		t.Errorf("the pass was not recorded: %v", entries)
	}
}

// TestOpenCodeProbeIgnoresProjectConfigAboveItOnTheBinary: a probe's
// directory is in no git repository, so OpenCode would read the opencode.json
// of every directory above it, a world-writable /tmp included, and load the
// plugins it names. One planted in the directory the probe's is created in is
// not merged into `debug config`.
func TestOpenCodeProbeIgnoresProjectConfigAboveItOnTheBinary(t *testing.T) {
	bin := realOpenCode(t)
	parent := t.TempDir()
	const marker = "nightgauge-planted-parent-config"
	if err := os.WriteFile(filepath.Join(parent, "opencode.json"), []byte(`{"username":"`+marker+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", parent)
	probe, err := NewOpenCodeProbe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if filepath.Dir(probe.Root()) != parent {
		t.Fatalf("the probe's directory %s is not in %s, where the config was planted", probe.Root(), parent)
	}

	res, err := probe.Run(bin.Path, []string{"debug", "config"}, "", nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("`opencode debug config` in the probe = exit %d, %v", res.ExitCode, err)
	}
	if strings.Contains(string(res.Stdout), marker) {
		t.Error("`opencode debug config` in a probe merged the opencode.json planted in the directory above it")
	}
}
