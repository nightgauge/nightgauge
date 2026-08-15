package adapters

import (
	"strings"
	"testing"
)

func TestGrokBuildCommandUsesPromptFile(t *testing.T) {
	a := NewGrokAdapter()
	if a.UsesStdin() {
		t.Fatal("Grok adapter must not use stdin")
	}
	if !a.Agentic() {
		t.Fatal("Grok adapter must be agentic")
	}

	cmd, args, env := a.BuildCommand(RunOptions{
		Prompt:      "do the work",
		IssueNumber: 522,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		Model:       "sonnet",
		TargetRepo:  "nightgauge/nightgauge",
		WorktreeDir: "/tmp/wt",
		RunID:       "run-1",
		MaxTurns:    12,
	})
	if cmd != "grok" {
		t.Fatalf("cmd = %q", cmd)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--prompt-file") {
		t.Fatalf("expected --prompt-file, got %v", args)
	}
	if strings.Contains(joined, "do the work") {
		t.Fatalf("prompt leaked onto argv: %v", args)
	}
	if !containsPair(args, "--output-format", "streaming-json") {
		t.Fatalf("missing streaming-json: %v", args)
	}
	if !containsPair(args, "--model", "grok-4.6") {
		t.Fatalf("sonnet should resolve to grok-4.6, got %v", args)
	}
	if !containsPair(args, "--cwd", "/tmp/wt") {
		t.Fatalf("missing --cwd: %v", args)
	}
	if env["NIGHTGAUGE_ADAPTER"] != "grok" {
		t.Fatalf("adapter env = %q", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["NIGHTGAUGE_TARGET_REPO"] != "nightgauge/nightgauge" {
		t.Fatalf("target repo missing: %#v", env)
	}
	if env[RunIDEnvVar] != "run-1" {
		t.Fatalf("run id missing: %#v", env)
	}
}

// TestGrokBuildCommandHaikuResolvesToServedModel guards the argv the CLI is
// actually spawned with (#532). The haiku band used to resolve to
// grok-build-0.1 — a real xAI API model that the Grok Build CLI's chat proxy
// does not serve — so every haiku-band run died with `unknown model id` in
// seconds. The registry alone cannot prove this: the bug bit at argv
// construction, where a Resolve miss falls through and spawns the raw band
// name. Assert the flag pair the process actually receives.
func TestGrokBuildCommandHaikuResolvesToServedModel(t *testing.T) {
	a := NewGrokAdapter()
	_, args, _ := a.BuildCommand(RunOptions{
		Prompt:      "do the work",
		IssueNumber: 532,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		Model:       "haiku",
		TargetRepo:  "nightgauge/nightgauge",
		WorktreeDir: "/tmp/wt",
		RunID:       "run-2",
	})
	if !containsPair(args, "--model", "grok-4.5") {
		t.Fatalf("haiku should resolve to grok-4.5, got %v", args)
	}
	if containsPair(args, "--model", "haiku") {
		t.Fatalf("unresolved band name leaked onto argv: %v", args)
	}
}

func TestMapGrokEffort(t *testing.T) {
	if got := mapGrokEffortToNightgauge("none"); got != "low" {
		t.Fatalf("none → %q", got)
	}
	if got := grokCliEffortFlag("minimal"); got != "minimal" {
		t.Fatalf("minimal flag → %q", got)
	}
}

func containsPair(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
