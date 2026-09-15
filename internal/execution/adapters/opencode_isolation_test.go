package adapters

import (
	"context"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

// testRunID is a canonical run identity, the only shape a run root is named by.
const testRunID = "01890a5d-ac96-774b-bcce-b302099a8057"

// envLookup turns a fixture environment into the Lookup OpenCodeIsolation
// takes, so no test resolves anything from the real environment.
func envLookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

// TestOpenCodeIsolationEnv pins the environment an opencode spawn runs in
// (ADR-022 § 8), exactly: the four XDG directories inside the run root, the
// disable flags, and the tools the XDG move would otherwise take from the
// operator, each pinned back to what it resolves to outside the run. Exact map
// equality, so an extra variable fails as surely as a missing one.
func TestOpenCodeIsolationEnv(t *testing.T) {
	const root, home = "/runs/r", "/home/op"
	base := func() map[string]string {
		return map[string]string{
			"XDG_CONFIG_HOME":                     root + "/config",
			"XDG_DATA_HOME":                       root + "/data",
			"XDG_CACHE_HOME":                      root + "/cache",
			"XDG_STATE_HOME":                      root + "/state",
			"OPENCODE_DISABLE_MODELS_FETCH":       "1",
			"OPENCODE_DISABLE_AUTOUPDATE":         "1",
			"OPENCODE_DISABLE_LSP_DOWNLOAD":       "1",
			"OPENCODE_DISABLE_DEFAULT_PLUGINS":    "1",
			"OPENCODE_DISABLE_SHARE":              "1",
			"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT": "1",
			"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS": "1",
			"OPENCODE_DISABLE_EXTERNAL_SKILLS":    "1",
			"GH_CONFIG_DIR":                       home + "/.config/gh",
			"NIGHTGAUGE_CONFIG_HOME":              "/machine/tier",
			"GOCACHE":                             home + "/.cache/go-build",
		}
	}
	for _, tc := range []struct {
		name      string
		goos      string
		inherited map[string]string
		inherit   bool
		change    func(map[string]string)
	}{
		{name: "linux, nothing inherited", goos: "linux"},
		{name: "operator XDG_CONFIG_HOME moves gh's default", goos: "linux",
			inherited: map[string]string{"XDG_CONFIG_HOME": "/xdg"},
			change:    func(w map[string]string) { w["GH_CONFIG_DIR"] = "/xdg/gh" }},
		{name: "operator GH_CONFIG_DIR wins", goos: "linux",
			inherited: map[string]string{"XDG_CONFIG_HOME": "/xdg", "GH_CONFIG_DIR": "/gh"},
			change:    func(w map[string]string) { w["GH_CONFIG_DIR"] = "/gh" }},
		{name: "linux go cache follows the operator's XDG_CACHE_HOME", goos: "linux",
			inherited: map[string]string{"XDG_CACHE_HOME": "/cache"},
			change:    func(w map[string]string) { w["GOCACHE"] = "/cache/go-build" }},
		{name: "darwin go cache ignores XDG_CACHE_HOME", goos: "darwin",
			inherited: map[string]string{"XDG_CACHE_HOME": "/cache"},
			change:    func(w map[string]string) { w["GOCACHE"] = home + "/Library/Caches/go-build" }},
		{name: "an operator GOCACHE passes through", goos: "linux",
			inherited: map[string]string{"GOCACHE": "/fast/go-build"},
			change:    func(w map[string]string) { delete(w, "GOCACHE") }},
		{name: "an empty GOCACHE is unset", goos: "linux",
			inherited: map[string]string{"GOCACHE": ""}},
		{name: "inherit_user_config layers the operator's config directory", goos: "linux", inherit: true,
			change: func(w map[string]string) { w["OPENCODE_CONFIG_DIR"] = home + "/.config/opencode" }},
		{name: "inherit_user_config follows the operator's XDG_CONFIG_HOME", goos: "linux", inherit: true,
			inherited: map[string]string{"XDG_CONFIG_HOME": "/xdg"},
			change: func(w map[string]string) {
				w["OPENCODE_CONFIG_DIR"] = "/xdg/opencode"
				w["GH_CONFIG_DIR"] = "/xdg/gh"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := base()
			if tc.change != nil {
				tc.change(want)
			}
			got, err := OpenCodeIsolationEnv(OpenCodeIsolation{
				Root: root, Home: home, Lookup: envLookup(tc.inherited), GOOS: tc.goos,
				MachineConfigDir: "/machine/tier", InheritUserConfig: tc.inherit,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, want) {
				t.Errorf("OpenCodeIsolationEnv =\n  %v\nwant\n  %v", got, want)
			}
		})
	}

	for name, in := range map[string]OpenCodeIsolation{
		"relative root":         {Root: "r", Home: home, Lookup: envLookup(nil), MachineConfigDir: "/m"},
		"relative home":         {Root: root, Home: "h", Lookup: envLookup(nil), MachineConfigDir: "/m"},
		"no machine config dir": {Root: root, Home: home, Lookup: envLookup(nil)},
		"no lookup":             {Root: root, Home: home, MachineConfigDir: "/m"},
	} {
		if _, err := OpenCodeIsolationEnv(in); err == nil {
			t.Errorf("%s: OpenCodeIsolationEnv succeeded; want an error, not a half-pinned environment", name)
		}
	}
}

// TestOpenCodeDisableFlags: the seven upstream switches issue #1616 names, and
// OPENCODE_DISABLE_EXTERNAL_SKILLS, which 1.18.30 needs to keep the operator's
// ~/.agents/skills out, are "1" on every spawn. The blanket
// OPENCODE_DISABLE_CLAUDE_CODE is never set, because it would also drop what
// ADR-022 § 11 keeps. OPENCODE_DISABLE_PROJECT_CONFIG is absent from THIS
// function's own output — OpenCodeIsolationEnv, the base isolation env — not
// because it is unset on a dispatch: InstallNightgaugePlugin
// (opencode.go, ADR-022 amendment 2026-09-14) sets it separately, on every
// OpenCode dispatch in practice, for AC2. The names are literal here, so
// dropping one from the adapter's list fails.
func TestOpenCodeDisableFlags(t *testing.T) {
	env, err := OpenCodeIsolationEnv(OpenCodeIsolation{Root: "/r", Home: "/h", Lookup: envLookup(nil), GOOS: "linux", MachineConfigDir: "/m"})
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{
		"OPENCODE_DISABLE_MODELS_FETCH",
		"OPENCODE_DISABLE_AUTOUPDATE",
		"OPENCODE_DISABLE_LSP_DOWNLOAD",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS",
		"OPENCODE_DISABLE_SHARE",
		"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
		"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
		"OPENCODE_DISABLE_EXTERNAL_SKILLS",
	} {
		if env[flag] != "1" {
			t.Errorf("%s = %q, want \"1\"", flag, env[flag])
		}
	}
	for _, absent := range []string{"OPENCODE_DISABLE_CLAUDE_CODE", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_PURE"} {
		if v, ok := env[absent]; ok {
			t.Errorf("%s is set (%q); it must not be", absent, v)
		}
	}
}

// TestOpenCodeWithholdsEnv is the inherited-environment policy (ADR-022 § 8,
// § 17). Every OPENCODE_* variable is withheld, a login-bearing one and a
// name no version has yet alike, and so are the provider base-URL variables,
// whatever the provider. A local model inherits none of the variables
// OpenCode's catalog binds to a hosted model service: the issue's seven, and
// the rest of the catalog's, each of which makes OpenCode load its provider
// (GROQ_API_KEY adds groq's models). The lists are literal here, so narrowing
// the adapter's fails. A hosted model keeps its own provider's variables and
// no other model service's. The forge tokens and the cloud platform
// credentials a stage's tools read pass through whatever the provider
// (TestOpenCodeKeepsPlatformCredentialFamiliesWhole), and so do a variable no
// catalog entry binds and everything else.
func TestOpenCodeWithholdsEnv(t *testing.T) {
	cloudKeys := []string{
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "XAI_API_KEY", "GEMINI_API_KEY",
		"GOOGLE_API_KEY", "OPENROUTER_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY",
		"GROQ_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY", "LMSTUDIO_API_KEY",
		"AZURE_API_KEY", "AZURE_RESOURCE_NAME", "NVIDIA_API_KEY", "OLLAMA_API_KEY",
	}
	endpointVars := []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"}
	openCodeVars := []string{
		"OPENCODE_AUTH_CONTENT", "OPENCODE_CONSOLE_TOKEN", "OPENCODE_DB",
		"OPENCODE_CONFIG_CONTENT", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR",
		"OPENCODE_MODELS_PATH", "OPENCODE_PERMISSION", "OPENCODE_SERVER_PASSWORD",
		"OPENCODE_API_KEY", "OPENCODE_A_VARIABLE_A_LATER_VERSION_ADDS",
	}
	passes := []string{
		"PATH", "HOME", "GITHUB_TOKEN", "GITLAB_TOKEN", "GH_TOKEN", "NIGHTGAUGE_ISSUE_NUMBER",
		"opencode_config", "DEEPSEEK_API_KEY_NOT_LISTED", "NIGHTGAUGE_TEST_UNBOUND_API_KEY",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "AWS_REGION",
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "HF_TOKEN",
		"CLOUDFLARE_API_TOKEN", "DATABRICKS_HOST",
	}
	alwaysWithheld := slices.Concat(endpointVars, openCodeVars)

	for _, model := range []string{"lmstudio/qwen/qwen3.8-27b", "ollama/qwen3-coder:30b"} {
		provider, _, _ := models.ParseOpenCodeModel(model)
		if !models.IsLocalProvider(provider) {
			t.Fatalf("%s parses to provider %q, which is not local; the fixture no longer exercises a local run", model, provider)
		}
	}
	for _, model := range []string{"lmstudio/qwen/qwen3.8-27b", "ollama/qwen3-coder:30b", "lmstudio-remote/qwen/qwen3.8-27b", ""} {
		for _, key := range slices.Concat(cloudKeys, alwaysWithheld) {
			if key == "LMSTUDIO_API_KEY" && strings.HasPrefix(model, "lmstudio/") {
				continue // lmstudio's own, checked below
			}
			if !OpenCodeWithholdsEnv(model, key) {
				t.Errorf("model %q: %s reaches the child; a local run inherits no OpenCode or base-URL variable and no hosted model service's credentials", model, key)
			}
		}
		for _, key := range passes {
			if OpenCodeWithholdsEnv(model, key) {
				t.Errorf("model %q: %s is withheld; only OPENCODE_*, the base-URL variables and other model services' catalog variables are", model, key)
			}
		}
	}
	if OpenCodeWithholdsEnv("lmstudio/qwen/qwen3.8-27b", "LMSTUDIO_API_KEY") {
		t.Error("an lmstudio/ run lost LMSTUDIO_API_KEY, its own provider's catalog variable")
	}

	for model, own := range map[string][]string{
		"anthropic/claude-sonnet-5":       {"ANTHROPIC_API_KEY"},
		"openai/gpt-5.5":                  {"OPENAI_API_KEY"},
		"xai/grok-4.6":                    {"XAI_API_KEY"},
		"google/gemini-2.5-pro":           {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
		"openrouter/meta-llama/llama-4":   {"OPENROUTER_API_KEY"},
		"openrouter/anthropic/claude-x-1": {"OPENROUTER_API_KEY"},
		"groq/llama-4-scout":              {"GROQ_API_KEY"},
		"deepseek/deepseek-chat":          {"DEEPSEEK_API_KEY"},
		"amazon-bedrock/anthropic.claude": {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK"},
	} {
		for _, key := range cloudKeys {
			if got, want := OpenCodeWithholdsEnv(model, key), !slices.Contains(own, key); got != want {
				t.Errorf("model %q: withholds %s = %v, want %v", model, key, got, want)
			}
		}
		for _, key := range alwaysWithheld {
			if !OpenCodeWithholdsEnv(model, key) {
				t.Errorf("model %q: %s reaches the child", model, key)
			}
		}
		for _, key := range passes {
			if OpenCodeWithholdsEnv(model, key) {
				t.Errorf("model %q: %s is withheld", model, key)
			}
		}
	}

	a := NewOpenCodeAdapter()
	if !a.WithholdsEnv(RunOptions{Model: "lmstudio/q"}, "OPENAI_API_KEY") || a.WithholdsEnv(RunOptions{Model: "openai/gpt-5.5"}, "OPENAI_API_KEY") {
		t.Error("the adapter's WithholdsEnv hook does not apply the dispatched model")
	}
}

// TestOpenCodeKeepsPlatformCredentialFamiliesWhole: removing part of a
// platform's credentials does not leave a stage's tools without credentials.
// It moves them to the next source in the platform's credential chain, which
// can be another account in another region, and nothing says so. With
// AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_REGION removed and
// AWS_SESSION_TOKEN left, the AWS CLI reads ~/.aws/credentials and
// ~/.aws/config instead. With GOOGLE_APPLICATION_CREDENTIALS and
// GOOGLE_VERTEX_PROJECT removed and GOOGLE_CLOUD_PROJECT left, Google's
// clients, and OpenCode's google-vertex provider, which still loads on
// GOOGLE_CLOUD_PROJECT, use the operator's own application default
// credentials (ADR-022 § 8). So no dispatch, to any provider, withholds a
// variable of a platform family, whether the catalog lists it or not. The
// families are literal here, and the catalog variables a run on an
// uncatalogued provider keeps are exactly theirs, so the adapter's set can be
// neither narrowed nor widened unnoticed.
func TestOpenCodeKeepsPlatformCredentialFamiliesWhole(t *testing.T) {
	families := map[string][]string{
		"AWS": {
			"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK",
			"AWS_SESSION_TOKEN", "AWS_DEFAULT_REGION", "AWS_PROFILE",
		},
		"Google Cloud": {
			"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_VERTEX_PROJECT", "GOOGLE_VERTEX_LOCATION",
			"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
		},
		"Cloudflare": {
			"CLOUDFLARE_API_TOKEN", "CLOUDFLARE_API_KEY", "CLOUDFLARE_ACCOUNT_ID", "CLOUDFLARE_GATEWAY_ID",
			"CLOUDFLARE_EMAIL",
		},
		"Databricks":       {"DATABRICKS_HOST", "DATABRICKS_TOKEN", "DATABRICKS_CONFIG_PROFILE"},
		"DigitalOcean":     {"DIGITALOCEAN_ACCESS_TOKEN"},
		"Snowflake":        {"SNOWFLAKE_ACCOUNT", "SNOWFLAKE_CORTEX_PAT", "SNOWFLAKE_USER"},
		"Hugging Face":     {"HF_TOKEN", "HF_HOME"},
		"Weights & Biases": {"WANDB_API_KEY"},
		"Vultr":            {"VULTR_API_KEY"},
		"the forge":        {"GITHUB_TOKEN", "GITLAB_TOKEN", "GH_TOKEN"},
	}
	dispatches := []string{"lmstudio/qwen/qwen3.8-27b", "ollama/qwen3-coder:30b", "lmstudio-remote/qwen/qwen3.8-27b", ""}
	for _, provider := range slices.Sorted(maps.Keys(openCodeCatalogEnv)) {
		dispatches = append(dispatches, provider+"/m")
	}
	var familyCatalogVars []string
	for _, family := range slices.Sorted(maps.Keys(families)) {
		for _, key := range families[family] {
			if openCodeCatalogEnvNames[key] {
				familyCatalogVars = append(familyCatalogVars, key)
			}
			var withheldFrom []string
			for _, model := range dispatches {
				if OpenCodeWithholdsEnv(model, key) {
					withheldFrom = append(withheldFrom, model)
				}
			}
			if len(withheldFrom) > 0 {
				t.Errorf("%s (%s) is withheld from %d dispatches, %q among them; the rest of its family stays, so its tools fall back to another source of %s credentials",
					key, family, len(withheldFrom), withheldFrom[0], family)
			}
		}
	}

	// A run on a provider the catalog does not know keeps no catalog
	// variable of its own, so what it keeps is the platform families alone.
	var kept []string
	for key := range openCodeCatalogEnvNames {
		if !OpenCodeWithholdsEnv("lmstudio-remote/qwen/qwen3.8-27b", key) {
			kept = append(kept, key)
		}
	}
	slices.Sort(kept)
	slices.Sort(familyCatalogVars)
	if !slices.Equal(kept, slices.Compact(familyCatalogVars)) {
		t.Errorf("a run on an uncatalogued provider keeps the catalog variables %q; want exactly the platform families' %q", kept, familyCatalogVars)
	}
}

// TestOpenCodeDispatchNamesTheVariablesItWithholds: a stage and every tool it
// runs lose the variables the dispatch withholds, so the withholding is never
// silent. An enabled dispatch prints one stderr line naming, by name alone and
// sorted, each withheld provider variable the environment holds a value for,
// and saying why. A variable the stage keeps is not named: a platform
// credential, a forge token, or its own provider's key. Nor is an OPENCODE_*
// variable, which only OpenCode reads. With nothing withheld there is no line.
func TestOpenCodeDispatchNamesTheVariablesItWithholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.opencode
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	for key := range openCodeCatalogEnvNames {
		t.Setenv(key, "")
	}
	for _, key := range []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"} {
		t.Setenv(key, "")
	}
	a := &OpenCodeAdapter{managedConfig: []string{}, settings: fixedOpenCodeSettings(config.OpenCodeConfig{})}
	model := RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}
	const phrase = "withheld from this stage and every tool it runs: "
	dispatch := func() []string {
		t.Helper()
		var err error
		stderr := captureAdapterStderr(t, func() { err = a.PreDispatch(context.Background(), model) })
		if err != nil {
			t.Fatalf("PreDispatch refused: %v", err)
		}
		if strings.Contains(stderr, "sentinel-1616") {
			t.Errorf("a variable's value was written to stderr:\n%s", stderr)
		}
		var lines []string
		for _, line := range strings.Split(stderr, "\n") {
			if strings.Contains(line, phrase) {
				lines = append(lines, line)
			}
		}
		return lines
	}

	if lines := dispatch(); len(lines) != 0 {
		t.Errorf("with nothing withheld the dispatch printed %q", lines)
	}

	withheld := []string{"ANTHROPIC_BASE_URL", "GROQ_API_KEY", "OPENAI_API_KEY"}
	kept := []string{
		"AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "GITHUB_TOKEN",
		"LMSTUDIO_API_KEY", "OPENCODE_CONFIG",
	}
	for _, key := range slices.Concat(withheld, kept) {
		t.Setenv(key, "sentinel-1616-"+strings.ToLower(key))
	}
	lines := dispatch()
	if len(lines) != 1 {
		t.Fatalf("the dispatch printed %d lines naming withheld variables, want 1: %q", len(lines), lines)
	}
	line := lines[0]
	if !strings.HasPrefix(line, "[opencode] ") || !strings.Contains(line, phrase+strings.Join(withheld, ", ")+".") {
		t.Errorf("the line does not name %q in order:\n%s", withheld, line)
	}
	for _, want := range []string{`"lmstudio"`, "provider base URL", "OpenCode's catalog binds", "fails without it", "login of its own", "cloud platform and forge credentials"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line does not say %q:\n%s", want, line)
		}
	}
	for _, key := range kept {
		if strings.Contains(line, key) {
			t.Errorf("the line names %s, which the stage keeps:\n%s", key, line)
		}
	}
}

// TestOpenCodeRedactedEnv: the values removed from a child's captured output
// are the secrets it is allowed to hold (ADR-022 § 22): the server password,
// the forge tokens, and every variable the catalog binds to the dispatched
// provider, whichever provider that is, not only the providers an earlier
// list happened to name.
func TestOpenCodeRedactedEnv(t *testing.T) {
	always := []string{"OPENCODE_SERVER_PASSWORD", "GITHUB_TOKEN", "GH_TOKEN", "GITLAB_TOKEN"}
	a := NewOpenCodeAdapter()
	for model, own := range map[string][]string{
		"openai/gpt-5.5":                  {"OPENAI_API_KEY"},
		"deepseek/deepseek-chat":          {"DEEPSEEK_API_KEY"},
		"groq/llama-4-scout":              {"GROQ_API_KEY"},
		"google/gemini-2.5-pro":           {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
		"amazon-bedrock/anthropic.claude": {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_BEARER_TOKEN_BEDROCK"},
		"lmstudio/qwen/qwen3.8-27b":       {"LMSTUDIO_API_KEY"},
		"ollama/qwen3-coder:30b":          nil,
	} {
		got := a.RedactedEnv(RunOptions{Model: model})
		for _, name := range slices.Concat(always, own) {
			if !slices.Contains(got, name) {
				t.Errorf("model %q: %s is not redacted from the captured output (redacted: %q)", model, name, got)
			}
		}
	}
}

// TestOpenCodeCatalogEnvSnapshot guards the catalog snapshot the credential
// policy stands on against an edit by hand. It is the bundled catalog of
// opencode 1.18.30 as read from the binary (TestOpenCodeCatalogEnvMatchesTheBinary
// re-reads it): 213 provider keys, with the bindings ADR-022 § 8 and the forge
// exception name.
func TestOpenCodeCatalogEnvSnapshot(t *testing.T) {
	if n := len(openCodeCatalogEnv); n != 213 {
		t.Errorf("the catalog snapshot holds %d provider keys; opencode 1.18.30 bundles 213", n)
	}
	for provider, want := range map[string][]string{
		"anthropic":      {"ANTHROPIC_API_KEY"},
		"openai":         {"OPENAI_API_KEY"},
		"xai":            {"XAI_API_KEY"},
		"google":         {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
		"openrouter":     {"OPENROUTER_API_KEY"},
		"lmstudio":       {"LMSTUDIO_API_KEY"},
		"github-copilot": {"GITHUB_TOKEN"},
		"gitlab":         {"GITLAB_TOKEN"},
		"amazon-bedrock": {"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_REGION", "AWS_BEARER_TOKEN_BEDROCK"},
	} {
		if got := openCodeCatalogEnv[provider]; !slices.Equal(got, want) {
			t.Errorf("catalog[%q] = %q, want %q", provider, got, want)
		}
	}
	for _, notInCatalog := range []string{"ollama", "lm-studio", "lmstudio-remote"} {
		if _, ok := openCodeCatalogEnv[notInCatalog]; ok {
			t.Errorf("%q is in the catalog snapshot; 1.18.30 bundles no such key", notInCatalog)
		}
	}
	envName := regexp.MustCompile(`^[A-Z0-9][A-Z0-9_]*$`)
	for provider, vars := range openCodeCatalogEnv {
		if len(vars) == 0 {
			t.Errorf("catalog[%q] binds no variable", provider)
		}
		for _, v := range vars {
			if !envName.MatchString(v) {
				t.Errorf("catalog[%q] binds %q, which is not an environment variable name", provider, v)
			}
		}
	}
}

// TestOpenCodeRunRootNamesOnlyARunIdentity: a root is named by a run
// identity and nothing else, so an id can never climb out of the runs
// directory. ../x is refused by every function that takes an id, and nothing
// outside the runs directory is created or deleted.
func TestOpenCodeRunRootNamesOnlyARunIdentity(t *testing.T) {
	home := t.TempDir()
	sibling := filepath.Join(home, ".nightgauge", "opencode", "x")
	if err := os.MkdirAll(filepath.Join(sibling, "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"../x", "..", "", "x", "a/b", "/abs",
		"01890A5D-AC96-774B-BCCE-B302099A8057", // upper case
		"01890a5d-ac96-474b-bcce-b302099a8057", // a UUIDv4
		testRunID + "/..",
	} {
		if _, err := OpenCodeRunRoot(home, id); err == nil {
			t.Errorf("OpenCodeRunRoot(%q) succeeded", id)
		}
		if _, _, err := EnsureOpenCodeRunRoot(home, id, envLookup(nil)); err == nil {
			t.Errorf("EnsureOpenCodeRunRoot(%q) succeeded", id)
		}
		if err := RemoveOpenCodeRunRoot(home, id); err == nil {
			t.Errorf("RemoveOpenCodeRunRoot(%q) succeeded", id)
		}
	}
	if _, err := os.Stat(filepath.Join(sibling, "keep")); err != nil {
		t.Errorf("a directory beside the runs directory was touched: %v", err)
	}
	if _, err := OpenCodeRunRoot("relative/home", testRunID); err == nil {
		t.Error("OpenCodeRunRoot accepted a relative home directory")
	}
	got, err := OpenCodeRunRoot(home, testRunID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".nightgauge", "opencode", "runs", testRunID); got != want {
		t.Errorf("OpenCodeRunRoot = %q, want %q", got, want)
	}
}

// TestEnsureOpenCodeRunRoot: the root and its four XDG directories are 0700
// directories, the data directory starts empty, a second stage of the run
// reuses the root and refreshes its age, a mode someone loosened is reset,
// and a symbolic link where a directory belongs is refused before anything is
// written through it.
func TestEnsureOpenCodeRunRoot(t *testing.T) {
	home := t.TempDir()
	root, created, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Error("the first call did not report creating the root")
	}
	if want := filepath.Join(OpenCodeRunsDir(home), testRunID); root != want {
		t.Errorf("root = %q, want %q", root, want)
	}
	for _, dir := range []string{"", "config", "data", "cache", "state"} {
		fi, err := os.Lstat(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s/%s: mode %v, want a 0700 directory", root, dir, fi.Mode())
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "data")); len(entries) != 0 {
		t.Errorf("the data directory starts with %d entries; it must start empty", len(entries))
	}

	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "state"), 0o755); err != nil {
		t.Fatal(err)
	}
	again, created, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil))
	if err != nil || again != root || created {
		t.Fatalf("second call = %q, created=%v, %v; want the same root, reused", again, created, err)
	}
	if fi, _ := os.Stat(root); !fi.ModTime().After(old.Add(time.Hour)) {
		t.Errorf("reusing the root did not refresh its modification time (%v)", fi.ModTime())
	}
	if fi, _ := os.Stat(filepath.Join(root, "state")); fi.Mode().Perm() != 0o700 {
		t.Errorf("state/ kept mode %v; want it reset to 0700", fi.Mode().Perm())
	}

	elsewhere := t.TempDir()
	const planted = "01890a5d-ac96-774b-bcce-b302099a8058"
	if err := os.Symlink(elsewhere, filepath.Join(OpenCodeRunsDir(home), planted)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureOpenCodeRunRoot(home, planted, envLookup(nil)); err == nil {
		t.Error("a root that is a symbolic link was accepted")
	}
	if entries, _ := os.ReadDir(elsewhere); len(entries) != 0 {
		t.Errorf("EnsureOpenCodeRunRoot wrote %d entries through a symbolic link", len(entries))
	}

	if err := os.RemoveAll(filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(root, "data")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil)); err == nil {
		t.Error("a data/ that is a symbolic link was accepted")
	}
}

// TestOpenCodeRunStartsWithNoStoredLogin: the other half of ADR-022 § 17's
// condition for lifting the anthropic refusal. A run's data directory starts
// empty, so the operator's own auth.json is never copied in, and a dispatch
// whose run root holds one, however it got there, is refused without its
// content being read.
func TestOpenCodeRunStartsWithNoStoredLogin(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	const sentinel = "fake-stored-login-sentinel-1616"
	operatorAuth := filepath.Join(home, ".local", "share", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(operatorAuth), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(operatorAuth, []byte(`{"anthropic":{"type":"oauth","refresh":"`+sentinel+`"}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	a := &OpenCodeAdapter{settings: fixedOpenCodeSettings(lmStudioSettings())}
	req := RunRootRequest{
		ID:               testRunID,
		MachineConfigDir: filepath.Join(home, ".nightgauge"),
		Run:              RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()},
	}
	run, err := a.PrepareRunRoot(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = filepath.WalkDir(run.Dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "auth.json" {
			t.Errorf("a stored-login file exists in the run root: %s", path)
		}
		return nil
	})
	if entries, _ := os.ReadDir(filepath.Join(run.Dir, "data")); len(entries) != 0 {
		t.Errorf("the run's data directory starts with %d entries", len(entries))
	}

	stored := filepath.Join(run.Dir, "data", "opencode", "auth.json")
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = a.PrepareRunRoot(req)
	if err == nil {
		t.Fatal("a run root holding auth.json was prepared for the next stage")
	}
	if !strings.Contains(err.Error(), "stored logins") || strings.Contains(err.Error(), sentinel) {
		t.Errorf("refusal = %v; want it to name the stored logins and never their content", err)
	}
}

// TestRemoveOpenCodeRunRootNeverFollowsALink: deleting a root unlinks the
// symbolic links inside it (config/git points at the operator's git config)
// and never deletes what they point at; a root that is itself a link is
// refused and left alone; a root that is already gone is not an error.
func TestRemoveOpenCodeRunRootNeverFollowsALink(t *testing.T) {
	home := t.TempDir()
	operatorGit := filepath.Join(home, ".config", "git")
	if err := os.MkdirAll(operatorGit, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operatorGit, "config"), []byte("[user]\n\tname = operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "precious"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(root, "config", "git")); err != nil || target != operatorGit {
		t.Fatalf("config/git = %q (%v); want a link to the operator's git config directory", target, err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "data", "escape")); err != nil {
		t.Fatal(err)
	}

	if err := RemoveOpenCodeRunRoot(home, testRunID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Errorf("the root survives its deletion: %v", err)
	}
	for _, kept := range []string{filepath.Join(operatorGit, "config"), filepath.Join(outside, "precious")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("deleting the root deleted %s, which a link inside it pointed at: %v", kept, err)
		}
	}
	if err := RemoveOpenCodeRunRoot(home, testRunID); err != nil {
		t.Errorf("deleting a root that is already gone = %v; want nil", err)
	}

	linked := filepath.Join(OpenCodeRunsDir(home), testRunID)
	if err := os.Symlink(outside, linked); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOpenCodeRunRoot(home, testRunID); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("deleting a root that is a symbolic link = %v; want a refusal", err)
	}
	if _, err := os.Lstat(linked); err != nil {
		t.Errorf("the refused link was removed anyway: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Errorf("the refused link's target lost its content: %v", err)
	}
}

// TestSweepOpenCodeRunRoots: the backstop for a crashed run deletes a root no
// stage has touched for longer than the limit, keeps a recent one, and leaves
// alone everything in the runs directory that is not a root Nightgauge made:
// a directory not named by a run identity, a symbolic link named like one
// (and its target), and a file.
func TestSweepOpenCodeRunRoots(t *testing.T) {
	home := t.TempDir()
	now := time.Now()
	runs := OpenCodeRunsDir(home)
	ids := map[string]time.Duration{
		"01890a5d-ac96-774b-bcce-000000000001": 8 * 24 * time.Hour, // orphaned
		"01890a5d-ac96-774b-bcce-000000000002": 24 * time.Hour,     // a paused run's
	}
	for id, age := range ids {
		root, _, err := EnsureOpenCodeRunRoot(home, id, envLookup(nil))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "data", "opencode.db"), []byte("transcript"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(root, now.Add(-age), now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}
	strangers := []string{"not-a-run", "01890a5d-ac96-774b-bcce-000000000003", "01890a5d-ac96-774b-bcce-000000000004"}
	if err := os.Mkdir(filepath.Join(runs, strangers[0]), 0o700); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "precious"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(runs, strangers[1])); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runs, strangers[2]), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ancient := now.Add(-30 * 24 * time.Hour)
	for _, s := range strangers {
		_ = os.Chtimes(filepath.Join(runs, s), ancient, ancient)
	}

	removed, err := SweepOpenCodeRunRoots(home, OpenCodeOrphanMaxAge, now)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"01890a5d-ac96-774b-bcce-000000000001"}; !slices.Equal(removed, want) {
		t.Errorf("swept %q, want %q", removed, want)
	}
	if _, err := os.Lstat(filepath.Join(runs, "01890a5d-ac96-774b-bcce-000000000001")); !os.IsNotExist(err) {
		t.Error("the orphaned root survived the sweep")
	}
	if _, err := os.Lstat(filepath.Join(runs, "01890a5d-ac96-774b-bcce-000000000002")); err != nil {
		t.Errorf("a root younger than %s was swept: %v", OpenCodeOrphanMaxAge, err)
	}
	for _, s := range strangers {
		if _, err := os.Lstat(filepath.Join(runs, s)); err != nil {
			t.Errorf("the sweep removed %s, which is not a run root: %v", s, err)
		}
	}
	if _, err := os.Stat(filepath.Join(target, "precious")); err != nil {
		t.Errorf("the sweep deleted a link's target: %v", err)
	}

	if removed, err := SweepOpenCodeRunRoots(t.TempDir(), OpenCodeOrphanMaxAge, now); err != nil || len(removed) != 0 {
		t.Errorf("sweeping a home with no runs directory = %q, %v; want nothing and no error", removed, err)
	}
}

// TestOpenCodeIsolationKeepsTheOperatorsGitAndGh: moving XDG_CONFIG_HOME
// would also move git's XDG config and gh's hosts and auth. For every layout
// an operator's git config can take, `git config --global --list` and the
// XDG ignore file behave the same inside the run's environment as outside
// it, and gh reads the same config and token. Each layout builds a fake home
// and runs the real git and gh with an environment written out in full, so
// nothing from the machine running the test takes part.
func TestOpenCodeIsolationKeepsTheOperatorsGitAndGh(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	ghBin, _ := exec.LookPath("gh")

	const homeGitconfig = "[user]\n\tname = from-home-gitconfig\n[alias]\n\thomealias = status\n"
	const xdgGitconfig = "[user]\n\temail = from-xdg@example.invalid\n[alias]\n\txdgalias = log\n"
	for _, tc := range []struct {
		name       string
		home, xdg  bool   // which files exist
		xdgBase    string // the operator's XDG_CONFIG_HOME, relative to HOME; "" is unset
		gitGlobal  bool   // the operator sets GIT_CONFIG_GLOBAL
		ghDirIsEnv bool   // the operator sets GH_CONFIG_DIR
	}{
		{name: "only ~/.gitconfig", home: true},
		{name: "only the XDG file", xdg: true},
		{name: "both", home: true, xdg: true},
		{name: "both, XDG_CONFIG_HOME set", home: true, xdg: true, xdgBase: "xdg-elsewhere"},
		{name: "GIT_CONFIG_GLOBAL and GH_CONFIG_DIR set", home: true, xdg: true, gitGlobal: true, ghDirIsEnv: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			outside := map[string]string{
				"HOME": home, "PATH": filepath.Dir(gitBin) + string(os.PathListSeparator) + "/usr/bin:/bin",
				"GIT_CONFIG_NOSYSTEM": "1", "GH_NO_UPDATE_NOTIFIER": "1", "GH_PROMPT_DISABLED": "1",
			}
			if ghBin != "" {
				outside["PATH"] = filepath.Dir(ghBin) + string(os.PathListSeparator) + outside["PATH"]
			}
			configHome := filepath.Join(home, ".config")
			if tc.xdgBase != "" {
				configHome = filepath.Join(home, tc.xdgBase)
				outside["XDG_CONFIG_HOME"] = configHome
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.home {
				write(filepath.Join(home, ".gitconfig"), homeGitconfig)
			}
			if tc.xdg {
				write(filepath.Join(configHome, "git", "config"), xdgGitconfig)
				write(filepath.Join(configHome, "git", "ignore"), "*.xdgignored\n")
			}
			if tc.gitGlobal {
				write(filepath.Join(home, "custom.gitconfig"), "[user]\n\tname = from-git-config-global\n")
				outside["GIT_CONFIG_GLOBAL"] = filepath.Join(home, "custom.gitconfig")
			}
			ghDir := filepath.Join(configHome, "gh")
			if tc.ghDirIsEnv {
				ghDir = filepath.Join(home, "gh-elsewhere")
				outside["GH_CONFIG_DIR"] = ghDir
			}
			write(filepath.Join(ghDir, "config.yml"), "editor: fixture-editor\n")
			write(filepath.Join(ghDir, "hosts.yml"), "github.com:\n    oauth_token: fixture-token-not-real\n    user: fixture-user\n    git_protocol: https\n")

			root, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(outside))
			if err != nil {
				t.Fatal(err)
			}
			iso, err := OpenCodeIsolationEnv(OpenCodeIsolation{
				Root: root, Home: home, Lookup: envLookup(outside), GOOS: "linux", MachineConfigDir: filepath.Join(home, ".nightgauge"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if iso["GH_CONFIG_DIR"] != ghDir {
				t.Errorf("GH_CONFIG_DIR = %q, want the operator's gh directory %q", iso["GH_CONFIG_DIR"], ghDir)
			}
			inside := maps.Clone(outside)
			maps.Copy(inside, iso)
			if !strings.HasPrefix(inside["XDG_CONFIG_HOME"], root) {
				t.Fatalf("XDG_CONFIG_HOME = %q is not in the run root; the test would compare nothing", inside["XDG_CONFIG_HOME"])
			}

			repo := t.TempDir()
			run := func(env map[string]string, name string, args ...string) string {
				t.Helper()
				cmd := exec.Command(name, args...)
				cmd.Dir = repo
				for k, v := range env {
					cmd.Env = append(cmd.Env, k+"="+v)
				}
				out, err := cmd.CombinedOutput()
				code := 0
				if exitErr, ok := err.(*exec.ExitError); ok {
					code = exitErr.ExitCode()
				} else if err != nil {
					t.Fatalf("%s %q: %v", name, args, err)
				}
				return string(out) + "\nexit " + strconv.Itoa(code)
			}
			if out := run(outside, gitBin, "init", "-q"); !strings.HasSuffix(out, "exit 0") {
				t.Fatalf("git init: %s", out)
			}

			for _, args := range [][]string{
				{"config", "--global", "--list"},
				{"check-ignore", "-q", "file.xdgignored"},
			} {
				want, got := run(outside, gitBin, args...), run(inside, gitBin, args...)
				if got != want {
					t.Errorf("git %s inside the run =\n%s\noutside =\n%s", strings.Join(args, " "), got, want)
				}
			}
			if ghBin == "" {
				t.Log("gh is not on PATH; the gh half is covered by the GH_CONFIG_DIR assertion only")
				return
			}
			for _, args := range [][]string{
				{"auth", "token", "--hostname", "github.com"},
				{"config", "get", "editor"},
			} {
				want, got := run(outside, ghBin, args...), run(inside, ghBin, args...)
				if got != want {
					t.Errorf("gh %s inside the run =\n%s\noutside =\n%s", strings.Join(args, " "), got, want)
				}
				if !strings.Contains(want, "fixture-") {
					t.Errorf("gh %s outside the run did not read the fixture (%q); the comparison proves nothing", strings.Join(args, " "), want)
				}
			}
		})
	}
}

// TestOpenCodeRunRootLinksTheOperatorsXDGConfig: moving XDG_CONFIG_HOME
// moves it for every tool the stage starts, not only OpenCode, so a tool that
// keeps a security setting there, such as uv's or pip's private package index,
// would fall back to its public default without notice. config/ links every
// entry of the operator's XDG config directory but OpenCode's own, files and
// hidden entries included, and a tool reading $XDG_CONFIG_HOME/uv/uv.toml
// inside the run reads the operator's. A later stage links what the operator
// has added since and re-points a link whose directory moved; an entry the run
// made itself is left alone; and an entry spelled OpenCode is never linked,
// because on a case-insensitive filesystem it is the run's opencode/.
func TestOpenCodeRunRootLinksTheOperatorsXDGConfig(t *testing.T) {
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	home := t.TempDir()
	operator := filepath.Join(home, ".config")
	const index = "index-url = \"https://pypi.example.invalid/simple\"\n"
	write(filepath.Join(operator, "uv", "uv.toml"), index)
	write(filepath.Join(operator, "pip", "pip.conf"), "[global]\nindex-url = https://pypi.example.invalid/simple\n")
	write(filepath.Join(operator, "git", "config"), "[user]\n\tname = operator\n")
	write(filepath.Join(operator, "starship.toml"), "format = \"$all\"\n")
	write(filepath.Join(operator, ".bunfig.toml"), "[install]\n")
	write(filepath.Join(operator, "opencode", "opencode.json"), `{"username":"operator"}`)

	root, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	linked := func(name, want string) {
		t.Helper()
		if got, err := os.Readlink(filepath.Join(root, "config", name)); err != nil || got != want {
			t.Errorf("config/%s = %q (%v); want a link to %s", name, got, err, want)
		}
	}
	for _, name := range []string{"uv", "pip", "git", "starship.toml", ".bunfig.toml"} {
		linked(name, filepath.Join(operator, name))
	}
	if _, err := os.Lstat(filepath.Join(root, "config", "opencode")); !os.IsNotExist(err) {
		t.Errorf("config/opencode exists (%v); OpenCode's own config directory must stay the run's", err)
	}

	cat := exec.Command("sh", "-c", `cat "$XDG_CONFIG_HOME/uv/uv.toml"`)
	cat.Env = []string{"PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + filepath.Join(root, "config")}
	if out, err := cat.Output(); err != nil || string(out) != index {
		t.Errorf("a tool reading $XDG_CONFIG_HOME/uv/uv.toml in the run read %q (%v); want the operator's %q", out, err, index)
	}

	// The run made its own entry while the operator had none; the operator
	// then made one, and added another.
	write(filepath.Join(root, "config", "pnpm", "rc"), "the run's own\n")
	write(filepath.Join(operator, "pnpm", "rc"), "the operator's\n")
	write(filepath.Join(operator, "containers", "registries.conf"), "unqualified-search-registries = []\n")
	if _, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(nil)); err != nil {
		t.Fatalf("the next stage refused a root holding an entry the run made itself: %v", err)
	}
	linked("containers", filepath.Join(operator, "containers"))
	if b, err := os.ReadFile(filepath.Join(root, "config", "pnpm", "rc")); err != nil || string(b) != "the run's own\n" {
		t.Errorf("the run's own config/pnpm was replaced: %q, %v", b, err)
	}

	// The operator's XDG config directory moved.
	moved := filepath.Join(home, "xdg-elsewhere")
	write(filepath.Join(moved, "uv", "uv.toml"), index)
	if _, _, err := EnsureOpenCodeRunRoot(home, testRunID, envLookup(map[string]string{"XDG_CONFIG_HOME": moved})); err != nil {
		t.Fatal(err)
	}
	linked("uv", filepath.Join(moved, "uv"))

	spelled := t.TempDir()
	write(filepath.Join(spelled, ".config", "OpenCode", "opencode.json"), `{"username":"operator"}`)
	spelledRoot, _, err := EnsureOpenCodeRunRoot(spelled, testRunID, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(spelledRoot, "config")); len(entries) != 0 {
		t.Errorf("an operator entry spelled OpenCode was linked into config/: %v", entries)
	}
}

// TestOpenCodeManagedConfigFiles: the machine-wide managed config opencode
// 1.18.30 reads, per platform, from its bundled source.
func TestOpenCodeManagedConfigFiles(t *testing.T) {
	for goos, want := range map[string][]string{
		"linux": {"/etc/opencode/opencode.json", "/etc/opencode/opencode.jsonc"},
		"darwin": {
			"/Library/Application Support/opencode/opencode.json",
			"/Library/Application Support/opencode/opencode.jsonc",
			"/Library/Managed Preferences/op/ai.opencode.managed.plist",
			"/Library/Managed Preferences/ai.opencode.managed.plist",
		},
	} {
		if got := openCodeManagedConfigFiles(goos, "op"); !slices.Equal(got, want) {
			t.Errorf("%s: managed config files = %q, want %q", goos, got, want)
		}
	}
}

// TestOpenCodeRefusesManagedOpenCodeConfig: observed on 1.18.30, the
// machine's managed config merges above OPENCODE_CONFIG_CONTENT, the layer
// Nightgauge's locked keys go in, and no XDG or HOME variable moves it
// (ADR-022 § 8). A dispatch is refused before spawn while any of its files
// exists, naming the file without reading it, unless the operator has opted
// into their own OpenCode config, which the stderr line then says includes it.
// The refusal is PrepareRunRoot's, the preparation `nightgauge opencode
// config` shares, and it follows the block the run is built from.
func TestOpenCodeRefusesManagedOpenCodeConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.opencode
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	const sentinel = "managed-config-content-sentinel-1616"
	dir := t.TempDir()
	files := []string{
		filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc"),
		filepath.Join(dir, "user", "ai.opencode.managed.plist"), filepath.Join(dir, "ai.opencode.managed.plist"),
	}
	a := &OpenCodeAdapter{managedConfig: files, settings: fixedOpenCodeSettings(lmStudioSettings())}
	req := RunRootRequest{ID: testRunID, MachineConfigDir: t.TempDir(), Run: RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()}}
	if _, err := a.PrepareRunRoot(req); err != nil {
		t.Fatalf("with no managed config the dispatch was refused: %v", err)
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := a.PrepareRunRoot(req)
		if err == nil {
			t.Errorf("%s: the dispatch was allowed", f)
		} else {
			for _, want := range []string{f, "managed OpenCode config", openCodeInheritSetting + ": true", "machine-tier config"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%s: the refusal does not say %q: %v", f, want, err)
				}
			}
			if strings.Contains(err.Error(), sentinel) {
				t.Errorf("%s: the refusal carries the file's content", f)
			}
		}
		if err := os.Remove(f); err != nil {
			t.Fatal(err)
		}
	}

	if err := os.WriteFile(files[0], []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}
	inherit := lmStudioSettings()
	inherit.InheritUserConfig = true
	a.settings = fixedOpenCodeSettings(inherit)
	var err error
	stderr := captureAdapterStderr(t, func() { _, err = a.PrepareRunRoot(req) })
	if err != nil {
		t.Errorf("with %s on the dispatch was refused: %v", openCodeInheritSetting, err)
	}
	if !strings.Contains(stderr, "managed OpenCode config") {
		t.Errorf("the opt-in line does not say it takes in the machine's managed config:\n%s", stderr)
	}
}

// TestOpenCodeIsolationRefusalFollowsTheBlockTheRunIsBuiltFrom: the
// ~/.opencode and managed-config refusals and the environment that decides
// whether the operator's config is layered in come from one read of the
// machine-tier block. With a block that reads opted in first and opted out
// after, a dispatch is never let through with ~/.opencode holding config and
// the run built as opted out, which would load that config with neither the
// refusal nor the stderr line.
func TestOpenCodeIsolationRefusalFollowsTheBlockTheRunIsBuiltFrom(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	if err := os.MkdirAll(filepath.Join(home, ".opencode", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	reads := 0
	a := &OpenCodeAdapter{managedConfig: []string{}, settings: func(string) (config.OpenCodeConfig, error) {
		reads++
		s := lmStudioSettings()
		s.InheritUserConfig = reads == 1
		return s, nil
	}}
	run := RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()}
	var root *RunRoot
	var err error
	stderr := captureAdapterStderr(t, func() {
		if err = a.PreDispatch(context.Background(), run); err == nil {
			root, err = a.PrepareRunRoot(RunRootRequest{ID: testRunID, MachineConfigDir: t.TempDir(), Run: run})
		}
	})
	switch {
	case err != nil:
		if !strings.Contains(err.Error(), filepath.Join(home, ".opencode")) {
			t.Errorf("the dispatch was refused for another reason: %v", err)
		}
	case root.Env["OPENCODE_CONFIG_DIR"] == "":
		t.Error("the dispatch went ahead with ~/.opencode holding config and the run built as opted out: that config loads with neither the refusal nor the opt-in")
	case !strings.Contains(stderr, openCodeInheritSetting+" is on"):
		t.Errorf("the run layers the operator's config in but stderr does not say so:\n%s", stderr)
	}
}

// captureAdapterStderr runs fn with os.Stderr redirected and returns what fn
// wrote to it.
func captureAdapterStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	out := make(chan []byte)
	go func() {
		b, _ := io.ReadAll(r)
		out <- b
	}()
	defer func() { os.Stderr = orig }()
	fn()
	_ = w.Close()
	return string(<-out)
}

// TestOpenCodeRefusesAHomeDotOpenCodeWithConfig: observed on 1.18.30,
// OpenCode reads $HOME/.opencode as a config directory whatever the XDG
// variables say, so the per-run root cannot keep it out (ADR-022 § 8). A
// dispatch is refused before spawn while it holds anything OpenCode loads
// from a config directory, naming the entries without reading them, and
// nothing is created. What an install or OpenCode itself leaves there is not
// config. With the operator's opt-in into their own OpenCode config,
// opencode.inherit_user_config in the machine tier, the dispatch goes ahead
// and stderr says so. The refusal is PrepareRunRoot's, which the verb shares;
// without the switch, PreDispatch's gate refuses first.
func TestOpenCodeRefusesAHomeDotOpenCodeWithConfig(t *testing.T) {
	// The name is what operators set and what ADR-022 § 8 documents.
	if openCodeInheritSetting != "opencode.inherit_user_config" {
		t.Fatalf("the opt-in setting is %q; ADR-022 § 8 names opencode.inherit_user_config", openCodeInheritSetting)
	}
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	a := &OpenCodeAdapter{managedConfig: []string{}, settings: fixedOpenCodeSettings(lmStudioSettings())}
	req := RunRootRequest{ID: testRunID, MachineConfigDir: t.TempDir(), Run: RunOptions{Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()}}
	t.Setenv(ExperimentalOpenCodeEnvVar, "1")
	const sentinel = "home-config-content-sentinel-1616"
	for _, entry := range []string{
		"opencode.json", "opencode.jsonc", "agent", "agents", "command", "commands",
		"mode", "modes", "plugin", "plugins", "tool", "tools", "skill", "skills",
	} {
		home := t.TempDir()
		t.Setenv("HOME", home)
		path := filepath.Join(home, ".opencode", entry)
		if strings.Contains(entry, ".") {
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(sentinel), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		_, err := a.PrepareRunRoot(req)
		if err == nil {
			t.Errorf("~/.opencode/%s: the dispatch was allowed", entry)
			continue
		}
		for _, want := range []string{filepath.Join(home, ".opencode"), entry, openCodeInheritSetting + ": true", filepath.Join("~", ".config", "opencode")} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("~/.opencode/%s: the refusal does not say %q: %v", entry, want, err)
			}
		}
		if strings.Contains(err.Error(), sentinel) {
			t.Errorf("~/.opencode/%s: the refusal carries the file's content", entry)
		}
		if _, err := os.Lstat(OpenCodeRunsDir(home)); !os.IsNotExist(err) {
			t.Errorf("~/.opencode/%s: the refused dispatch created %s", entry, OpenCodeRunsDir(home))
		}
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, notConfig := range []string{"bin", "node_modules"} {
		if err := os.MkdirAll(filepath.Join(home, ".opencode", notConfig), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, notConfig := range []string{"package.json", ".gitignore", "bun.lock"} {
		if err := os.WriteFile(filepath.Join(home, ".opencode", notConfig), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.PrepareRunRoot(req); err != nil {
		t.Errorf("a ~/.opencode holding only bin/ and install files was refused: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(home, ".opencode", "agent"), 0o700); err != nil {
		t.Fatal(err)
	}
	inherit := lmStudioSettings()
	inherit.InheritUserConfig = true
	a.settings = fixedOpenCodeSettings(inherit)
	var err error
	stderr := captureAdapterStderr(t, func() { _, err = a.PrepareRunRoot(req) })
	if err != nil {
		t.Errorf("with %s on the dispatch was refused: %v", openCodeInheritSetting, err)
	}
	if n := strings.Count(stderr, openCodeInheritSetting+" is on: this dispatch also reads your own OpenCode config"); n != 1 {
		t.Errorf("the opt-in was announced %d times on stderr, want once:\n%s", n, stderr)
	}

	a.settings = fixedOpenCodeSettings(lmStudioSettings())
	t.Setenv(ExperimentalOpenCodeEnvVar, "")
	if err := a.PreDispatch(context.Background(), RunOptions{Model: "lmstudio/qwen/qwen3.8-27b"}); err == nil || !strings.Contains(err.Error(), "is experimental") {
		t.Errorf("with the switch unset the refusal = %v; want the gate's", err)
	}
}

// TestOpenCodePrepareRunRoot drives the hook the manager calls: the root is
// created under the home directory, its environment points OpenCode at it and
// carries the run's config, a second stage of the run reuses it, and creating
// a root sweeps an orphan a crashed run left behind.
func TestOpenCodePrepareRunRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_CACHE_HOME", "GH_CONFIG_DIR", "GOCACHE"} {
		t.Setenv(k, "")
	}
	const orphan = "01890a5d-ac96-774b-bcce-0000000000ff"
	orphanRoot, _, err := EnsureOpenCodeRunRoot(home, orphan, envLookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-OpenCodeOrphanMaxAge - time.Hour)
	if err := os.Chtimes(orphanRoot, stale, stale); err != nil {
		t.Fatal(err)
	}

	a := &OpenCodeAdapter{settings: fixedOpenCodeSettings(lmStudioSettings())}
	req := RunRootRequest{
		ID:               testRunID,
		MachineConfigDir: filepath.Join(home, ".nightgauge"),
		Run:              RunOptions{Stage: "feature-dev", Model: "lmstudio/qwen/qwen3.8-27b", WorktreeDir: t.TempDir()},
	}
	var first *RunRoot
	stderr := captureAdapterStderr(t, func() { first, err = a.PrepareRunRoot(req) })
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".nightgauge", "opencode", "runs", testRunID); first.Dir != want {
		t.Errorf("Dir = %q, want %q", first.Dir, want)
	}
	if first.Env["XDG_DATA_HOME"] != filepath.Join(first.Dir, "data") || first.Env["NIGHTGAUGE_CONFIG_HOME"] != req.MachineConfigDir {
		t.Errorf("Env does not point OpenCode at the root: %v", first.Env)
	}
	if content := first.Env["OPENCODE_CONFIG_CONTENT"]; !strings.Contains(content, `"enabled_providers":["lmstudio"]`) {
		t.Errorf("Env does not carry the run's config as OPENCODE_CONFIG_CONTENT: %q", content)
	}
	if _, err := os.Lstat(orphanRoot); !os.IsNotExist(err) {
		t.Error("creating a root did not sweep the orphaned one")
	}
	if !strings.Contains(stderr, orphan) {
		t.Errorf("the sweep did not say what it deleted:\n%s", stderr)
	}
	second, err := a.PrepareRunRoot(req)
	if err != nil || second.Dir != first.Dir {
		t.Errorf("the next stage got %v, %v; want the same root", second, err)
	}
	if !runstate.IsIdentity(filepath.Base(first.Dir)) {
		t.Errorf("the root is not named by a run identity: %s", first.Dir)
	}
}
