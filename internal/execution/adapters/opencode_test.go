package adapters

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

// openCodeForbiddenFlags are the flags the opencode adapter must never emit
// (ADR-022 § 9, § 18): each one either approves tool calls without the
// permission map, publishes the session, or binds a discoverable listener.
var openCodeForbiddenFlags = []string{"--auto", "--yolo", "--dangerously-skip-permissions", "--share", "--mdns"}

// assertNoForbiddenFlag fails when any argv element is, or starts with, a
// forbidden flag, which also catches the `--auto=true` spelling.
func assertNoForbiddenFlag(t *testing.T, args []string) {
	t.Helper()
	for _, a := range args {
		for _, f := range openCodeForbiddenFlags {
			if a == f || strings.HasPrefix(a, f+"=") {
				t.Errorf("argv carries forbidden flag %q: %q", f, args)
			}
		}
	}
}

func TestOpenCodeAdapterIdentity(t *testing.T) {
	a := NewOpenCodeAdapter()
	if a.Name() != "opencode" {
		t.Errorf("Name() = %q, want opencode", a.Name())
	}
	if !a.Agentic() {
		t.Error("Agentic() = false; opencode drives a real tool loop and must be dispatchable")
	}
	if !a.UsesStdin() {
		t.Error("UsesStdin() = false; stdin is the only prompt channel the adapter has (ADR-022 § 19)")
	}
}

// TestOpenCodeBuildCommandArgv pins the exact argv, in order. The model is
// ADR-022 § 1's split-on-the-first-slash fixture: provider "lmstudio", model
// "qwen/qwen3.8-27b".
func TestOpenCodeBuildCommandArgv(t *testing.T) {
	cmd, args, _ := NewOpenCodeAdapter().BuildCommand(RunOptions{
		Prompt:      "implement the issue",
		Model:       "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir: "/work/nightgauge-issue-1612",
		IssueNumber: 1612,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})
	if cmd != "opencode" {
		t.Errorf("cmd = %q, want opencode", cmd)
	}
	want := []string{
		"run", "--format", "json", "--print-logs", "--log-level", "ERROR",
		"-m", "lmstudio/qwen/qwen3.8-27b",
		"--dir", "/work/nightgauge-issue-1612",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("argv =\n  %q\nwant\n  %q", args, want)
	}
	assertNoForbiddenFlag(t, args)
}

// TestOpenCodeBuildCommandNeverEmitsForbiddenFlags drives BuildCommand through
// every RunOptions field that could plausibly grow a flag mapping, and a
// hostile environment, and checks no forbidden flag ever appears.
func TestOpenCodeBuildCommandNeverEmitsForbiddenFlags(t *testing.T) {
	t.Setenv("NIGHTGAUGE_AUTO_APPROVE", "true")
	a := NewOpenCodeAdapter()
	cases := []RunOptions{
		{},
		{Prompt: "--auto --share --mdns --yolo --dangerously-skip-permissions"},
		{Model: "anthropic/claude-sonnet-5", WorktreeDir: "/w", MaxTurns: 3, Effort: "max"},
		{Model: "lmstudio/q", AllowedTools: []string{"Bash", "Edit", "Write", "WebFetch"}, CostBudget: 1, MaxTokens: 1},
		{Model: "--auto/x"},
		{Model: "lmstudio/--share"},
	}
	for _, opts := range cases {
		_, args, _ := a.BuildCommand(opts)
		assertNoForbiddenFlag(t, args)
	}
}

// TestOpenCodePromptNeverOnArgv is the prompt-channel contract (ADR-022 § 19):
// a 200 KiB prompt that opens with `--auto` puts no prompt bytes on argv — as
// a positional it would exceed Linux's 128 KiB per-argument cap and be parsed
// as the auto-approve flag — and delivery writes no temp file at all, so there
// is no 0600 file to leak and nothing to clean up after exit.
func TestOpenCodePromptNeverOnArgv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	worktree := t.TempDir()

	prompt := "--auto " + strings.Repeat("implement the issue as specified. ", 200*1024/34+1)
	if len(prompt) < 200*1024 {
		t.Fatalf("fixture prompt is %d bytes, want at least 200 KiB", len(prompt))
	}

	a := NewOpenCodeAdapter()
	if !a.UsesStdin() {
		t.Fatal("UsesStdin() = false: the manager would not deliver the prompt on stdin")
	}
	_, args, env := a.BuildCommand(RunOptions{
		Prompt:      prompt,
		Model:       "lmstudio/qwen/qwen3.8-27b",
		WorktreeDir: worktree,
	})

	total := 0
	for _, arg := range args {
		total += len(arg)
		if strings.Contains(arg, "implement the issue") || strings.Contains(prompt, arg) && len(arg) > 16 {
			t.Errorf("argv element carries prompt bytes: %.80q", arg)
		}
	}
	if total > 1024 {
		t.Errorf("argv totals %d bytes; with the prompt off argv it is a few dozen", total)
	}
	assertNoForbiddenFlag(t, args)
	for k, v := range env {
		if strings.Contains(v, "implement the issue") {
			t.Errorf("env %s carries prompt bytes", k)
		}
	}

	for _, dir := range []string{tmp, worktree} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Errorf("BuildCommand wrote %d file(s) under %s; stdin delivery needs none", len(entries), dir)
		}
	}
}

// TestOpenCodeServerPasswordIsRandomPerBuild: every spawn gets its own
// non-empty OPENCODE_SERVER_PASSWORD, and it never reaches argv.
func TestOpenCodeServerPasswordIsRandomPerBuild(t *testing.T) {
	t.Setenv(openCodeServerPasswordEnvVar, "inherited-from-the-host")
	a := NewOpenCodeAdapter()
	opts := RunOptions{Model: "lmstudio/q", WorktreeDir: "/w", Prompt: "p"}

	_, args1, env1 := a.BuildCommand(opts)
	_, args2, env2 := a.BuildCommand(opts)
	p1, p2 := env1[openCodeServerPasswordEnvVar], env2[openCodeServerPasswordEnvVar]

	if p1 == "" || p2 == "" {
		t.Fatalf("%s missing from the adapter env: %q, %q", openCodeServerPasswordEnvVar, p1, p2)
	}
	if p1 == p2 {
		t.Errorf("two builds produced the same password %q; it must be fresh per spawn", p1)
	}
	if p1 == "inherited-from-the-host" {
		t.Error("the host's value was reused; the adapter must mint its own")
	}
	if len(p1) < 20 {
		t.Errorf("password is %d characters; want at least 128 bits of randomness", len(p1))
	}
	for _, args := range [][]string{args1, args2} {
		for _, arg := range args {
			if strings.Contains(arg, p1) || strings.Contains(arg, p2) || arg == "-p" || arg == "--password" {
				t.Errorf("password reached argv: %q", args)
			}
		}
	}
}

// TestOpenCodeEnvContract covers the all-adapters exports beside the ones the
// registry-driven tests in adapters_test.go already assert for every adapter.
func TestOpenCodeEnvContract(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghs_contract_token")
	_, _, env := NewOpenCodeAdapter().BuildCommand(RunOptions{
		Model:       "anthropic/claude-sonnet-5",
		IssueNumber: 1612,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		TargetRepo:  "nightgauge/nightgauge",
		RunID:       "01890a5d-ac96-774b-bcce-b302099a8057",
		ContextFile: "/ctx.json",
		OutputFile:  "/out.json",
	})
	want := map[string]string{
		"GITHUB_TOKEN":              "ghs_contract_token",
		"NIGHTGAUGE_ADAPTER":        "opencode",
		"NIGHTGAUGE_OUTPUT_FORMAT":  "json",
		"NIGHTGAUGE_RUN_ID":         "01890a5d-ac96-774b-bcce-b302099a8057",
		"NIGHTGAUGE_TARGET_REPO":    "nightgauge/nightgauge",
		"NIGHTGAUGE_ISSUE_NUMBER":   "1612",
		"NIGHTGAUGE_REPO":           "nightgauge/nightgauge",
		"NIGHTGAUGE_STAGE":          "feature-dev",
		"NIGHTGAUGE_DISPATCH_MODEL": "anthropic/claude-sonnet-5",
		"NIGHTGAUGE_CONTEXT_FILE":   "/ctx.json",
		"NIGHTGAUGE_OUTPUT_FILE":    "/out.json",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}

	t.Setenv("GITHUB_TOKEN", "")
	_, _, env = NewOpenCodeAdapter().BuildCommand(RunOptions{Model: "lmstudio/q"})
	if v, ok := env["GITHUB_TOKEN"]; ok {
		t.Errorf("GITHUB_TOKEN exported as %q with none in the host environment", v)
	}
}

// TestOpenCodeGate pins the enable switch: exactly "1" opens it, and an open
// gate prints every unenforced control; anything else refuses with remediation.
func TestOpenCodeGate(t *testing.T) {
	for _, v := range []string{"", "0", "true", "yes", " 1", "1 "} {
		var warn bytes.Buffer
		err := openCodeGate(v, &warn)
		if err == nil {
			t.Errorf("gate value %q opened the gate; only exactly \"1\" may", v)
			continue
		}
		for _, want := range []string{ExperimentalOpenCodeEnvVar + "=1", "--adapter", "NIGHTGAUGE_ADAPTER", "stream parsing", "run isolation", "permission map", "safety plugin"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal for %q does not mention %q: %v", v, want, err)
			}
		}
		if warn.Len() != 0 {
			t.Errorf("a refused dispatch printed the enabled-dispatch warning: %q", warn.String())
		}
	}

	var warn bytes.Buffer
	if err := openCodeGate("1", &warn); err != nil {
		t.Fatalf("gate value \"1\" refused: %v", err)
	}
	out := warn.String()
	if !strings.Contains(out, "[opencode] WARNING: experimental adapter enabled by "+ExperimentalOpenCodeEnvVar+"=1") {
		t.Errorf("warning header missing:\n%s", out)
	}
	if len(openCodeUnenforcedControls) == 0 {
		t.Fatal("no unenforced controls listed; when the last one lands, the gate goes with it")
	}
	for _, c := range openCodeUnenforcedControls {
		if !strings.Contains(out, "- "+c.name+": "+c.gap) {
			t.Errorf("warning does not list control %q:\n%s", c.name, out)
		}
	}
}

// TestOpenCodePreDispatchReadsTheEnvironment checks the hook the manager calls
// reads the switch from the process environment.
func TestOpenCodePreDispatchReadsTheEnvironment(t *testing.T) {
	a := NewOpenCodeAdapter()
	t.Setenv(ExperimentalOpenCodeEnvVar, "")
	if err := a.PreDispatch(RunOptions{}); err == nil {
		t.Error("PreDispatch allowed a dispatch with the switch unset")
	}
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	if err := a.PreDispatch(RunOptions{}); err != nil {
		t.Errorf("PreDispatch refused with the switch set: %v", err)
	}
}

func TestOpenCodeValidateModel(t *testing.T) {
	a := NewOpenCodeAdapter()
	for _, ok := range []string{
		"lmstudio/qwen/qwen3.8-27b",
		"lmstudio-remote/qwen/qwen3.8-27b",
		"ollama/qwen3-coder:30b",
		"anthropic/claude-sonnet-5",
		"openrouter/meta-llama/llama-4",
	} {
		if err := a.ValidateModel(ok); err != nil {
			t.Errorf("ValidateModel(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"",                // no model: OpenCode would use the operator's default
		"sonnet",          // a tier names no provider
		"not-a-model",     // unknown bare id
		"/qwen",           // empty provider
		"lmstudio/",       // empty model
		"--auto/x",        // provider reads as a flag
		"lmstudio/-x",     // model reads as a flag
		"LMStudio/q",      // provider keys are lowercase
		"192.0.2.10/q",    // an address can never be a provider key
		"lmstudio/q wen",  // whitespace
		"lmstudio/q\tx",   // control character
		"lm.studio/model", // no dots in a provider key
	} {
		if err := a.ValidateModel(bad); err == nil {
			t.Errorf("ValidateModel(%q) = nil, want an error", bad)
		}
	}
}

// TestOpenCodeQualifiesRegistryIDs: a bare, non-deprecated registry id from a
// hosted provider is qualified with that provider's OpenCode key; a deprecated
// id, or one from a provider OpenCode is not wired to, is refused. Driven by
// the registry so a model release does not break the test.
func TestOpenCodeQualifiesRegistryIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range models.All() {
		got, err := openCodeModelArg(m.ID)
		key, hosted := openCodeHostedProviderKeys[m.Provider]
		switch {
		case hosted && !m.Deprecated:
			seen[m.Provider] = true
			if err != nil || got != key+"/"+m.ID {
				t.Errorf("openCodeModelArg(%q) = %q, %v; want %q", m.ID, got, err, key+"/"+m.ID)
			}
		default:
			if err == nil {
				t.Errorf("openCodeModelArg(%q) = %q; want a refusal (provider %q, deprecated %v)", m.ID, got, m.Provider, m.Deprecated)
			}
		}
	}
	for p := range openCodeHostedProviderKeys {
		if !seen[p] {
			t.Errorf("no non-deprecated registry model exercised provider %q", p)
		}
	}

	_, args, _ := NewOpenCodeAdapter().BuildCommand(RunOptions{Model: "claude-sonnet-5"})
	if !containsPair(args, "-m", "anthropic/claude-sonnet-5") {
		t.Errorf("a bare registry id did not reach -m qualified: %q", args)
	}
}

// TestOpenCodeArgvMatchesCapturedHelp checks the argv against real, captured
// `opencode run --help` output (testdata/opencode-cli, see its README): every
// flag the adapter emits must be an option of the captured CLI, every value it
// passes must be one of that option's declared choices, and every forbidden
// flag that the captured CLI defines must still be defined — otherwise the
// forbidden list would be guarding a name that no longer exists.
func TestOpenCodeArgvMatchesCapturedHelp(t *testing.T) {
	dir := filepath.Join("testdata", "opencode-cli")
	raw, err := os.ReadFile(filepath.Join(dir, "run-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	version, err := os.ReadFile(filepath.Join(dir, "version.txt"))
	if err != nil {
		t.Fatal(err)
	}
	help := string(raw)
	ver := strings.TrimSpace(string(version))

	if !strings.HasPrefix(help, "opencode run [message..]") {
		t.Fatalf("capture is not `opencode run --help` (opencode %s): %.60q", ver, help)
	}

	// Each option starts a line: optional "-x, " short form, then "--long".
	// Its block runs to the next option line and holds any [choices: ...].
	optionLine := regexp.MustCompile(`(?m)^\s+(?:-([a-z]), )?--([a-z][a-z-]*)\s`)
	locs := optionLine.FindAllStringSubmatchIndex(help, -1)
	options := map[string]string{} // "--long" and "-x" → the option's text block
	for i, loc := range locs {
		end := len(help)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := help[loc[0]:end]
		options["--"+help[loc[4]:loc[5]]] = block
		if loc[2] >= 0 {
			options["-"+help[loc[2]:loc[3]]] = block
		}
	}
	if len(options) < 20 {
		t.Fatalf("parsed only %d options from the capture; the parser no longer matches the help format", len(options))
	}

	choices := regexp.MustCompile(`\[choices: ([^\]]+)\]`)
	_, args, _ := NewOpenCodeAdapter().BuildCommand(RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: "/w", Prompt: "p"})
	if args[0] != "run" {
		t.Fatalf("argv[0] = %q, want the run subcommand", args[0])
	}
	for i := 1; i < len(args); i++ {
		flag := args[i]
		block, ok := options[flag]
		if !ok {
			t.Errorf("argv flag %q is not an option of opencode %s run", flag, ver)
			continue
		}
		if strings.Contains(block, "[boolean]") {
			continue
		}
		if i+1 >= len(args) {
			t.Errorf("flag %q takes a value and has none", flag)
			continue
		}
		value := args[i+1]
		i++
		if m := choices.FindStringSubmatch(block); m != nil && !strings.Contains(m[1], `"`+value+`"`) {
			t.Errorf("%s %q is not one of the captured choices %s (opencode %s)", flag, value, m[1], ver)
		}
	}

	// The forbidden flags this version defines. --yolo,
	// --dangerously-skip-permissions and --mdns are not `run` options in the
	// capture; they stay forbidden against a future version adding them.
	for _, f := range []string{"--auto", "--share"} {
		if _, ok := options[f]; !ok {
			t.Errorf("forbidden flag %s is no longer an option of opencode %s run; re-check ADR-022 § 9 before editing the list", f, ver)
		}
	}
}
