package adapters

import (
	"strings"
	"testing"
)

func TestGrokBuildCommandUsesPromptFile(t *testing.T) {
	// BuildCommand writes the prompt to os.TempDir() and nothing here deletes
	// it (the spawned CLI owns the file's lifetime), so without redirecting
	// TMPDIR every `go test` run leaks a nightgauge-grok-prompt-*.txt into the
	// real temp dir. t.TempDir() is removed when the test ends.
	t.Setenv("TMPDIR", t.TempDir())
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

// TestGrokBuildCommandEveryBandResolvesToServedModel guards the argv the CLI is
// actually spawned with (#532). The haiku band used to resolve to
// grok-build-0.1 — a real xAI API model that the Grok Build CLI's chat proxy
// does not serve — so every haiku-band run died with `unknown model id` in
// seconds. The registry alone cannot prove this: the bug bit at argv
// construction, where a Resolve miss falls through and spawns the raw band
// name. Assert the flag pair the process actually receives.
//
// All four bands are checked together because they must land on ONE model: the
// `--effort` value comes from a single provider-global NIGHTGAUGE_GROK_EFFORT
// knob with no per-band clamp, so the moment two bands resolve to models with
// different effort ladders, the higher rungs reproduce #532's exact symptom
// (`unknown effort level 'xhigh'`, exit 1, no work) on whichever band has the
// shorter ladder. Collapsing the bands onto grok-4.6 is what makes that knob
// coherent; a future split must fail here.
func TestGrokBuildCommandEveryBandResolvesToServedModel(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a := NewGrokAdapter()
	for _, band := range []string{"haiku", "sonnet", "opus", "fable"} {
		_, args, _ := a.BuildCommand(RunOptions{
			Prompt:      "do the work",
			IssueNumber: 532,
			Repo:        "nightgauge/nightgauge",
			Stage:       "feature-dev",
			Model:       band,
			TargetRepo:  "nightgauge/nightgauge",
			WorktreeDir: "/tmp/wt",
			RunID:       "run-2",
		})
		if !containsPair(args, "--model", "grok-4.6") {
			t.Fatalf("%s should resolve to grok-4.6, got %v", band, args)
		}
		if containsPair(args, "--model", band) {
			t.Fatalf("unresolved band name %q leaked onto argv: %v", band, args)
		}
	}
}

// TestGrokBuildCommandRejectsUnservedModelAsBandTarget is the other half of
// #532: the models the CLI cannot spawn must not be reachable through band
// resolution on ANY band, only by exact id. grok-build-0.1 is `deprecated` (the
// chat proxy answers `unknown model id`) and grok-4.5 carries no band, so
// neither may ever appear on argv from a band request.
func TestGrokBuildCommandRejectsUnservedModelAsBandTarget(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a := NewGrokAdapter()
	for _, band := range []string{"haiku", "sonnet", "opus", "fable"} {
		_, args, _ := a.BuildCommand(RunOptions{Prompt: "x", Model: band, WorktreeDir: "/tmp/wt"})
		for _, forbidden := range []string{"grok-build-0.1", "grok-4.5"} {
			if containsPair(args, "--model", forbidden) {
				t.Fatalf("band %q resolved to non-band model %q: %v", band, forbidden, args)
			}
		}
	}
	// Exact-id requests still pass through untouched — the registry keeps both
	// entries for historical cost replay and explicit override.
	for _, id := range []string{"grok-build-0.1", "grok-4.5"} {
		_, args, _ := a.BuildCommand(RunOptions{Prompt: "x", Model: id, WorktreeDir: "/tmp/wt"})
		if !containsPair(args, "--model", id) {
			t.Fatalf("exact id %q did not survive to argv: %v", id, args)
		}
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
