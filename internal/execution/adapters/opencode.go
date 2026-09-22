package adapters

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/opencodeplugin"
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
// missing on every dispatch it lets through. It refuses an anthropic/ model
// while ANTHROPIC_API_KEY is unset, and a model on a provider that runs on the
// forge's or a cloud platform's credentials (openCodeCredentialRefusal).
// Once the switch is set it holds the binary to the compat manifest's version
// policy (checkVersionPolicy, opencode_preflight.go), and a binary
// opencode.binary pins is the one spawned.
//
// Every spawn runs in a root private to its pipeline run
// (opencode_isolation.go, ADR-022 § 8), under a per-run config built from the
// stage and the machine-tier `opencode:` block (opencode_config.go): the
// manager prepares both through PrepareRunRoot, BuildCommand points OpenCode
// at them, and WithholdsEnv keeps the operator's OpenCode variables, the
// provider base-URL variables and the variables OpenCode's catalog binds to
// every other model service out of the child. The forge and cloud platform
// credentials stay.
type OpenCodeAdapter struct {
	// managedConfig replaces the managed OpenCode config files PrepareRunRoot
	// checks; nil means this machine's (openCodeManagedConfigFiles). Only
	// tests set it, because the real files are outside any directory a test
	// may write.
	managedConfig []string
	// settings replaces how the machine-tier `opencode:` block is read for a
	// stage's worktree; nil means config.LoadOpenCodeConfig. Only tests set
	// it, so a test never reads the machine's real config.
	settings func(worktreeDir string) (config.OpenCodeConfig, error)
	// pinned hands BuildCommand the binary opencode.binary pins, keyed by the
	// *RunRoot PrepareRunRoot returned for the dispatch, from the same read of
	// the machine-tier block the run's config is built from. BuildCommand
	// takes it out; the manager calls it once for each root.
	pinned sync.Map
}

// loadSettings reads the machine-tier `opencode:` block for a stage running
// in worktreeDir.
func (a *OpenCodeAdapter) loadSettings(worktreeDir string) (config.OpenCodeConfig, error) {
	if a.settings != nil {
		return a.settings(worktreeDir)
	}
	return config.LoadOpenCodeConfig(worktreeDir)
}

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

// openCodeRedactedEnv names the child-environment variables, for a dispatch to
// model, whose values are secrets Nightgauge lets the child have: the
// per-spawn server password, the forge tokens (openCodeForgeEnv and GH_TOKEN),
// and every variable the bundled catalog binds to the dispatched provider,
// whichever provider it is. The manager removes their values from everything
// the child prints before it is streamed or kept (ADR-022 § 22), so a tool
// that prints its environment does not put them in a log. The catalog
// variables of every other provider never reach the child
// (OpenCodeWithholdsEnv). Any other secret the child holds is not named here,
// and the output-redaction warning line says so.
func openCodeRedactedEnv(model string) []string {
	names := []string{openCodeServerPasswordEnvVar, "GH_TOKEN"}
	names = append(names, openCodeForgeEnv...)
	for _, v := range openCodeCatalogEnv[openCodeDispatchProvider(model)] {
		if !slices.Contains(names, v) {
			names = append(names, v)
		}
	}
	return names
}

// openCodeControl is one control a pipeline stage relies on that the opencode
// adapter does not enforce yet.
type openCodeControl struct {
	name string
	gap  string
}

// openCodeUnenforcedControls is the list PreDispatch prints on every dispatch
// it allows, and whose names the refusal cites. It is the operator's only
// disclosure of what an enabled dispatch runs without, so it lists exactly the
// rows of ADR-022's "Control not yet enforced" table, in the same order
// (TestOpenCodeUnenforcedControlsMatchADR). The change that implements a
// control removes its entry and its row; when the list is empty the gate goes
// with it.
var openCodeUnenforcedControls = []openCodeControl{
	{"failure classification", "a permission request OpenCode rejects on its own ends the run with exit code 0, so a stage that stopped early reads as a success"},
	{"output redaction", "only the values of the server password, GITHUB_TOKEN, GH_TOKEN, GITLAB_TOKEN and the variables OpenCode's catalog binds to the dispatched provider are removed from the captured output; every other secret the child holds, inherited from the environment or read by a tool from a file, stays in it, and an OpenCode error event carries the model endpoint's full URL"},
	{"project-config tamper gate", "the target repository's opencode.json and .opencode/ load unchecked: they cannot change a key the per-run config sets, such as the dispatched model's id or SDK package, except the model and steps cap of the general and explore subagents, which a mode entry of the same name replaces, but they can add to it, such as an agent or subagent of their own, with its own model on the dispatched provider and no steps cap, an MCP server of their own, which OpenCode starts beside the default branch's, a remote instructions URL OpenCode fetches, or a header on the provider block, which can carry a variable from the stage's environment, a forge token included, to the model server; they can also add options.model to the dispatched model's own entry or an agent's own options, or a variant, and still change the model actually served without touching the pinned id; on anthropic they can add options.speed or options.fallbacks to the dispatched model's entry and turn on fast mode or a server-side fallback the same way; and an agent's options.mcpServers can send ANTHROPIC_API_KEY, or another variable the run holds, as an authorization token to a URL of their choosing"},
	{"permission map", "tool permissions come from OpenCode's config, not from the stage's allowed tools"},
	{"safety plugin", "Nightgauge's careful-gate and stage-gate hooks do not run inside OpenCode"},
	{"endpoint policy", "the server behind a hosted provider key other than anthropic is whatever OpenCode's bundled catalog and a lower config layer make it: a provider block the repository or your OpenCode config names after that provider can send its API key to another base URL and the stage to another model, a LAN or public base URL of the declared endpoint is neither refused nor warned about, and an Ollama cloud model, which a local Ollama forwards to Ollama's hosted service, is dispatched like a local one"},
	{"stage limits", "the stage's token cap on a hosted model, anthropic's included, is not passed to OpenCode: its limits come from OpenCode's catalog unless the repository or your OpenCode config sets them, and a context limit of 0 set there means the session is never compacted; the steps cap, the stage timeout and, on a model the registry prices, the cost budget bound a run"},
	{"subagent cost", "the cost budget stops the stage at its own steps only: a subagent's steps never reach OpenCode's JSON events, so its usage is read once the stage has ended, and a stage whose subagents take it past its cost budget is not stopped while they run; it fails as budget_exceeded when it ends, and so does a stage that would otherwise succeed whose subagent usage was only partly read, since its budget cannot be verified; the cost budget prices every step, a subagent's included, at the dispatched model's rates, so a subagent on a pricier model of the same provider is under-counted; this gap is currently unreachable, not merely unenforced, because gates.js denies every `task` tool call unconditionally as AC9's fallback, so no subagent session can start to spend past the budget — the row stays because the gap becomes live again the moment AC9 is settled and the denial is lifted (#1748)"},
}

// PreDispatch implements the manager's optional pre-dispatch hook, which runs
// after worktree setup and before BuildCommand, so a refusal spawns nothing.
// ctx is the stage's: the version policy's probes run under it.
// `nightgauge opencode config` calls it too, so the SDK path meets the same
// checks.
//
// The project-config tamper gate (openCodeProjectConfigTamperCheck,
// opencode_guard.go, #1638) comes first: it is the one check here that reads
// the worktree the stage will run in, which is why this hook runs after
// worktree setup at all (ADR-022 § 8), and a tampered worktree is refused
// whatever the switch or a model's credentials say.
//
// The credential refusals come next (openCodeCredentialRefusal): the switch
// cannot satisfy them, so they are the reason to state, and no
// enabled-dispatch warning precedes them. Then the gate. Then the version
// policy (checkVersionPolicy): a binary below the compat manifest's floor, or
// newer than max-tested and failing its self-test or dispatched to a model
// server the operator runs, is refused as adapter_incompatible before
// anything is created. Last, a stderr line
// names every provider variable the environment holds that the stage, and so
// every tool it runs, will not get (openCodeWithheldProviderEnv), by name
// alone.
//
// Every other refusal that depends on the machine-tier `opencode:` block comes
// from PrepareRunRoot, which reads the block once and builds the run's config
// and environment from it before it creates anything: the block committed in
// the target repository, a zero limit, an undeclared endpoint, a malformed
// base_url, and, unless the operator opted into their own OpenCode config, a
// $HOME/.opencode holding config or the machine's managed OpenCode config.
// The version policy reads the block for its binary pin and, above
// max-tested, for the self-test's per-run config.
func (a *OpenCodeAdapter) PreDispatch(ctx context.Context, opts RunOptions) error {
	if err := openCodeProjectConfigTamperCheck(ctx, opts.WorktreeDir); err != nil {
		return err
	}
	if err := openCodeCredentialRefusal(opts.Model, os.LookupEnv); err != nil {
		return err
	}
	if err := openCodeGate(os.Getenv(ExperimentalOpenCodeEnvVar), os.Stderr); err != nil {
		return err
	}
	if err := a.checkVersionPolicy(ctx, opts); err != nil {
		return err
	}
	if names := openCodeWithheldProviderEnv(opts.Model, os.Environ()); len(names) > 0 {
		fmt.Fprintf(os.Stderr, "[opencode] withheld from this stage and every tool it runs: %s. Each is a provider base URL or a variable OpenCode's catalog binds to a model provider other than %q; a tool that needs one fails without it or uses a login of its own. The cloud platform and forge credentials in your environment are kept\n",
			strings.Join(names, ", "), openCodeDispatchProvider(opts.Model))
	}
	return nil
}

// openCodeCredentialRefusal is ADR-022 § 17 at dispatch: an anthropic/ model
// needs ANTHROPIC_API_KEY (openCodeAnthropicRefusal), and a model on a
// provider whose credentials are the forge's or a cloud platform's is refused
// (openCodePlatformProviderRefusal).
func openCodeCredentialRefusal(model string, lookup func(string) (string, bool)) error {
	if err := openCodeAnthropicRefusal(model, lookup); err != nil {
		return err
	}
	return openCodePlatformProviderRefusal(model)
}

// openCodePlatformProviderRefusal refuses a dispatch to a platform provider
// (openCodePlatformProviders), a provider whose credentials belong to a
// general-purpose account the stage keeps for its tools: the forge's
// GITHUB_TOKEN and GITLAB_TOKEN, and the cloud and data platforms'. OpenCode's
// catalog binds them to github-copilot, gitlab, amazon-bedrock, google-vertex
// and the rest, and those providers' loaders read the platforms' own
// credential chains as well, so such a run would spend a login that may be a
// subscription or OAuth one, such as the gh login behind GITHUB_TOKEN, on
// which github-copilot serves Claude and other models. A pipeline run
// authenticates only with a model provider's own API-key variable (ADR-022
// § 17), so the refusal holds whatever the switch says.
//
// The provider key is parsed and compared the way openCodeAnthropicRefusal
// compares it, case-insensitively.
func openCodePlatformProviderRefusal(model string) error {
	m := strings.TrimSpace(model)
	provider, _, qualified := strings.Cut(m, "/")
	if !qualified {
		return nil
	}
	for _, p := range openCodePlatformProviders {
		if !strings.EqualFold(provider, p) {
			continue
		}
		return fmt.Errorf(
			"model %q is refused: provider %q authenticates with the credentials of a platform account the stage keeps for its own tools (%s, and whatever else that platform's credential chain finds), which can be a subscription or OAuth login, and an OpenCode stage authenticates only with a model provider's own API-key variable, never with a subscription or OAuth login. "+
				"Name a provider that has one, such as anthropic/<model> with ANTHROPIC_API_KEY, or run the model on another adapter (--adapter or NIGHTGAUGE_ADAPTER). "+
				"See docs/decisions/022-opencode-multi-provider-adapter.md § 17",
			m, p, strings.Join(openCodeCatalogEnv[p], ", "))
	}
	return nil
}

// openCodeAnthropicRefusal is ADR-022 § 17's key requirement: an anthropic/
// stage through OpenCode has exactly one credential, the ANTHROPIC_API_KEY
// variable, so a dispatch to an anthropic/ model is refused before spawn while
// that variable is unset or empty, with the enable switch set or not. A
// subscription or OAuth login is never used, because a run reads neither of
// OpenCode's two sources of stored logins: the per-run data directory starts
// empty and never holds an auth.json (EnsureOpenCodeRunRoot,
// openCodeStoredLoginRefusal), and no inherited OPENCODE_* variable, the
// login-bearing OPENCODE_AUTH_CONTENT and OPENCODE_CONSOLE_TOKEN among them,
// reaches the child (OpenCodeWithholdsEnv). Nor does an inherited
// ANTHROPIC_BASE_URL, which would send the stage and its key to whatever
// server it names, a proxy serving a subscription included
// (openCodeEndpointEnv).
//
// The model is parsed the way OpenCodeModelArg parses it (trimmed, split on
// the first slash). The provider key is compared case-insensitively, so every
// spelling of it meets this requirement rather than only the model check.
func openCodeAnthropicRefusal(model string, lookup func(string) (string, bool)) error {
	m := strings.TrimSpace(model)
	provider, _, qualified := strings.Cut(m, "/")
	if !qualified || !strings.EqualFold(provider, "anthropic") {
		return nil
	}
	if key, ok := lookup("ANTHROPIC_API_KEY"); ok && key != "" {
		return nil
	}
	return fmt.Errorf(
		"model %q is refused: ANTHROPIC_API_KEY is not set, and an anthropic/ stage through OpenCode authenticates only with that variable, never with a subscription or OAuth login. "+
			"Set ANTHROPIC_API_KEY in the environment that runs nightgauge, or run the Anthropic model on the claude-headless adapter (--adapter claude-headless or NIGHTGAUGE_ADAPTER=claude-headless), which serves a Claude subscription. "+
			"See docs/decisions/022-opencode-multi-provider-adapter.md § 17",
		m)
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
// the pipeline's. OpenCodeModelArg defines the one accepted form.
func (a *OpenCodeAdapter) ValidateModel(model string) error {
	_, err := OpenCodeModelArg(model)
	return err
}

// openCodeProviderKeyRE is the shape of an OpenCode provider key, and of an
// operator endpoint id, which becomes one (ADR-022 § Multiple local
// endpoints). No leading dash, so the value can never read as a flag; no dot,
// so a host name or an address can never be used as a key.
var openCodeProviderKeyRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// OpenCodeModelArg returns the value for OpenCode's -m flag. Only an explicit
// "<provider>/<model>" is accepted, and it passes through with only its
// surrounding space trimmed; the manager records that value as the stage's
// upstream model (ADR-022 § 2). OpenCode
// splits it on the FIRST slash, so "lmstudio/qwen/qwen3.8-27b" is model
// "qwen/qwen3.8-27b" on provider "lmstudio".
//
// A bare id is refused, a model-registry id ("claude-sonnet-5") and a tier band
// ("sonnet") alike. The adapter never infers a provider: the provider decides
// where the repository's code goes and what the stage costs, so the operator
// names it (ADR-022, The command). Also refused: an empty model, and a value
// whose provider key or model id could read as a flag.
func OpenCodeModelArg(model string) (string, error) {
	m := strings.TrimSpace(model)
	if m == "" {
		return "", fmt.Errorf("the opencode adapter needs a model: set the stage model to <provider>/<model>, such as lmstudio/<model-id> or anthropic/<model-id>")
	}
	provider, id, qualified := strings.Cut(m, "/")
	if !qualified {
		return "", fmt.Errorf(
			"model %q names no provider, and the opencode adapter never infers one: set the stage model to <provider>/<model>, naming the provider that serves it, such as lmstudio/<model-id> or anthropic/<model-id>; "+
				"a bare registry id and a tier (%s) are refused alike",
			m, models.BandAlternation())
	}
	if !openCodeProviderKeyRE.MatchString(provider) {
		return "", fmt.Errorf("model %q is not valid for the opencode adapter: the provider key %q must be lowercase letters, digits and '-', starting with a letter or digit", m, provider)
	}
	if id == "" || strings.HasPrefix(id, "-") || strings.IndexFunc(id, isSpaceOrControl) >= 0 {
		return "", fmt.Errorf("model %q is not valid for the opencode adapter: the model id after %q/ must be non-empty, must not start with '-', and must not contain whitespace", m, provider)
	}
	return m, nil
}

func isSpaceOrControl(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r)
}

// openCodeSessionIDRE is the shape a session id session.js's own opencode
// plugin observes (opencodeplugin.Event.SessionID, #1624) — the only session
// id -s ever carries (#1643): one this run itself recorded, never one read
// from config, a prompt or model output. Anything else is silently omitted
// rather than trusted, mirroring OpenCodeModelArg's refusal shape.
var openCodeSessionIDRE = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

// openCodeRejectsFlagValue reports whether v must never reach argv as a flag
// value: empty, starting with '-' (so it can never be read as a flag of its
// own), or carrying whitespace/control characters. Mirrors OpenCodeModelArg's
// model-id check (#1643 AC 7); applied here to --variant and -s.
func openCodeRejectsFlagValue(v string) bool {
	return v == "" || strings.HasPrefix(v, "-") || strings.IndexFunc(v, isSpaceOrControl) >= 0
}

// openCodeVariantsForModel returns the Variants an operator declared for
// model's provider key and model id in settings' declared endpoints, or nil
// when the provider is not a declared endpoint, or declares no variants for
// this model.
//
// This is the config-declared-only path AC 4 and AC 8 name as the fallback:
// the issue's `opencode models --verbose` catalog-probe extension is not
// implemented here because no captured fixture of that flag's output exists
// against opencode 1.18.30 (testdata/opencode-cli holds none) to parse
// against, and shipping an unverified parser would be exactly the guess AC 8
// forbids. See the docs/decisions/022-opencode-multi-provider-adapter.md
// amendment this issue adds.
func openCodeVariantsForModel(settings config.OpenCodeConfig, model string) []string {
	provider, modelID, qualified := strings.Cut(strings.TrimSpace(model), "/")
	if !qualified {
		return nil
	}
	endpoints, err := OpenCodeEndpoints(settings)
	if err != nil {
		return nil
	}
	ep, ok := findOpenCodeEndpoint(endpoints, provider)
	if !ok {
		return nil
	}
	return openCodeDeclaredVariants(ep.Models, modelID)
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
// never shared, and nothing binds a discoverable listener. MaxTurns and
// MaxTokens, which OpenCode has no flag for, reach it through the per-run
// config as the steps cap and the endpoint's output limit
// (BuildOpenCodeConfig); Effort, AllowedTools and CostBudget are not mapped
// yet, and ADR-022 names the change that maps each.
//
// The environment includes opts.RunRoot's, which points OpenCode at the
// run's own root and carries the per-run config as OPENCODE_CONFIG_CONTENT
// (PrepareRunRoot). The command is the binary opencode.binary pins, which
// PrepareRunRoot hands over for that root, and otherwise the opencode on
// PATH. The manager always prepares one; a caller that skips it gets no
// isolation variables, no config and no pin, the way a model the manager's
// check would refuse gets no -m.
func (a *OpenCodeAdapter) BuildCommand(opts RunOptions) (string, []string, map[string]string) {
	name := openCodeBinaryName
	if opts.RunRoot != nil {
		if pin, ok := a.pinned.LoadAndDelete(opts.RunRoot); ok {
			name = pin.(string)
		}
	}
	args := []string{"run", "--format", "json", "--print-logs", "--log-level", "ERROR"}
	// The manager's ValidateModel call has already rejected a model this
	// cannot express, so -m is always present on a real dispatch. Omitting it
	// here, rather than guessing, keeps BuildCommand total for callers that
	// skip validation.
	if model, err := OpenCodeModelArg(opts.Model); err == nil {
		args = append(args, "-m", model)
	}
	if opts.WorktreeDir != "" {
		args = append(args, "--dir", opts.WorktreeDir)
	}
	// Effort → --variant (#1643 AC 4): only for a model that DECLARES the
	// dispatched effort rung as one of its variants — never guessed, never
	// emitted for a model with no declared variants (effort is recorded
	// "not applicable" for those, by the caller emitting no flag here).
	if opts.Effort != "" && !openCodeRejectsFlagValue(opts.Effort) {
		if settings, err := a.loadSettings(opts.WorktreeDir); err == nil {
			if slices.Contains(openCodeVariantsForModel(settings, opts.Model), opts.Effort) {
				args = append(args, "--variant", opts.Effort)
			}
		}
	}
	// Session resume (#1643 AC 5): only a session id this run itself
	// recorded and only in the shape session.js's plugin observes — see
	// openCodeSessionIDRE and RunOptions.ResumeSessionID.
	if id := strings.TrimSpace(opts.ResumeSessionID); id != "" && openCodeSessionIDRE.MatchString(id) {
		args = append(args, "-s", id)
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
	if opts.RunRoot != nil {
		maps.Copy(env, opts.RunRoot.Env)
		// EnvOperatorInstallRisk is manager-only (manager.go's operator-
		// install-risk watchdog reads it back from opts.RunRoot.Env
		// directly, before this function ever runs): the child opencode
		// process itself has no use for it, so it does not belong in the
		// child's own environment (#1635/A11 round 8).
		delete(env, opencodeplugin.EnvOperatorInstallRisk)
	}

	return name, args, env
}

// PrepareRunRoot implements the manager's optional per-run root hook, which
// runs after the pre-dispatch, model and effort checks and before
// BuildCommand (ADR-022 § 8, § 22).
//
// It reads the machine-tier `opencode:` block once (config.LoadOpenCodeConfig,
// which refuses one the target repository commits) and hands it, with the
// dispatch, to PrepareOpenCodeRun, the path `nightgauge opencode config`
// takes too: the per-run config is built first, and a refused one (a zero
// limit, an undeclared endpoint, a malformed base_url) creates nothing, nor
// does a $HOME/.opencode holding config or the machine's managed OpenCode
// config unless the block opts into the operator's own OpenCode config.
// The MCP servers are read from GitHub (OpenCodeMcpForge), as the identity the
// config in req.WorkspaceRoot names for the repository's owner.
// Then the root for req.ID is created, or the one an earlier stage of the run
// created is reused, and the environment that points OpenCode at it, with the
// config as OPENCODE_CONFIG_CONTENT, is resolved against the environment this
// process inherited. Creating a root also sweeps the roots no stage has used
// for OpenCodeOrphanMaxAge, which a crashed run leaves behind. A root holding
// stored logins refuses the dispatch (openCodeStoredLoginRefusal).
//
// The same read gives the binary pin (opencode.binary, ADR-022 § 7), which
// is checked again here (ResolveOpenCodeBinary) and handed to BuildCommand, so
// the binary spawned is the one the block names now, never one PATH finds in
// its place.
func (a *OpenCodeAdapter) PrepareRunRoot(req RunRootRequest) (*RunRoot, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("the opencode per-run root needs the home directory: %w", err)
	}
	settings, err := a.loadSettings(req.Run.WorktreeDir)
	if err != nil {
		return nil, err
	}
	var pin string
	if settings.Binary != "" {
		bin, err := ResolveOpenCodeBinary(settings.Binary, nil)
		if err != nil {
			return nil, err
		}
		pin = bin.Path
	}
	run, err := PrepareOpenCodeRun(OpenCodeRunRequest{
		Home:               home,
		ID:                 req.ID,
		MachineConfigDir:   req.MachineConfigDir,
		Run:                req.Run,
		Settings:           settings,
		Lookup:             os.LookupEnv,
		GOOS:               runtime.GOOS,
		ManagedConfigFiles: a.managedConfig,
		McpForge:           OpenCodeMcpForge(req.WorkspaceRoot),
		BinDir:             OpenCodeBinDir(),
	})
	if err != nil {
		return nil, err
	}
	ctx := req.Context
	if ctx == nil {
		ctx = context.Background()
	}
	// req.ID, not req.Run.RunID, is what names this handshake: req.ID is
	// always a run identity (RunRootRequest.ID's contract — either the
	// dispatch's own or one the manager minted for a dispatch that has none),
	// while req.Run.RunID is only ever non-empty for a dispatch that already
	// carries a runtime identity. A dispatch with no identity (the autonomous
	// issue-refine dispatch, buildRunOptions's nil-Runtime case) still gets a
	// minted root and a real spawn, so it must still get a handshake, or a
	// plugin that fails to load on that dispatch goes unnoticed (#1635 fix
	// round finding 1/5).
	if err := InstallNightgaugePlugin(ctx, run, req.Run.OutputFile, req.ID); err != nil {
		return nil, err
	}
	root := &RunRoot{Dir: run.RunDir, Env: run.Env, Endpoints: run.Endpoints}
	if pin != "" {
		a.pinned.Store(root, pin)
	}
	return root, nil
}

// InstallNightgaugePlugin writes the embedded Nightgauge OpenCode plugin
// (internal/execution/opencodeplugin, #1635) into run's PluginDir and adds it
// to the per-run config's `plugin` array — the only change this makes to the
// config PrepareOpenCodeRun already built; every other key is untouched.
// outputFile and runID are the dispatch's RunOptions.OutputFile/RunID.
//
// Both the adapter's PrepareRunRoot and the `nightgauge opencode config` verb
// (cmd/nightgauge/opencode.go) call this, immediately after
// PrepareOpenCodeRun, so the SDK path and the Go path get the identical
// plugin reference TestOpenCodeConfigVerbMatchesTheAdapter checks byte for
// byte.
//
// OPENCODE_DISABLE_PROJECT_CONFIG=1 is the load mechanism #1632's
// adversarial suite (TestOpenCodeDisableProjectConfig) proved isolates it
// (ADR-022 amendment 2026-09-14): with it set, neither the target
// repository's `.opencode/plugins/*` nor its own `plugin[]` entries load,
// only the per-run config's own list does, and the run's isolation
// environment already keeps a global OpenCode plugin directory out of the
// XDG config tree this run sees (#1616). AC2 requires this, and the
// maintainer decision on the #1635 fix round's second review keeps it set on
// every spawn rather than narrowing AC2 — restored after the round's first
// pass removed it (a regression: see the amendment for what that broke and
// what it costs to keep this set instead).
//
// The flag also drops the target repository's own opencode.json/opencode.jsonc
// and `.opencode/` directory WHOLESALE — its agent, provider, mode,
// permission and instructions keys, not only `plugin` — because 1.18.30
// offers no finer-grained switch: project config is one unit, on or off.
// #1638 does not restore any of it: ADR-022's own "control not yet enforced"
// table scopes #1638 to two rows, the permission map (opencode_guard.go's
// openCodePermissionMap, folded into this builder's output) and the
// project-config tamper gate (openCodeProjectConfigTamperCheck, called from
// PreDispatch) — a REFUSAL when the worktree's opencode.json* or .opencode/
// differs from the base branch, not a merge that lets a target repository's
// own project config reach a dispatch. TestOpenCodeIntegrationPerRunConfigReachesOpenCode
// and TestOpenCodeIntegrationAnthropicBlockHoldsItsServer's assertions stand
// unchanged by this ticket: Nightgauge's own locked keys are exactly what a
// dispatch sees, whatever a target repository's project config says. That
// narrowing is recorded in the ADR-022 amendment dated 2026-09-14, not
// invented by this comment.
//
// Without a run identity (runID == "") the plugin is still installed and
// referenced, but no handshake nonce or sentinel is minted. No production
// caller takes this branch today: the adapter's own PrepareRunRoot (above)
// always passes req.ID, which is a run identity by RunRootRequest.ID's
// contract, and `nightgauge opencode config` (cmd/nightgauge/opencode.go)
// also always passes a validated-or-minted id of its own, for parity with
// the adapter's config (TestOpenCodeConfigVerbMatchesTheAdapter) — even
// though it spawns nothing and so nothing ever verifies the handshake it
// arms. This branch exists only so a direct, non-CLI caller with no run
// identity of its own still gets a usable config back rather than an error.

// seedPluginDependencies runs before the plugin is referenced: opencode
// 1.18.30 installs the @opencode-ai/plugin npm package into any OpenCode
// config directory whose resolved config carries a non-empty `plugin` array —
// observed independent of whether the plugin itself is a local file with no
// import of that package — and every invocation that resolves such a config
// (`debug config`, `run`, ...) waits for that install before doing anything
// else. Pre-seeding this run's own OpenCode config directory with the
// embedded, version-pinned copy WriteDependencies extracts
// (opencode_plugin_deps.go, ADR-022 amendment 2026-09-14) is what keeps a
// dispatch from ever making a network request for it, cold cache or warm.
// ctx is honoured by the seed (bounded, local work; see
// writeEmbeddedPluginDependencies) rather than a disconnected
// context.Background() of its own.
func InstallNightgaugePlugin(ctx context.Context, run *OpenCodeRun, outputFile, runID string) error {
	entry, err := opencodeplugin.Write(run.PluginDir)
	if err != nil {
		return fmt.Errorf("opencode: writing the nightgauge plugin: %w", err)
	}
	seedPluginDependencies(ctx, filepath.Dir(run.PluginDir))
	// 1.18.30 installs @opencode-ai/plugin into every directory its own
	// ConfigPaths.directories names, not only the run's own XDG config
	// directory: $HOME/.opencode when it exists — under
	// opencode.inherit_user_config, that is the operator's real one, since
	// HOME is left untouched; otherwise it is the per-run HOME's own
	// home/.opencode, which linkOperatorHome never populates, so this path is
	// only ever in play under the inherit setting — and OPENCODE_CONFIG_DIR,
	// set only under opencode.inherit_user_config, at the operator's own XDG
	// OpenCode config directory. Nightgauge never
	// seeds or merges anything into either (#1635/A11 round 6, ADR-022
	// amendment 2026-09-15, narrowed AC1: an earlier round did, and the
	// review that found the seed it fell back to left an operator's own
	// `import ... from "@opencode-ai/plugin"` unresolvable — see
	// opencode_plugin_deps.go's package doc comment). OpenCode's own install
	// into its own config directories is the operator's environment, exactly
	// as in the operator's own OpenCode runs. operatorInstallRisk only READS
	// (never writes) whether either directory is in play AND does not already
	// satisfy the pin (#1635/A11 round 8, ADR-022 amendment 2026-09-15: a
	// directory that already satisfies opencode's own check gets OpenCode's
	// local, instant fast path, same as a run's own XDG-resolved config
	// directory, so it is not flagged) — so the manager can bound the wait
	// and classify a stage that never produces output as adapter_incompatible
	// instead of an unclassified hang (manager.go), rather than silently
	// waiting on the registry.
	if risk := operatorInstallRisk(run); risk != "" {
		run.Env[opencodeplugin.EnvOperatorInstallRisk] = risk
	}
	content, err := addNightgaugePluginToConfig(run.ConfigContent, entry)
	if err != nil {
		return fmt.Errorf("opencode: adding the nightgauge plugin to the per-run config: %w", err)
	}
	run.ConfigContent = content
	run.Env[openCodeConfigContentEnvVar] = content
	run.Env["OPENCODE_DISABLE_PROJECT_CONFIG"] = "1"
	run.Env[opencodeplugin.EnvPluginPath] = entry
	run.PluginVersion = opencodeplugin.PluginVersion

	if runID == "" {
		return nil
	}
	nonce, err := opencodeplugin.NewNonce()
	if err != nil {
		return fmt.Errorf("opencode: minting the plugin handshake nonce: %w", err)
	}
	sentinel := opencodeplugin.SentinelPath(outputFile, run.RunDir, runID)
	if err := opencodeplugin.DeleteStaleSentinel(sentinel); err != nil {
		return fmt.Errorf("opencode: %w", err)
	}
	run.Env[opencodeplugin.EnvNonce] = nonce
	run.Env[opencodeplugin.EnvSentinel] = sentinel
	return nil
}

// addNightgaugePluginToConfig sets content's top-level "plugin" key to an
// array holding exactly entry, leaving every other key byte-identical.
// content is decoded generically (map[string]any) rather than through
// openCodeConfigJSON: BuildOpenCodeConfig (opencode_config.go) now also
// extends the same builder with a "permission" key (#1638,
// opencode_guard.go), so content already carries one when this runs, and the
// decode-and-re-encode round-trips it unchanged, along with every other key —
// this remains the one place the config gains a "plugin" entry, because
// plugin identity is #1635's, not this builder's.
func addNightgaugePluginToConfig(content, entry string) (string, error) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(content), &raw); err != nil {
		return "", fmt.Errorf("decoding the per-run config: %w", err)
	}
	raw["plugin"] = []string{entry}
	out, err := json.Marshal(raw)
	if err != nil {
		return "", fmt.Errorf("re-encoding the per-run config: %w", err)
	}
	return string(out), nil
}

// WithholdsEnv implements the manager's optional hook deciding which inherited
// environment variables a stage's child must not receive: the manager removes
// every one it names from the host environment before it adds BuildCommand's
// exports. It decides on the name alone (OpenCodeWithholdsEnv): every
// OPENCODE_* variable, the provider base-URL variables, and every variable
// OpenCode's catalog binds to a model service other than the dispatched one.
// The forge tokens and the cloud platform credentials a stage's tools read
// are kept (openCodePlatformProviders).
func (a *OpenCodeAdapter) WithholdsEnv(opts RunOptions, key string) bool {
	return OpenCodeWithholdsEnv(opts.Model, key)
}

// RedactedEnv implements the manager's optional hook naming the variables of
// the child's environment whose values the manager removes from the child's
// captured output (openCodeRedactedEnv).
func (a *OpenCodeAdapter) RedactedEnv(opts RunOptions) []string {
	return openCodeRedactedEnv(opts.Model)
}

// RedactedLiterals implements the manager's optional hook naming secret
// values directly, rather than the name of an environment variable to look
// one up by: an endpoint's base_url, which is deliberately never an
// environment variable (it is referenced from the generated config through a
// {file:...} reference into a 0600 file in the run root), so RedactedEnv
// above cannot catch it. Observed on 1.18.30, an OpenCode --format json
// error event's metadata.url field carries the request's full URL (ADR-022
// § 22, § Endpoints, #1678).
//
// It returns the base_url of every endpoint this run declared
// (opts.RunRoot.Endpoints), resolved back against the machine-tier
// opencode: block, so a run that never touched a declared endpoint (a hosted
// dispatch, or one with no RunRoot) returns none.
func (a *OpenCodeAdapter) RedactedLiterals(opts RunOptions) []string {
	if opts.RunRoot == nil || len(opts.RunRoot.Endpoints) == 0 {
		return nil
	}
	settings, err := config.LoadOpenCodeConfig(opts.WorktreeDir)
	if err != nil {
		return nil
	}
	endpoints, err := OpenCodeEndpoints(settings)
	if err != nil {
		return nil
	}
	var values []string
	for _, id := range opts.RunRoot.Endpoints {
		if ep, ok := findOpenCodeEndpoint(endpoints, id); ok && ep.BaseURL != "" {
			values = append(values, ep.BaseURL)
		}
	}
	return values
}
