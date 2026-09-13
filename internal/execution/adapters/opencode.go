package adapters

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/models"
)

// OpenCodeAdapter implements SkillRunner for the OpenCode CLI (`opencode run`).
//
// OpenCode is the first multi-provider adapter: one adapter id reaches local
// model servers (LM Studio, Ollama) and hosted providers, selected per dispatch
// by the provider-qualified model id OpenCode's -m flag takes.
// docs/decisions/022-opencode-multi-provider-adapter.md (ADR-022) records the
// design and what was observed of opencode 1.18.30.
//
// The adapter is EXPERIMENTAL. The controls a pipeline stage relies on are not
// built yet (openCodeUnenforcedControls), so PreDispatch refuses every dispatch
// unless the operator sets ExperimentalOpenCodeEnvVar=1, and prints what is
// missing on every dispatch it lets through.
type OpenCodeAdapter struct{}

// NewOpenCodeAdapter creates an OpenCode CLI adapter.
func NewOpenCodeAdapter() *OpenCodeAdapter {
	return &OpenCodeAdapter{}
}

func (a *OpenCodeAdapter) Name() string { return "opencode" }

// Agentic is true: `opencode run` drives a real tool loop (bash, edit, write,
// read, grep, glob, task, webfetch), unlike the lm-studio/ollama chat bridges.
func (a *OpenCodeAdapter) Agentic() bool { return true }

// UsesStdin is true, and it is the adapter's prompt channel: the prompt never
// goes on argv (ADR-022 § 19). opencode 1.18.30 reads piped stdin as the
// message when no positional message is given. A positional prompt would be
// capped at 128 KiB per argument on Linux, visible to every local user in the
// process table, and parsed as flags when it starts with `--` — an issue body
// that opens with `--auto` would switch on auto-approval.
func (a *OpenCodeAdapter) UsesStdin() bool { return true }

// ExperimentalOpenCodeEnvVar is the enable gate for the opencode adapter
// (ADR-022). A dispatch proceeds only when it is exactly "1".
//
// Default OFF. ADR-020 requires every default-off switch to state its reason
// beside the flag, and the reason is SECURITY, not footprint or cost: a
// dispatch through this adapter runs without the controls listed in
// openCodeUnenforcedControls, and setting the switch accepts that. It is read
// from the process environment only, deliberately, so a committed repository
// config can never enable it on the operator's behalf.
const ExperimentalOpenCodeEnvVar = "NIGHTGAUGE_EXPERIMENTAL_OPENCODE"

// openCodeServerPasswordEnvVar is the basic-auth password for the server
// OpenCode runs in-process. opencode 1.18.30's `run` opened no TCP listener
// when observed (ADR-022 § 18); every spawn still gets a fresh random value so
// that any listener a later version or an added flag opens is authenticated.
const openCodeServerPasswordEnvVar = "OPENCODE_SERVER_PASSWORD"

// openCodeControl is one control a pipeline stage relies on that the opencode
// adapter does not enforce yet.
type openCodeControl struct {
	name string
	gap  string
}

// openCodeUnenforcedControls is the list PreDispatch prints on every dispatch
// it allows, and whose names the refusal cites. The change that implements a
// control removes its entry; when the list is empty the gate goes with it.
var openCodeUnenforcedControls = []openCodeControl{
	{"stream parsing", "token usage, cost and the served model are not recorded, because OpenCode's JSON events reach the Claude stream parser"},
	{"failure classification", "a permission request OpenCode rejects on its own ends the run with exit code 0, so a stage that stopped early reads as a success"},
	{"run isolation", "OpenCode reads the operator's own global config, credentials, plugins and session database"},
	{"project-config tamper gate", "the target repository's opencode.json and .opencode/ load unchecked"},
	{"permission map", "tool permissions come from OpenCode's config, not from the stage's allowed tools"},
	{"safety plugin", "Nightgauge's careful-gate and stage-gate hooks do not run inside OpenCode"},
	{"egress defaults", "share, autoupdate, the model-catalog fetch, LSP downloads and webfetch follow OpenCode's own defaults"},
	{"credential policy", "an OAuth login stored by OpenCode can be used, and ANTHROPIC_API_KEY is not required for anthropic models"},
	{"version policy", "the opencode binary's version is not checked against a floor"},
}

// PreDispatch implements the manager's optional pre-dispatch hook, which runs
// after worktree setup and before BuildCommand, so a refusal spawns nothing.
func (a *OpenCodeAdapter) PreDispatch(RunOptions) error {
	return openCodeGate(os.Getenv(ExperimentalOpenCodeEnvVar), os.Stderr)
}

// openCodeGate refuses the dispatch unless switchValue is exactly "1", and
// writes the unenforced-controls warning to warn when it allows one.
func openCodeGate(switchValue string, warn io.Writer) error {
	names := make([]string, len(openCodeUnenforcedControls))
	for i, c := range openCodeUnenforcedControls {
		names[i] = c.name
	}
	if switchValue != "1" {
		return fmt.Errorf(
			"adapter \"opencode\" is experimental and does not dispatch by default: these controls are not enforced yet: %s. "+
				"To dispatch anyway, set %s=1 in the environment that runs nightgauge (it is read from the environment only, so no config file can set it); "+
				"otherwise choose another agentic adapter with --adapter or NIGHTGAUGE_ADAPTER. "+
				"See docs/decisions/022-opencode-multi-provider-adapter.md",
			strings.Join(names, ", "), ExperimentalOpenCodeEnvVar)
	}
	fmt.Fprintf(warn, "[opencode] WARNING: experimental adapter enabled by %s=1; this dispatch runs without %d controls that are not enforced yet:\n",
		ExperimentalOpenCodeEnvVar, len(openCodeUnenforcedControls))
	for _, c := range openCodeUnenforcedControls {
		fmt.Fprintf(warn, "[opencode]   - %s: %s\n", c.name, c.gap)
	}
	return nil
}

// ValidateModel implements the manager's optional pre-spawn model check. A
// dispatch must name a model OpenCode can take on -m, because without -m
// OpenCode falls back to the model its own config names — the operator's, not
// the pipeline's. openCodeModelArg defines the accepted forms.
func (a *OpenCodeAdapter) ValidateModel(model string) error {
	_, err := openCodeModelArg(model)
	return err
}

// openCodeHostedProviderKeys maps a model-registry provider to the OpenCode
// provider key that serves its registry ids. A bare registry id is qualified
// with this key; ADR-022 § 1 gives the reverse table.
var openCodeHostedProviderKeys = map[string]string{
	"anthropic": "anthropic",
	"google":    "google",
	"openai":    "openai",
	"xai":       "xai",
}

// openCodeProviderKeyRE is the shape of an OpenCode provider key, and of an
// operator endpoint id, which becomes one (ADR-022 § Multiple local
// endpoints). No leading dash, so the value can never read as a flag; no dot,
// so a host name or an address can never be used as a key.
var openCodeProviderKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// openCodeModelArg returns the value for OpenCode's -m flag:
//
//   - "<provider>/<model>" passes through unchanged. OpenCode splits it on the
//     FIRST slash, so "lmstudio/qwen/qwen3.8-27b" is model "qwen/qwen3.8-27b"
//     on provider "lmstudio".
//   - A concrete, non-deprecated model-registry id from a hosted provider is
//     qualified with that provider's key: "claude-sonnet-5" becomes
//     "anthropic/claude-sonnet-5".
//
// Anything else is an error: an empty model, a tier band (it names no
// provider, and provider resolution for a multi-provider adapter is not built
// yet), an unknown bare id, or a value whose parts could read as a flag.
func openCodeModelArg(model string) (string, error) {
	m := strings.TrimSpace(model)
	if m == "" {
		return "", fmt.Errorf("the opencode adapter needs a model: set the stage model to <provider>/<model>, such as lmstudio/<model-id> or anthropic/<model-id>")
	}
	if provider, id, qualified := strings.Cut(m, "/"); qualified {
		if !openCodeProviderKeyRE.MatchString(provider) {
			return "", fmt.Errorf("model %q is not valid for the opencode adapter: the provider key %q must be lowercase letters, digits and '-', starting with a letter or digit", m, provider)
		}
		if id == "" || strings.HasPrefix(id, "-") || strings.IndexFunc(id, isSpaceOrControl) >= 0 {
			return "", fmt.Errorf("model %q is not valid for the opencode adapter: the model id after %q/ must be non-empty, must not start with '-', and must not contain whitespace", m, provider)
		}
		return m, nil
	}
	if d, ok := models.Resolve("", m); ok && d.ID == m && !d.Deprecated {
		if key, ok := openCodeHostedProviderKeys[d.Provider]; ok {
			return key + "/" + d.ID, nil
		}
	}
	hosted := make([]string, 0, len(openCodeHostedProviderKeys))
	for p := range openCodeHostedProviderKeys {
		hosted = append(hosted, p)
	}
	sort.Strings(hosted)
	return "", fmt.Errorf(
		"model %q is not valid for the opencode adapter: name it as <provider>/<model> (such as lmstudio/<model-id>), or use a concrete, non-deprecated registry id from %s; "+
			"a tier (%s) names no provider, and the opencode adapter does not infer one",
		m, strings.Join(hosted, ", "), models.BandAlternation())
}

func isSpaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

// BuildCommand builds `opencode run` for one stage.
//
// Every flag here is load-bearing, and so is every flag that is absent. The
// argv is exactly
//
//	opencode run --format json --print-logs --log-level ERROR -m <provider/model> --dir <worktree>
//
// and the prompt goes on stdin (UsesStdin). The adapter never emits --auto,
// --yolo, --dangerously-skip-permissions, --share or --mdns: approval comes
// from a permission map that only allows or denies (ADR-022 § 9), sessions are
// never shared, and nothing binds a discoverable listener. RunOptions fields
// OpenCode has no flag for (MaxTurns, Effort, AllowedTools, CostBudget,
// MaxTokens) are not mapped yet; ADR-022 names the change that maps each.
func (a *OpenCodeAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	args := []string{"run", "--format", "json", "--print-logs", "--log-level", "ERROR"}
	// The manager's ValidateModel call has already rejected a model this
	// cannot express, so -m is always present on a real dispatch. Omitting it
	// here, rather than guessing, keeps BuildCommand total for callers that
	// skip validation.
	if model, err := openCodeModelArg(opts.Model); err == nil {
		args = append(args, "-m", model)
	}
	if opts.WorktreeDir != "" {
		args = append(args, "--dir", opts.WorktreeDir)
	}

	env := map[string]string{
		"NIGHTGAUGE_ISSUE_NUMBER": fmt.Sprintf("%d", opts.IssueNumber),
		"NIGHTGAUGE_REPO":         opts.Repo,
		"NIGHTGAUGE_STAGE":        opts.Stage,
		"NIGHTGAUGE_ADAPTER":      "opencode",
		// Mirrors the --format json flag above, and is the value run-stage.sh
		// exports for every non-Claude adapter on the extension path. The
		// posture is recorded in outputFormatPosture in adapters_test.go.
		"NIGHTGAUGE_OUTPUT_FORMAT": "json",
		// A fresh value per spawn, never logged and never on argv.
		openCodeServerPasswordEnvVar: rand.Text(),
	}
	// The model this stage was dispatched to run on, as dispatched; see the
	// same export in grok.go for why the stamp needs it before the stage ends.
	if opts.Model != "" {
		env["NIGHTGAUGE_DISPATCH_MODEL"] = opts.Model
	}
	if opts.ContextFile != "" {
		env["NIGHTGAUGE_CONTEXT_FILE"] = opts.ContextFile
	}
	if opts.OutputFile != "" {
		env["NIGHTGAUGE_OUTPUT_FILE"] = opts.OutputFile
	}
	if opts.TargetRepo != "" {
		env["NIGHTGAUGE_TARGET_REPO"] = opts.TargetRepo
	}
	if opts.RunID != "" {
		env[RunIDEnvVar] = opts.RunID
	}
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		env["GITHUB_TOKEN"] = token
	}

	return "opencode", args, env
}
