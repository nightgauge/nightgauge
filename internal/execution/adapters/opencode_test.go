package adapters

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/models"
)

// openCodeForbiddenFlags are the flags the opencode adapter must never emit
// (ADR-022 § 9, § 18): each one either approves tool calls without the
// permission map, publishes the session, or exposes a listener to the network
// or to other origins. TestOpenCodeNeverEmitsBypassFlags checks the full list.
var openCodeForbiddenFlags = []string{"--auto", "--yolo", "--dangerously-skip-permissions", "--share", "--mdns", "--cors"}

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

// TestOpenCodeWarningDisclosesWhereThePromptCanGo: the model a stage names is
// not the only place its prompt can go, and the warning is the operator's only
// disclosure of the others. The anthropic refusal sees only the stage's model,
// while any OpenCode config the run reads, the target repository's
// opencode.json and .opencode/ as much as the operator's own, can name another
// that receives the prompt: small_model titles every session, and the title and
// compaction agents and every subagent run on their agent's model (ADR-022
// § 10, § 17). And an endpoint can forward: a local Ollama serves its cloud
// models from Ollama's hosted service (§ 3, § Endpoints). So the credential
// line names both kinds of config and both keys, the egress line names
// session-title generation, and the endpoint line names Ollama cloud models.
func TestOpenCodeWarningDisclosesWhereThePromptCanGo(t *testing.T) {
	gaps := map[string]string{}
	for _, c := range openCodeUnenforcedControls {
		gaps[c.name] = c.gap
	}
	for name, wants := range map[string][]string{
		"credential policy": {
			"only the anthropic/ model a stage names is refused",
			"the operator's own", "the target repository's opencode.json and .opencode/",
			"small_model", "titles every session", "an agent's model", "subagent", "stored login",
		},
		"egress defaults": {"session-title generation", "stage prompt", "small_model"},
		"endpoint policy": {"Ollama cloud model", "Ollama's hosted service"},
	} {
		gap, ok := gaps[name]
		if !ok {
			t.Errorf("the warning has no %q control", name)
			continue
		}
		for _, want := range wants {
			if !strings.Contains(gap, want) {
				t.Errorf("the %q warning line does not say %q:\n  %s", name, want, gap)
			}
		}
	}
}

// openCodeADR is ADR-022, which the tests below keep the adapter in step with.
const openCodeADR = "../../../docs/decisions/022-opencode-multi-provider-adapter.md"

// TestOpenCodeUnenforcedControlsMatchADR: the enabled-dispatch warning is the
// operator's only disclosure of what an opencode dispatch runs without, so
// openCodeUnenforcedControls lists exactly the rows of ADR-022's "Control not
// yet enforced" table, in the same order, and every row names its owning
// change. A control recorded in one and not the other fails here.
func TestOpenCodeUnenforcedControlsMatchADR(t *testing.T) {
	raw, err := os.ReadFile(openCodeADR)
	if err != nil {
		t.Fatalf("read %s: %v", openCodeADR, err)
	}
	owners := regexp.MustCompile(`^#\d+(, #\d+)*$`)
	var rows []string
	inTable := false
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
		switch {
		case !inTable:
			inTable = len(cells) == 2 && cells[0] == "Control not yet enforced"
		case strings.Trim(cells[0], "-: ") == "":
			// The header's separator row.
		default:
			if len(cells) != 2 || !owners.MatchString(cells[1]) {
				t.Errorf("ADR-022 row %q does not name its owning change as #N[, #N]", line)
			}
			rows = append(rows, cells[0])
		}
	}
	if len(rows) == 0 {
		t.Fatalf("%s has no \"Control not yet enforced\" table", openCodeADR)
	}

	names := make([]string, len(openCodeUnenforcedControls))
	for i, c := range openCodeUnenforcedControls {
		names[i] = c.name
		if strings.TrimSpace(c.gap) == "" {
			t.Errorf("control %q does not say what the dispatch runs without", c.name)
		}
	}
	if strings.Join(rows, "\n") != strings.Join(names, "\n") {
		t.Errorf("the warning and ADR-022's \"Control not yet enforced\" table differ; they must match exactly, in order:\n  ADR-022: %q\n  warning: %q", rows, names)
	}
}

// TestOpenCodeCaptureScriptWritesOnlyAClearedCapture runs capture.sh against a
// fake opencode, from a copy of its directory. The fixtures it writes are
// committed, so a capture naming an IPv4 address other than 127.0.0.1, on a
// line of its own or beside 127.0.0.1, must fail and leave the fixtures exactly
// as they were, with nothing left behind in staging. A clean capture replaces
// them.
func TestOpenCodeCaptureScriptWritesOnlyAClearedCapture(t *testing.T) {
	for _, tool := range []string{"bash", "perl"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	script, err := os.ReadFile(filepath.Join("testdata", "opencode-cli", "capture.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const oldVersion, oldHelp = "previous version\n", "previous help\n"

	capture := func(t *testing.T, help string) (string, []byte, error) {
		t.Helper()
		dir := t.TempDir() // stands in for testdata/opencode-cli
		for name, body := range map[string]string{"capture.sh": string(script), "version.txt": oldVersion, "run-help.txt": oldHelp} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		bin := t.TempDir()
		fake := "#!/bin/sh\nif [ \"$1\" = --version ]; then echo 9.9.9; else printf '%s' \"$FAKE_OPENCODE_HELP\"; fi\n"
		if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
		staging := t.TempDir()
		cmd := exec.Command("bash", filepath.Join(dir, "capture.sh"))
		cmd.Env = append(os.Environ(),
			"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"TMPDIR="+staging,
			"FAKE_OPENCODE_HELP="+help)
		out, runErr := cmd.CombinedOutput()

		if left, err := os.ReadDir(staging); err != nil || len(left) != 0 {
			t.Errorf("capture.sh left %d staging entries behind (%v)", len(left), err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 3 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("the fixture directory holds %q; want only capture.sh and the two fixtures", names)
		}
		return dir, out, runErr
	}
	fixtures := func(t *testing.T, dir string) map[string]string {
		t.Helper()
		got := map[string]string{}
		for _, name := range []string{"version.txt", "run-help.txt"} {
			b, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			got[name] = string(b)
		}
		return got
	}

	for name, help := range map[string]string{
		"address on its own line":  "opencode run [message..]\n  --attach  e.g., http://192.0.2.10:4096\n",
		"address beside 127.0.0.1": "opencode run [message..]\n  --attach  e.g., http://127.0.0.1:4096 or http://192.0.2.10:4096\n",
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			dir, out, err := capture(t, help)
			if err == nil {
				t.Errorf("capture.sh accepted a capture naming 192.0.2.10; want a non-zero exit\n%s", out)
			}
			got := fixtures(t, dir)
			if got["version.txt"] != oldVersion || got["run-help.txt"] != oldHelp {
				t.Errorf("a refused capture rewrote the committed fixtures: %q", got)
			}
		})
	}

	t.Run("clean", func(t *testing.T) {
		const help = "opencode run [message..]\n  --attach  e.g., http://127.0.0.1:4096\n"
		dir, out, err := capture(t, help)
		if err != nil {
			t.Fatalf("capture.sh refused a clean capture: %v\n%s", err, out)
		}
		got := fixtures(t, dir)
		if got["version.txt"] != "9.9.9\n" || got["run-help.txt"] != help {
			t.Errorf("a clean capture was not written as captured: %q", got)
		}
	})
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

// TestOpenCodePreDispatchRefusesAnthropicWithTheSwitchSet: until the credential
// policy is enforced (#1616), nothing holds an anthropic/ stage to
// ANTHROPIC_API_KEY, and OpenCode would use a subscription or OAuth login it
// has stored. So PreDispatch refuses every anthropic/ model with the switch set
// and the key present, and names the claude-headless adapter as the way out
// (ADR-022 § 17). Driven by the registry, so a model release adds cases. Any
// other provider key passes this check and meets the gate instead.
func TestOpenCodePreDispatchRefusesAnthropicWithTheSwitchSet(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "set-by-the-test")
	a := NewOpenCodeAdapter()

	refused := []string{" anthropic/claude-sonnet-5", "Anthropic/claude-sonnet-5"}
	for _, m := range models.All() {
		if m.Provider == "anthropic" && !strings.Contains(m.ID, "/") {
			refused = append(refused, "anthropic/"+m.ID)
		}
	}
	if len(refused) == 2 {
		t.Fatal("the model registry has no anthropic model; nothing exercises a registry id")
	}

	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	for _, model := range refused {
		err := a.PreDispatch(RunOptions{Model: model})
		if err == nil {
			t.Errorf("PreDispatch(%q) with %s=1 allowed the dispatch; an anthropic/ model is refused until #1616", model, ExperimentalOpenCodeEnvVar)
			continue
		}
		for _, want := range []string{
			"ANTHROPIC_API_KEY", "subscription or OAuth login", "#1616",
			ExperimentalOpenCodeEnvVar + "=1 does not lift this refusal",
			"--adapter claude-headless", "NIGHTGAUGE_ADAPTER=claude-headless",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal of %q does not say %q: %v", model, want, err)
			}
		}
	}

	t.Setenv(ExperimentalOpenCodeEnvVar, "")
	for _, model := range []string{"lmstudio/qwen/qwen3.8-27b", "openai/gpt-5.5", "openrouter/anthropic/claude-sonnet-5"} {
		err := a.PreDispatch(RunOptions{Model: model})
		if err == nil || strings.Contains(err.Error(), "claude-headless") || !strings.Contains(err.Error(), "is experimental") {
			t.Errorf("PreDispatch(%q) with the switch unset = %v; want the gate's refusal, not the anthropic one", model, err)
		}
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
		"claude-sonnet-5", // a registry id names no provider either
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

// TestOpenCodeNeverInfersAProvider: every bare id is refused with remediation
// to name the provider, however well the registry knows it. Qualifying a bare
// registry id to its hosted provider would choose, for the operator, where the
// repository's code goes and what the stage costs (ADR-022, The command).
// Driven by the registry and the band vocabulary, so a model release adds
// cases instead of breaking the test.
func TestOpenCodeNeverInfersAProvider(t *testing.T) {
	bare := append([]string{}, models.BandsAscending...)
	for _, m := range models.All() {
		if !strings.Contains(m.ID, "/") {
			bare = append(bare, m.ID)
		}
	}
	if len(bare) <= len(models.BandsAscending) {
		t.Fatal("the model registry is empty; nothing exercises a bare registry id")
	}
	a := NewOpenCodeAdapter()
	for _, id := range bare {
		got, err := openCodeModelArg(id)
		if err == nil {
			t.Errorf("openCodeModelArg(%q) = %q; a bare id must be refused, never qualified", id, got)
			continue
		}
		for _, want := range []string{"names no provider", "<provider>/<model>"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal of %q does not say %q: %v", id, want, err)
			}
		}
		if err := a.ValidateModel(id); err == nil {
			t.Errorf("ValidateModel(%q) = nil; the manager would spawn with no provider named", id)
		}
		_, args, _ := a.BuildCommand(RunOptions{Model: id})
		for i, arg := range args {
			if arg == "-m" || strings.HasSuffix(arg, "/"+id) {
				t.Errorf("BuildCommand(%q) put a model on argv at %d: %q", id, i, args)
			}
		}
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

	// The forbidden flags this version's help lists. --yolo and
	// --dangerously-skip-permissions are hidden `run` options in 1.18.30 that
	// switch on the same auto-approval as --auto, so the help never shows them;
	// --mdns and --cors are not `run` options, and `run` exits 1 on either
	// (testdata/cli-help/README.md). All of them stay forbidden, and
	// TestOpenCodeNeverEmitsBypassFlags checks the full list.
	for _, f := range []string{"--auto", "--share"} {
		if _, ok := options[f]; !ok {
			t.Errorf("forbidden flag %s is no longer an option of opencode %s run; re-check ADR-022 § 9 before editing the list", f, ver)
		}
	}
}
