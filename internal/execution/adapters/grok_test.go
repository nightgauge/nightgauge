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

// TestValidateGrokEffortRejectsUndeclaredRung is the #532-signature regression
// the issue's AC requires: a provider-global effort env of `xhigh` against a
// model whose registry ladder tops out at `high` must fail closed BEFORE spawn
// with an error naming the model, the requested effort, and the declared
// ladder — instead of passing the static vocabulary filter and dying inside
// the CLI (`unknown effort level 'xhigh'`, exit 1, no work, nothing
// classified).
func TestValidateGrokEffortRejectsUndeclaredRung(t *testing.T) {
	// grok-4.5 declares supported_efforts [low, medium, high] in the registry.
	err := validateGrokEffort("grok-4.5", "xhigh")
	if err == nil {
		t.Fatal("xhigh against grok-4.5 (tops out at high) must be rejected")
	}
	for _, want := range []string{"xhigh", "grok-4.5", "low, medium, high"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q (model, requested effort, declared ladder); got: %v", want, err)
		}
	}

	// The same rejection must fire through the manager's pre-spawn hook with
	// the provider-global env set — the exact dispatch shape #532 hit.
	t.Setenv("NIGHTGAUGE_GROK_EFFORT", "xhigh")
	a := NewGrokAdapter()
	if err := a.ValidateModel("grok-4.5"); err == nil {
		t.Fatal("ValidateModel must fail closed on NIGHTGAUGE_GROK_EFFORT=xhigh for grok-4.5")
	}
	// max is above every current xai ladder — grok-4.6 declares up to xhigh.
	t.Setenv("NIGHTGAUGE_GROK_EFFORT", "max")
	if err := a.ValidateModel("grok-4.6"); err == nil {
		t.Fatal("ValidateModel must fail closed on NIGHTGAUGE_GROK_EFFORT=max for grok-4.6")
	}
}

// TestValidateGrokEffortAcceptsDeclaredRungs pins the pass side of the gate,
// including the #523 vocabulary subtlety: grok-native none/minimal normalize
// to "low" BEFORE the ladder check, so they are valid exactly when the
// resolved model declares "low". Band names resolve the same way BuildCommand
// resolves them (sonnet → grok-4.6), so the rung is checked against the model
// actually served.
func TestValidateGrokEffortAcceptsDeclaredRungs(t *testing.T) {
	// grok-4.6 declares [low, medium, high, xhigh].
	for _, effort := range []string{"none", "minimal", "low", "medium", "high", "xhigh"} {
		if err := validateGrokEffort("grok-4.6", effort); err != nil {
			t.Fatalf("effort %q must be accepted for grok-4.6: %v", effort, err)
		}
	}
	// Bands resolve before enforcement: sonnet → grok-4.6, which declares xhigh.
	if err := validateGrokEffort("sonnet", "xhigh"); err != nil {
		t.Fatalf("xhigh must be accepted for band sonnet (resolves to grok-4.6): %v", err)
	}
	// No explicit effort → nothing to enforce.
	if err := validateGrokEffort("grok-4.5", ""); err != nil {
		t.Fatalf("empty effort must pass: %v", err)
	}
}

// TestValidateGrokEffortNoEffortAxis pins the #336 `[]` semantics: an empty
// supported_efforts is a positive declaration ("no effort axis"), so ANY
// explicit effort is rejected — including grok-native rungs that would
// otherwise normalize onto the Nightgauge ladder.
func TestValidateGrokEffortNoEffortAxis(t *testing.T) {
	// grok-build-0.1 declares supported_efforts [] (reachable by exact id only).
	for _, effort := range []string{"low", "none", "xhigh"} {
		err := validateGrokEffort("grok-build-0.1", effort)
		if err == nil {
			t.Fatalf("explicit effort %q against a no-effort-axis model must be rejected", effort)
		}
		if !strings.Contains(err.Error(), "no effort axis") {
			t.Fatalf("error must say the model declares no effort axis; got: %v", err)
		}
	}
}

// TestValidateGrokEffortUnknownModelPassesThrough pins the other half of the
// #336 semantics: a model with NO registry descriptor (unregistered id — the
// registry cannot answer) passes through with a warning, never a hard
// failure. A value outside the Grok CLI vocabulary is likewise not an error:
// BuildCommand's syntax filter drops the flag, exactly as before.
func TestValidateGrokEffortUnknownModelPassesThrough(t *testing.T) {
	if err := validateGrokEffort("some-unregistered-model", "xhigh"); err != nil {
		t.Fatalf("unknown model must pass through (logged warning, no failure): %v", err)
	}
	if err := validateGrokEffort("grok-4.5", "banana"); err != nil {
		t.Fatalf("non-vocabulary effort is dropped by the syntax filter, not an error: %v", err)
	}
}

// TestValidateGrokModelAcceptsServedModels pins the pass side of the #579
// closed-set gate: bands resolve to grok-4.6 (cli-served) and succeed, and
// concrete served ids pass through.
func TestValidateGrokModelAcceptsServedModels(t *testing.T) {
	for _, m := range []string{"", "haiku", "sonnet", "opus", "fable", "grok-4.6", "grok-4.5"} {
		if err := ValidateGrokModel(m); err != nil {
			t.Errorf("ValidateGrokModel(%q) = %v, want nil", m, err)
		}
	}
}

// TestValidateGrokModelRejectsUnknownModel absorbs #552: resolveGrokModel's
// bare 'return model' used to hand an unresolved name straight to `grok
// --model`, reaching the CLI unchecked. ValidateGrokModel closes that gate —
// Grok becomes a CLOSED set, like codex/gemini.
func TestValidateGrokModelRejectsUnknownModel(t *testing.T) {
	err := ValidateGrokModel("totally-made-up-model")
	if err == nil {
		t.Fatal("an unknown model must be rejected before spawn")
	}
	if !strings.Contains(err.Error(), "not valid") {
		t.Errorf("error must say the model is not valid; got: %v", err)
	}
	if !strings.Contains(err.Error(), "grok-4.6") {
		t.Errorf("error must list grok-4.6 among the valid models; got: %v", err)
	}
}

// TestValidateGrokModelRejectsUnservedTransport is the #579 regression: a
// model the registry knows about but marks transports.cli.served=false must
// fail closed BEFORE spawn with a classified error naming provider, model,
// and transport — grok-build-0.1 is exactly this shape (spike #568,
// landed by #578). ValidateModel (the hook manager.go calls before
// BuildCommand) must reject it too, independent of NIGHTGAUGE_GROK_EFFORT.
func TestValidateGrokModelRejectsUnservedTransport(t *testing.T) {
	err := ValidateGrokModel("grok-build-0.1")
	if err == nil {
		t.Fatal("grok-build-0.1 (transports.cli.served=false) must be rejected before spawn")
	}
	for _, want := range []string{"xai", "grok-build-0.1", "cli"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error must name provider/model/transport; missing %q in %q", want, err.Error())
		}
	}

	a := NewGrokAdapter()
	if err := a.ValidateModel("grok-build-0.1"); err == nil {
		t.Fatal("GrokAdapter.ValidateModel must reject grok-build-0.1 before spawn (no effort env set)")
	}
}

// TestGrokBuildBothUnselectableReasonsIndependentlyBlockDispatch pins the
// #579 AC directly on the adapter's pre-spawn gate: grok-build-0.1 keeps
// deprecated:true (cost-replay), but the reason ValidateGrokModel rejects it
// is transports.cli.served:false — checked BEFORE the deprecated fallback —
// and knownGrokModels() independently excludes it via `!Deprecated` too, so
// either fact alone is sufficient.
func TestGrokBuildBothUnselectableReasonsIndependentlyBlockDispatch(t *testing.T) {
	if known := knownGrokModels(); known["grok-build-0.1"] {
		t.Error("knownGrokModels() must exclude grok-build-0.1 (deprecated:true alone is sufficient)")
	}
	if err := ValidateGrokModel("grok-build-0.1"); err == nil {
		t.Error("ValidateGrokModel must reject grok-build-0.1 (transports.cli.served:false alone is sufficient)")
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
