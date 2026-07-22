package adapters

import (
	"strings"
	"testing"
)

func TestClaudeAdapter(t *testing.T) {
	adapter := NewClaudeAdapter()
	if adapter.Name() != "claude" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("Claude adapter should use stdin")
	}

	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:    "/skills/feature-dev/SKILL.md",
		WorktreeDir:  "/tmp/worktree",
		IssueNumber:  1311,
		Repo:         "nightgauge/nightgauge",
		Stage:        "feature-dev",
		Model:        "claude-sonnet-4-6",
		MaxTokens:    50000,
		AllowedTools: []string{"Read", "Edit", "Bash"},
		TargetRepo:   "nightgauge/nightgauge",
		CostBudget:   5.00,
	})

	if cmd != "claude" {
		t.Errorf("cmd = %q", cmd)
	}
	if !containsArg(args, "-p") {
		t.Error("missing -p flag")
	}
	if !containsArg(args, "--no-session-persistence") {
		t.Error("missing --no-session-persistence flag")
	}
	if !containsArg(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	if !containsArg(args, "--verbose") {
		t.Error("missing --verbose flag")
	}
	if !containsArg(args, "--allowedTools") {
		t.Error("missing --allowedTools flag")
	}
	if !containsArg(args, "--model") {
		t.Error("missing --model flag")
	}
	if !containsArg(args, "--max-budget-usd") {
		t.Error("missing --max-budget-usd flag")
	}
	// Should NOT have --prompt-file (uses stdin instead)
	if containsArg(args, "--prompt-file") {
		t.Error("should not have --prompt-file (uses stdin)")
	}
	if env["NIGHTGAUGE_STAGE"] != "feature-dev" {
		t.Errorf("stage env = %q", env["NIGHTGAUGE_STAGE"])
	}
	if env["NIGHTGAUGE_ADAPTER"] != "claude" {
		t.Errorf("adapter env = %q", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["NIGHTGAUGE_OUTPUT_FORMAT"] != "stream-json" {
		t.Errorf("output format env = %q", env["NIGHTGAUGE_OUTPUT_FORMAT"])
	}
	if env["NIGHTGAUGE_TARGET_REPO"] != "nightgauge/nightgauge" {
		t.Errorf("target repo env = %q", env["NIGHTGAUGE_TARGET_REPO"])
	}
	// Thinking must NOT be force-disabled (#73): the #3801 replay-400
	// workaround was retired after it stopped reproducing on CLI 2.1.186.
	// The spawn env is layered onto os.Environ(), so an operator can still
	// restore it from their shell without a rebuild.
	if v, ok := env["CLAUDE_CODE_DISABLE_THINKING"]; ok {
		t.Errorf("CLAUDE_CODE_DISABLE_THINKING forced to %q; the adapter must not set it", v)
	}
}

func TestCodexAdapter(t *testing.T) {
	adapter := NewCodexAdapter()
	if adapter.Name() != "codex" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("Codex should read the prompt via stdin (`-`), not --prompt-file")
	}

	cmd, args, env := adapter.BuildCommand(RunOptions{
		IssueNumber: 1311,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		Model:       "gpt-5.5",
	})

	if cmd != "codex" {
		t.Errorf("cmd = %q", cmd)
	}
	// Modern `codex exec` contract (#4019): exec subcommand, autonomous bypass
	// flag (replaces deprecated --full-auto), JSON stream, prompt via stdin `-`.
	for _, want := range []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--json", "-"} {
		if !containsArg(args, want) {
			t.Errorf("missing arg %q in %v", want, args)
		}
	}
	if args[0] != "exec" {
		t.Errorf("first arg = %q, want exec", args[0])
	}
	if args[len(args)-1] != "-" {
		t.Errorf("last arg = %q, want stdin marker -", args[len(args)-1])
	}
	// Legacy/invalid flags must not appear — they do not exist in current Codex.
	for _, bad := range []string{"--quiet", "--approval-mode", "--full-auto", "--prompt-file"} {
		if containsArg(args, bad) {
			t.Errorf("invalid arg %q should not be present in %v", bad, args)
		}
	}
	if !containsArg(args, "--model") || !containsArg(args, "gpt-5.5") {
		t.Errorf("expected --model gpt-5.5 in %v", args)
	}
	if env["NIGHTGAUGE_ADAPTER"] != "codex" {
		t.Errorf("adapter env = %q", env["NIGHTGAUGE_ADAPTER"])
	}
}

func TestResolveCodexModel(t *testing.T) {
	// Mirrors the canonical SDK codexModelRegistry tier→model map (#4018,#4019).
	cases := map[string]string{
		"haiku":             "gpt-5.4-mini",
		"sonnet":            "gpt-5.4",
		"opus":              "gpt-5.5",
		"fable":             "gpt-5.5",
		"claude-sonnet-4-6": "gpt-5.4", // scheduler escalation id
		"claude-opus-4-8":   "gpt-5.5",
		"claude-haiku-4-5":  "gpt-5.4-mini",
		"gpt-5.3-codex":     "gpt-5.5",      // deprecated → replacement
		"gpt-5.2":           "gpt-5.4",      // deprecated → replacement
		"gpt-5.5":           "gpt-5.5",      // concrete id passes through
		"gpt-5.4-mini":      "gpt-5.4-mini", // concrete id passes through
		"some-future-model": "some-future-model",
	}
	for in, want := range cases {
		if got := resolveCodexModel(in); got != want {
			t.Errorf("resolveCodexModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestValidateCodexModel mirrors the SDK validateModelForAdapter preflight
// (#4021): empty is allowed, tiers/deprecated ids resolve to a valid model, and
// an unknown model is rejected with a clear message.
func TestValidateCodexModel(t *testing.T) {
	valid := []string{
		"",                  // empty → skip, no error
		"haiku",             // tier → gpt-5.4-mini
		"sonnet",            // tier → gpt-5.4
		"opus",              // tier → gpt-5.5
		"fable",             // tier → gpt-5.5
		"claude-sonnet-4-6", // escalation id → gpt-5.4
		"gpt-5.3-codex",     // deprecated → gpt-5.5
		"gpt-5.2",           // deprecated → gpt-5.4
		"gpt-5.5",           // concrete valid id
		"gpt-5.4-mini",      // concrete valid id
	}
	for _, m := range valid {
		if err := ValidateCodexModel(m); err != nil {
			t.Errorf("ValidateCodexModel(%q) = %v, want nil", m, err)
		}
	}

	// Note: claude-* ids match by PREFIX in resolveCodexModel, so e.g.
	// "claude-sonnet-4-6-bad" still resolves to gpt-5.4 (valid) — only genuinely
	// unknown, non-tier, non-claude-prefixed ids are rejected.
	invalid := []string{"gpt-999", "gpt-5.4-typo", "o4-mini", "gpt-4o", "totally-made-up"}
	for _, m := range invalid {
		if err := ValidateCodexModel(m); err == nil {
			t.Errorf("ValidateCodexModel(%q) = nil, want error", m)
		}
	}
}

// TestCodexAdapterValidateModel asserts the adapter exposes the optional
// ValidateModel hook the execution manager checks before BuildCommand (#4021).
func TestCodexAdapterValidateModel(t *testing.T) {
	adapter := NewCodexAdapter()
	if err := adapter.ValidateModel("opus"); err != nil {
		t.Errorf("ValidateModel(opus) = %v, want nil", err)
	}
	if err := adapter.ValidateModel("not-a-real-model"); err == nil {
		t.Error("ValidateModel(not-a-real-model) = nil, want error")
	}
}

// TestResolveCodexSandboxMode mirrors the SDK codexSandbox mapping (#4026):
// tighten only with positive evidence; default to full access.
func TestResolveCodexSandboxMode(t *testing.T) {
	cases := []struct {
		name  string
		tools []string
		want  string
	}{
		{"nil → full access", nil, codexSandboxDangerFull},
		{"empty → full access", []string{}, codexSandboxDangerFull},
		{"bash → full access", []string{"Read", "Bash"}, codexSandboxDangerFull},
		{"scoped bash → full access", []string{"Read", "Bash(git *)"}, codexSandboxDangerFull},
		{"webfetch → full access", []string{"Read", "WebFetch"}, codexSandboxDangerFull},
		{"mcp → full access", []string{"Read", "mcp__pw__click"}, codexSandboxDangerFull},
		{"write-only → workspace-write", []string{"Read", "Edit"}, codexSandboxWorkspaceWrite},
		{"notebook-edit → workspace-write", []string{"NotebookEdit"}, codexSandboxWorkspaceWrite},
		{"read-only → read-only", []string{"Read", "Grep", "Glob"}, codexSandboxReadOnly},
	}
	for _, c := range cases {
		if got := resolveCodexSandboxMode(c.tools); got != c.want {
			t.Errorf("%s: resolveCodexSandboxMode(%v) = %q, want %q", c.name, c.tools, got, c.want)
		}
	}
}

// TestCodexAdapterSandboxFromAllowedTools asserts BuildCommand scopes the
// sandbox flags from the stage's allowed-tools (#4026).
func TestCodexAdapterSandboxFromAllowedTools(t *testing.T) {
	adapter := NewCodexAdapter()

	// Read-only tool set → scoped sandbox, no bypass flag.
	_, args, _ := adapter.BuildCommand(RunOptions{
		Stage:        "feature-validate",
		AllowedTools: []string{"Read", "Grep", "Glob"},
	})
	for _, want := range []string{"--sandbox", "read-only", "--ask-for-approval", "never"} {
		if !containsArg(args, want) {
			t.Errorf("read-only: missing %q in %v", want, args)
		}
	}
	if containsArg(args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("read-only: bypass flag should be absent in %v", args)
	}

	// Bash present → full access (bypass flag), no --sandbox.
	_, fullArgs, _ := adapter.BuildCommand(RunOptions{
		Stage:        "feature-dev",
		AllowedTools: []string{"Read", "Edit", "Bash"},
	})
	if !containsArg(fullArgs, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("bash: expected bypass flag in %v", fullArgs)
	}
	if containsArg(fullArgs, "--sandbox") {
		t.Errorf("bash: --sandbox should be absent in %v", fullArgs)
	}
}

// TestCodexAdapterResolvesTierModel asserts BuildCommand translates a Claude
// tier (what the scheduler actually supplies) to a concrete Codex id (#4019).
func TestCodexAdapterResolvesTierModel(t *testing.T) {
	adapter := NewCodexAdapter()
	_, args, _ := adapter.BuildCommand(RunOptions{Stage: "feature-dev", Model: "sonnet"})
	if !containsArg(args, "--model") || !containsArg(args, "gpt-5.4") {
		t.Errorf("expected tier 'sonnet' to resolve to --model gpt-5.4 in %v", args)
	}
	if containsArg(args, "sonnet") {
		t.Errorf("raw tier 'sonnet' must not reach codex --model in %v", args)
	}
}

func TestGeminiAdapter(t *testing.T) {
	adapter := NewGeminiAdapter()
	if adapter.Name() != "gemini" {
		t.Errorf("Name = %q", adapter.Name())
	}
	// #4032: the Gemini CLI takes the prompt positionally, not via stdin.
	if adapter.UsesStdin() {
		t.Error("UsesStdin = true, want false (positional prompt)")
	}

	t.Setenv("GEMINI_API_KEY", "test-key")

	const prompt = "Implement issue 1311 per the plan."
	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/feature-dev/SKILL.md",
		Prompt:      prompt,
		Model:       "gemini-2.5-pro",
		IssueNumber: 1311,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})

	if cmd != "gemini" {
		t.Errorf("cmd = %q", cmd)
	}
	// Prompt is the leading positional argument (#4032), mirroring the TS
	// GeminiAdapter — not --prompt-file (the CLI has no such flag), not stdin.
	if len(args) == 0 || args[0] != prompt {
		t.Errorf("expected prompt as the first positional arg, got args = %v", args)
	}
	// Structured NDJSON so the Gemini stream parser receives token usage.
	if !containsArg(args, "--output-format") || !containsArg(args, "stream-json") {
		t.Errorf("missing --output-format stream-json, args = %v", args)
	}
	// The broken pre-#4032 flags must be gone — the current CLI has neither.
	if containsArg(args, "--prompt-file") {
		t.Errorf("--prompt-file must not be emitted (CLI has no such flag), args = %v", args)
	}
	if containsArg(args, "--noinput") {
		t.Errorf("--noinput must not be emitted, args = %v", args)
	}
	if !containsArg(args, "--model") || !containsArg(args, "gemini-2.5-pro") {
		t.Errorf("missing --model gemini-2.5-pro, args = %v", args)
	}
	if env["GEMINI_API_KEY"] != "test-key" {
		t.Errorf("GEMINI_API_KEY not passed through")
	}
}

func TestContextFileEnvVars(t *testing.T) {
	adapter := NewClaudeAdapter()
	_, _, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/test/SKILL.md",
		IssueNumber: 42,
		Repo:        "org/repo",
		Stage:       "test",
		ContextFile: "/path/to/context.json",
		OutputFile:  "/path/to/output.json",
	})

	if env["NIGHTGAUGE_CONTEXT_FILE"] != "/path/to/context.json" {
		t.Errorf("context file = %q", env["NIGHTGAUGE_CONTEXT_FILE"])
	}
	if env["NIGHTGAUGE_OUTPUT_FILE"] != "/path/to/output.json" {
		t.Errorf("output file = %q", env["NIGHTGAUGE_OUTPUT_FILE"])
	}
}

func TestClaudeSdkAdapter(t *testing.T) {
	adapter := NewClaudeSdkAdapter()
	if adapter.Name() != "claude-sdk" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("Claude SDK adapter should use stdin")
	}

	t.Setenv("ANTHROPIC_API_KEY", "sk-test-key")

	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:    "/skills/feature-dev/SKILL.md",
		WorktreeDir:  "/tmp/worktree",
		IssueNumber:  1562,
		Repo:         "nightgauge/nightgauge",
		Stage:        "feature-dev",
		Model:        "claude-sonnet-4-6",
		AllowedTools: []string{"Read", "Edit"},
		CostBudget:   3.00,
	})

	if cmd != "claude" {
		t.Errorf("cmd = %q", cmd)
	}
	if !containsArg(args, "-p") {
		t.Error("missing -p flag")
	}
	if !containsArg(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	// SDK mode should NOT have --no-session-persistence
	if containsArg(args, "--no-session-persistence") {
		t.Error("SDK mode should not have --no-session-persistence")
	}
	if !containsArg(args, "--allowedTools") {
		t.Error("missing --allowedTools flag")
	}
	if !containsArg(args, "--max-budget-usd") {
		t.Error("missing --max-budget-usd flag")
	}
	if env["NIGHTGAUGE_ADAPTER"] != "claude-sdk" {
		t.Errorf("adapter env = %q", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["ANTHROPIC_API_KEY"] != "sk-test-key" {
		t.Errorf("ANTHROPIC_API_KEY not passed through")
	}
	// Thinking must NOT be force-disabled (#73): the SDK adapter spawns the
	// same claude CLI; the retired #3801 workaround must not resurface here.
	if v, ok := env["CLAUDE_CODE_DISABLE_THINKING"]; ok {
		t.Errorf("CLAUDE_CODE_DISABLE_THINKING forced to %q; the adapter must not set it", v)
	}
}

func TestGeminiSdkAdapter(t *testing.T) {
	adapter := NewGeminiSdkAdapter()
	if adapter.Name() != "gemini-sdk" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if adapter.UsesStdin() {
		t.Error("Gemini SDK adapter should not use stdin")
	}

	t.Setenv("GEMINI_API_KEY", "test-gemini-key")
	t.Setenv("GOOGLE_API_KEY", "test-google-key")

	const prompt = "Implement issue 1562 per the plan."
	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/feature-dev/SKILL.md",
		Prompt:      prompt,
		IssueNumber: 1562,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		Model:       "gemini-2.5-flash",
	})

	if cmd != "gemini" {
		t.Errorf("cmd = %q", cmd)
	}
	// #53: positional prompt, same contract as gemini.go (#4032) — the CLI has
	// no --prompt-file flag, and the pre-#53 form dropped the built prompt.
	if len(args) == 0 || args[0] != prompt {
		t.Errorf("expected prompt as the first positional arg, got args = %v", args)
	}
	if !containsArg(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	if containsArg(args, "--prompt-file") {
		t.Errorf("--prompt-file must not be emitted (CLI has no such flag), args = %v", args)
	}
	if containsArg(args, "--noinput") {
		t.Errorf("--noinput must not be emitted, args = %v", args)
	}
	if !containsArg(args, "--model") || !containsArg(args, "gemini-2.5-flash") {
		t.Errorf("missing --model gemini-2.5-flash, args = %v", args)
	}
	if env["NIGHTGAUGE_ADAPTER"] != "gemini-sdk" {
		t.Errorf("adapter env = %q", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["NIGHTGAUGE_OUTPUT_FORMAT"] != "stream-json" {
		t.Errorf("output format env = %q", env["NIGHTGAUGE_OUTPUT_FORMAT"])
	}
	if env["GEMINI_API_KEY"] != "test-gemini-key" {
		t.Errorf("GEMINI_API_KEY not passed through")
	}
	if env["GOOGLE_API_KEY"] != "test-google-key" {
		t.Errorf("GOOGLE_API_KEY not passed through")
	}
}

func TestOllamaAdapter(t *testing.T) {
	adapter := NewOllamaAdapter()
	if adapter.Name() != "ollama" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("Ollama adapter should use stdin (uses claude CLI bridge)")
	}

	t.Setenv("NIGHTGAUGE_OLLAMA_MODEL", "llama2")
	t.Setenv("NIGHTGAUGE_OLLAMA_BASE_URL", "http://localhost:11434/v1")

	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:    "/skills/feature-dev/SKILL.md",
		IssueNumber:  2592,
		Repo:         "nightgauge/nightgauge",
		Stage:        "feature-dev",
		AllowedTools: []string{"Read", "Edit"},
	})

	if cmd != "claude" {
		t.Errorf("cmd = %q, want claude", cmd)
	}
	if !containsArg(args, "-p") {
		t.Error("missing -p flag")
	}
	if !containsArg(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	if !containsArg(args, "--allowedTools") {
		t.Error("missing --allowedTools flag")
	}
	if env["NIGHTGAUGE_ADAPTER"] != "ollama" {
		t.Errorf("adapter env = %q, want ollama", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["NIGHTGAUGE_OLLAMA_MODEL"] != "llama2" {
		t.Errorf("ollama model not passed through: %q", env["NIGHTGAUGE_OLLAMA_MODEL"])
	}
	if env["NIGHTGAUGE_OLLAMA_BASE_URL"] != "http://localhost:11434/v1" {
		t.Errorf("ollama base url not passed through: %q", env["NIGHTGAUGE_OLLAMA_BASE_URL"])
	}
}

func TestOllamaAdapterEnvVarPassthrough(t *testing.T) {
	adapter := NewOllamaAdapter()

	t.Setenv("NIGHTGAUGE_OLLAMA_MODEL", "codellama")
	t.Setenv("NIGHTGAUGE_OLLAMA_API_KEY", "test-key")
	t.Setenv("NIGHTGAUGE_OLLAMA_TIMEOUT_MS", "600000")

	_, _, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/test/SKILL.md",
		IssueNumber: 2592,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})

	if env["NIGHTGAUGE_OLLAMA_MODEL"] != "codellama" {
		t.Errorf("model not passed: %q", env["NIGHTGAUGE_OLLAMA_MODEL"])
	}
	if env["NIGHTGAUGE_OLLAMA_API_KEY"] != "test-key" {
		t.Errorf("api key not passed: %q", env["NIGHTGAUGE_OLLAMA_API_KEY"])
	}
	if env["NIGHTGAUGE_OLLAMA_TIMEOUT_MS"] != "600000" {
		t.Errorf("timeout not passed: %q", env["NIGHTGAUGE_OLLAMA_TIMEOUT_MS"])
	}
}

func TestLmStudioAdapter(t *testing.T) {
	adapter := NewLmStudioAdapter()
	if adapter.Name() != "lm-studio" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("LM Studio adapter should use stdin (uses claude CLI bridge)")
	}

	t.Setenv("NIGHTGAUGE_LM_STUDIO_MODEL", "mistral-7b")
	t.Setenv("NIGHTGAUGE_LM_STUDIO_BASE_URL", "http://localhost:1234/v1")

	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:    "/skills/feature-dev/SKILL.md",
		IssueNumber:  2597,
		Repo:         "nightgauge/nightgauge",
		Stage:        "feature-dev",
		AllowedTools: []string{"Read", "Edit"},
	})

	if cmd != "claude" {
		t.Errorf("cmd = %q, want claude", cmd)
	}
	if !containsArg(args, "-p") {
		t.Error("missing -p flag")
	}
	if !containsArg(args, "stream-json") {
		t.Error("missing stream-json output format")
	}
	if !containsArg(args, "--allowedTools") {
		t.Error("missing --allowedTools flag")
	}
	if env["NIGHTGAUGE_ADAPTER"] != "lm-studio" {
		t.Errorf("adapter env = %q, want lm-studio", env["NIGHTGAUGE_ADAPTER"])
	}
	if env["NIGHTGAUGE_LM_STUDIO_MODEL"] != "mistral-7b" {
		t.Errorf("lm studio model not passed through: %q", env["NIGHTGAUGE_LM_STUDIO_MODEL"])
	}
	if env["NIGHTGAUGE_LM_STUDIO_BASE_URL"] != "http://localhost:1234/v1" {
		t.Errorf("lm studio base url not passed through: %q", env["NIGHTGAUGE_LM_STUDIO_BASE_URL"])
	}
}

func TestLmStudioAdapterEnvVarPassthrough(t *testing.T) {
	adapter := NewLmStudioAdapter()

	t.Setenv("NIGHTGAUGE_LM_STUDIO_MODEL", "llama-3")
	t.Setenv("NIGHTGAUGE_LM_STUDIO_API_KEY", "custom-key")
	t.Setenv("NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS", "300000")

	_, _, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/test/SKILL.md",
		IssueNumber: 2597,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})

	if env["NIGHTGAUGE_LM_STUDIO_MODEL"] != "llama-3" {
		t.Errorf("model not passed: %q", env["NIGHTGAUGE_LM_STUDIO_MODEL"])
	}
	if env["NIGHTGAUGE_LM_STUDIO_API_KEY"] != "custom-key" {
		t.Errorf("api key not passed: %q", env["NIGHTGAUGE_LM_STUDIO_API_KEY"])
	}
	if env["NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS"] != "300000" {
		t.Errorf("timeout not passed: %q", env["NIGHTGAUGE_LM_STUDIO_TIMEOUT_MS"])
	}
}

func TestCopilotAdapter(t *testing.T) {
	adapter := NewCopilotAdapter()
	if adapter.Name() != "copilot" {
		t.Errorf("Name = %q", adapter.Name())
	}
	if !adapter.UsesStdin() {
		t.Error("Copilot adapter should use stdin")
	}

	t.Setenv("COPILOT_GITHUB_TOKEN", "ghs-copilot-token")
	t.Setenv("GH_TOKEN", "ghs-gh-token")

	cmd, args, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/feature-dev/SKILL.md",
		IssueNumber: 2597,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
		TargetRepo:  "nightgauge/nightgauge",
	})

	if cmd != "copilot" {
		t.Errorf("cmd = %q, want copilot", cmd)
	}
	if !containsArg(args, "--allow-all-tools") {
		t.Error("missing --allow-all-tools flag")
	}
	// No model override → --model must be omitted so copilot uses its default.
	if containsArg(args, "--model") {
		t.Errorf("--model must not be present without a model override: %v", args)
	}
	if env["NIGHTGAUGE_ADAPTER"] != "copilot" {
		t.Errorf("adapter env = %q, want copilot", env["NIGHTGAUGE_ADAPTER"])
	}
	// All tokens passed through independently; TypeScript side applies priority
	if env["COPILOT_GITHUB_TOKEN"] != "ghs-copilot-token" {
		t.Errorf("COPILOT_GITHUB_TOKEN not passed through: %q", env["COPILOT_GITHUB_TOKEN"])
	}
	if env["GH_TOKEN"] != "ghs-gh-token" {
		t.Errorf("GH_TOKEN not passed through: %q", env["GH_TOKEN"])
	}
	if env["NIGHTGAUGE_TARGET_REPO"] != "nightgauge/nightgauge" {
		t.Errorf("target repo env = %q", env["NIGHTGAUGE_TARGET_REPO"])
	}
}

func TestCopilotAdapterOnlyCopilotToken(t *testing.T) {
	adapter := NewCopilotAdapter()

	t.Setenv("COPILOT_GITHUB_TOKEN", "ghs-copilot-only")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")

	_, _, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/test/SKILL.md",
		IssueNumber: 2597,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})

	if env["COPILOT_GITHUB_TOKEN"] != "ghs-copilot-only" {
		t.Errorf("COPILOT_GITHUB_TOKEN not passed: %q", env["COPILOT_GITHUB_TOKEN"])
	}
	if _, ok := env["GH_TOKEN"]; ok {
		t.Errorf("GH_TOKEN should not be set when empty: %q", env["GH_TOKEN"])
	}
}

func TestCopilotAdapterGHTokenPassthrough(t *testing.T) {
	adapter := NewCopilotAdapter()

	t.Setenv("COPILOT_GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "ghs-gh-token")
	t.Setenv("GITHUB_TOKEN", "")

	_, _, env := adapter.BuildCommand(RunOptions{
		SkillPath:   "/skills/test/SKILL.md",
		IssueNumber: 2597,
		Repo:        "nightgauge/nightgauge",
		Stage:       "feature-dev",
	})

	if env["GH_TOKEN"] != "ghs-gh-token" {
		t.Errorf("GH_TOKEN not passed: %q", env["GH_TOKEN"])
	}
}

// TestResolveCopilotModel mirrors the SDK copilot model policy (#52): Claude
// tiers and escalation ids map to a concrete copilot-hosted model id via the
// shared registry; concrete/unknown ids pass through (copilot is an OPEN set).
func TestResolveCopilotModel(t *testing.T) {
	cases := map[string]string{
		"haiku":             "gpt-4o-mini",       // registry copilot band: haiku
		"sonnet":            "gpt-4o",            // registry copilot band: sonnet
		"opus":              "claude-sonnet-4.5", // registry copilot band: opus
		"fable":             "claude-sonnet-4.5", // registry copilot band: fable
		"claude-sonnet-4-6": "gpt-4o",            // scheduler escalation id → sonnet band
		"claude-opus-4-8":   "claude-sonnet-4.5", // escalation id → opus band
		"claude-haiku-4-5":  "gpt-4o-mini",       // escalation id → haiku band
		"gpt-4o":            "gpt-4o",            // concrete id passes through
		"gpt-5.2":           "gpt-5.2",           // unknown-to-registry id passes through (OPEN)
		"claude-sonnet-4.6": "gpt-4o",            // claude-* prefix → sonnet band
	}
	for in, want := range cases {
		if got := resolveCopilotModel(in); got != want {
			t.Errorf("resolveCopilotModel(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCopilotAdapterModelInjection verifies BuildCommand forwards a resolved
// --model when a model override is present (#52 — the prior adapter never sent
// --model, so model selection was cosmetic).
func TestCopilotAdapterModelInjection(t *testing.T) {
	adapter := NewCopilotAdapter()

	// Tier alias resolves to a concrete copilot id before reaching --model.
	_, args, _ := adapter.BuildCommand(RunOptions{
		SkillPath: "/skills/feature-dev/SKILL.md",
		Stage:     "feature-dev",
		Model:     "sonnet",
	})
	if !containsArg(args, "--model") || !containsArg(args, "gpt-4o") {
		t.Errorf("expected tier 'sonnet' to resolve to --model gpt-4o in %v", args)
	}
	if containsArg(args, "sonnet") {
		t.Errorf("raw tier 'sonnet' must not reach copilot --model in %v", args)
	}

	// A concrete copilot id passes through unchanged.
	_, args2, _ := adapter.BuildCommand(RunOptions{
		SkillPath: "/skills/feature-dev/SKILL.md",
		Stage:     "feature-dev",
		Model:     "gpt-5.2",
	})
	if !containsArg(args2, "--model") || !containsArg(args2, "gpt-5.2") {
		t.Errorf("expected concrete model gpt-5.2 to pass through in %v", args2)
	}
}

func TestRegistryAliasLmStudio(t *testing.T) {
	registry := NewRegistry()

	// "lmstudio" should resolve to "lm-studio"
	adapter, err := registry.Get("lmstudio")
	if err != nil {
		t.Fatalf("Get(lmstudio) error: %v", err)
	}
	if adapter.Name() != "lm-studio" {
		t.Errorf("alias lmstudio resolved to %q, want lm-studio", adapter.Name())
	}
}

func TestRegistryGet(t *testing.T) {
	registry := NewRegistry()

	tests := []struct {
		name     string
		expected string
	}{
		{"claude-headless", "claude"},
		{"claude-sdk", "claude-sdk"},
		{"codex", "codex"},
		{"gemini", "gemini"},
		{"gemini-sdk", "gemini-sdk"},
		{"ollama", "ollama"},
		{"lm-studio", "lm-studio"},
		{"copilot", "copilot"},
	}

	for _, tt := range tests {
		adapter, err := registry.Get(tt.name)
		if err != nil {
			t.Errorf("Get(%q) error: %v", tt.name, err)
			continue
		}
		if adapter.Name() != tt.expected {
			t.Errorf("Get(%q).Name() = %q, want %q", tt.name, adapter.Name(), tt.expected)
		}
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	registry := NewRegistry()
	_, err := registry.Get("unknown-adapter")
	if err == nil {
		t.Error("expected error for unknown adapter")
	}
}

func TestRegistryAliases(t *testing.T) {
	registry := NewRegistry()

	// "claude" should resolve to "claude-headless"
	adapter, err := registry.Get("claude")
	if err != nil {
		t.Fatalf("Get(claude) error: %v", err)
	}
	if adapter.Name() != "claude" {
		t.Errorf("alias claude resolved to %q", adapter.Name())
	}

	// "gemini-headless" should resolve to "gemini"
	adapter, err = registry.Get("gemini-headless")
	if err != nil {
		t.Fatalf("Get(gemini-headless) error: %v", err)
	}
	if adapter.Name() != "gemini" {
		t.Errorf("alias gemini-headless resolved to %q", adapter.Name())
	}
}

func TestRegistryNames(t *testing.T) {
	registry := NewRegistry()
	names := registry.Names()

	expected := []string{"claude-headless", "claude-sdk", "codex", "copilot", "gemini", "gemini-sdk", "lm-studio", "ollama"}
	if len(names) != len(expected) {
		t.Fatalf("Names() = %v, want %v", names, expected)
	}
	for i, name := range names {
		if name != expected[i] {
			t.Errorf("Names()[%d] = %q, want %q", i, name, expected[i])
		}
	}
}

func TestRegistryList(t *testing.T) {
	registry := NewRegistry()
	infos := registry.List()

	if len(infos) != 8 {
		t.Fatalf("List() returned %d items, want 8", len(infos))
	}

	// Verify each info has the expected fields
	for _, info := range infos {
		if info.Name == "" {
			t.Error("empty adapter name in List()")
		}
		if info.DisplayName == "" {
			t.Error("empty display name in List()")
		}
		if info.Binary == "" {
			t.Error("empty binary in List()")
		}
	}
}

func TestRegistryResolveExplicit(t *testing.T) {
	registry := NewRegistry()

	adapter, err := registry.Resolve("codex", "")
	if err != nil {
		t.Fatalf("Resolve(codex) error: %v", err)
	}
	if adapter.Name() != "codex" {
		t.Errorf("Resolve(codex) = %q", adapter.Name())
	}
}

func TestRegistryResolveFromEnv(t *testing.T) {
	t.Setenv("NIGHTGAUGE_ADAPTER", "gemini")
	registry := NewRegistry()

	adapter, err := registry.Resolve("", "") // no explicit name
	if err != nil {
		t.Fatalf("Resolve('') with env error: %v", err)
	}
	if adapter.Name() != "gemini" {
		t.Errorf("Resolve('') with env = %q, want gemini", adapter.Name())
	}
}

func TestRegistryResolvePrecedence(t *testing.T) {
	// #54: explicit > NIGHTGAUGE_ADAPTER > configDefault > claude-headless.
	// The pre-#54 API-key auto-detect is gone — exported keys and model env
	// vars no longer influence which adapter runs.
	t.Setenv("ANTHROPIC_API_KEY", "sk-would-have-picked-claude-sdk")
	t.Setenv("NIGHTGAUGE_OLLAMA_MODEL", "would-have-picked-ollama")
	t.Setenv("NIGHTGAUGE_ADAPTER", "")

	registry := NewRegistry()

	adapter, err := registry.Resolve("", "")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if adapter.Name() != "claude" {
		t.Errorf("default = %q, want claude (headless) — API keys must not steer resolution", adapter.Name())
	}

	adapter, err = registry.Resolve("", "gemini")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if adapter.Name() != "gemini" {
		t.Errorf("configDefault = %q, want gemini", adapter.Name())
	}

	t.Setenv("NIGHTGAUGE_ADAPTER", "codex")
	adapter, err = registry.Resolve("", "gemini")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if adapter.Name() != "codex" {
		t.Errorf("env override = %q, want codex (env outranks configDefault)", adapter.Name())
	}

	adapter, err = registry.Resolve("copilot", "gemini")
	if err != nil {
		t.Fatalf("Resolve error: %v", err)
	}
	if adapter.Name() != "copilot" {
		t.Errorf("explicit = %q, want copilot (explicit outranks all)", adapter.Name())
	}
}

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if strings.Contains(a, flag) {
			return true
		}
	}
	return false
}

// ─── Agentic capability declarations (#57) ───────────────────────────────────

func TestAdapterAgenticDeclarations(t *testing.T) {
	cases := []struct {
		adapter SkillRunner
		want    bool
	}{
		{NewClaudeAdapter(), true},
		{NewClaudeSdkAdapter(), true},
		{NewCodexAdapter(), true},
		{NewGeminiAdapter(), true},
		// The Go gemini-sdk adapter spawns the agentic gemini CLI (#60) —
		// unlike its chat-only TypeScript namesake.
		{NewGeminiSdkAdapter(), true},
		{NewCopilotAdapter(), true},
		// The local bridges bottom out in the TypeScript fetch/SSE adapters
		// with zero tool handling — barred from pipeline dispatch.
		{NewOllamaAdapter(), false},
		{NewLmStudioAdapter(), false},
	}
	for _, c := range cases {
		if got := c.adapter.Agentic(); got != c.want {
			t.Errorf("%s.Agentic() = %v, want %v", c.adapter.Name(), got, c.want)
		}
	}
}

// ─── Gemini registry-backed model validation (#57, mirrors codex #4021) ─────

func TestGeminiAdapterValidateModel(t *testing.T) {
	adapter := NewGeminiAdapter()
	for _, ok := range []string{"", "opus", "haiku", "gemini-2.5-pro", "claude-sonnet-4-6"} {
		if err := adapter.ValidateModel(ok); err != nil {
			t.Errorf("ValidateModel(%q) = %v, want nil", ok, err)
		}
	}
	if err := adapter.ValidateModel("not-a-real-model"); err == nil {
		t.Error("ValidateModel(not-a-real-model) = nil, want error")
	}
	sdk := NewGeminiSdkAdapter()
	if err := sdk.ValidateModel("fable"); err != nil {
		t.Errorf("gemini-sdk ValidateModel(fable) = %v, want nil", err)
	}
}

func TestResolveGeminiModel(t *testing.T) {
	cases := map[string]string{
		"haiku":             "gemini-2.5-flash",
		"sonnet":            "gemini-2.5-flash",
		"opus":              "gemini-2.5-pro",
		"fable":             "gemini-2.5-pro",
		"claude-sonnet-4-6": "gemini-2.5-flash",
		"claude-fable-5":    "gemini-2.5-pro",
		"gemini-2.0-flash":  "gemini-2.0-flash", // concrete id passes through
		"mystery-model":     "mystery-model",    // unknown passes through (validation rejects)
	}
	for in, want := range cases {
		if got := resolveGeminiModel(in); got != want {
			t.Errorf("resolveGeminiModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGeminiBuildCommandTranslatesModel(t *testing.T) {
	for _, adapter := range []SkillRunner{NewGeminiAdapter(), NewGeminiSdkAdapter()} {
		_, args, _ := adapter.BuildCommand(RunOptions{Prompt: "p", Model: "sonnet"})
		found := false
		for i, a := range args {
			if a == "--model" && i+1 < len(args) {
				found = true
				if args[i+1] != "gemini-2.5-flash" {
					t.Errorf("%s: --model %q, want gemini-2.5-flash", adapter.Name(), args[i+1])
				}
			}
		}
		if !found {
			t.Errorf("%s: expected --model in args %v", adapter.Name(), args)
		}
	}
}
