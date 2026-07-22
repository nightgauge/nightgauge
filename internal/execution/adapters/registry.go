package adapters

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

// AdapterFactory is a constructor function for a SkillRunner.
type AdapterFactory func() SkillRunner

// AdapterInfo holds metadata about a registered adapter.
type AdapterInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Binary      string `json:"binary"`
	Available   bool   `json:"available"`
}

// Registry maps adapter names to their factory functions.
type Registry struct {
	factories map[string]AdapterFactory
	aliases   map[string]string
}

// NewRegistry creates a registry pre-populated with all built-in adapters.
func NewRegistry() *Registry {
	r := &Registry{
		factories: map[string]AdapterFactory{
			"claude-headless": func() SkillRunner { return NewClaudeAdapter() },
			"claude-sdk":      func() SkillRunner { return NewClaudeSdkAdapter() },
			"codex":           func() SkillRunner { return NewCodexAdapter() },
			"gemini":          func() SkillRunner { return NewGeminiAdapter() },
			"gemini-sdk":      func() SkillRunner { return NewGeminiSdkAdapter() },
			"ollama":          func() SkillRunner { return NewOllamaAdapter() },
			"lm-studio":       func() SkillRunner { return NewLmStudioAdapter() },
			"copilot":         func() SkillRunner { return NewCopilotAdapter() },
		},
		aliases: map[string]string{
			"claude":          "claude-headless",
			"gemini-headless": "gemini",
			"lmstudio":        "lm-studio",
		},
	}
	return r
}

// Get returns a SkillRunner by name, resolving aliases.
// Returns an error if the adapter name is unknown.
func (r *Registry) Get(name string) (SkillRunner, error) {
	resolved := r.resolve(name)
	factory, ok := r.factories[resolved]
	if !ok {
		return nil, fmt.Errorf("unknown adapter %q (available: %s)", name, strings.Join(r.Names(), ", "))
	}
	return factory(), nil
}

// Names returns sorted adapter names (excluding aliases).
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.factories))
	for name := range r.factories {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// List returns metadata about all registered adapters, including
// whether their CLI binary is available on PATH.
func (r *Registry) List() []AdapterInfo {
	infos := make([]AdapterInfo, 0, len(r.factories))

	for _, name := range r.Names() {
		adapter := r.factories[name]()
		binary := adapterBinary(name)
		_, err := exec.LookPath(binary)

		infos = append(infos, AdapterInfo{
			Name:        name,
			DisplayName: adapterDisplayName(name),
			Binary:      binary,
			Available:   err == nil,
		})
		_ = adapter // used only for name verification
	}
	return infos
}

// Resolve resolves an adapter name from (in priority order):
//  1. Explicit name (--adapter flag)
//  2. NIGHTGAUGE_ADAPTER env var
//  3. configDefault — the caller's config-resolved value
//     (pipeline.stage_adapters.<stage> / ui.core.adapter, #54)
//  4. Default: "claude-headless"
//
// The pre-#54 API-key auto-detect (ANTHROPIC_API_KEY → claude-sdk, model
// envs → local adapters, …) is deleted: adapter selection follows the one
// canonical precedence chain shared with the SDK CLI and VSCode resolver —
// an exported API key no longer silently changes which adapter runs.
func (r *Registry) Resolve(explicit, configDefault string) (SkillRunner, error) {
	name := explicit

	if name == "" {
		name = os.Getenv("NIGHTGAUGE_ADAPTER")
	}

	if name == "" {
		name = configDefault
	}

	if name == "" {
		name = "claude-headless"
	}

	return r.Get(name)
}

// resolve resolves aliases to canonical names.
func (r *Registry) resolve(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	if canonical, ok := r.aliases[name]; ok {
		return canonical
	}
	return name
}

// TestAdapter checks if an adapter's binary exists and can respond.
// Returns nil if the adapter is available, an error otherwise.
func (r *Registry) TestAdapter(name string) error {
	resolved := r.resolve(name)
	if _, ok := r.factories[resolved]; !ok {
		return fmt.Errorf("unknown adapter %q", name)
	}

	binary := adapterBinary(resolved)
	path, err := exec.LookPath(binary)
	if err != nil {
		return fmt.Errorf("%s binary not found on PATH", binary)
	}

	// Try --version to verify it responds
	cmd := exec.Command(path, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --version failed: %s", binary, strings.TrimSpace(string(out)))
	}

	return nil
}

// adapterBinary returns the CLI binary name for an adapter.
func adapterBinary(name string) string {
	switch {
	case strings.HasPrefix(name, "claude"):
		return "claude"
	case strings.HasPrefix(name, "codex"):
		return "codex"
	case strings.HasPrefix(name, "gemini"):
		return "gemini"
	case name == "ollama":
		return "claude" // Ollama uses the claude CLI as SDK bridge
	case name == "lm-studio":
		return "claude" // LM Studio uses the claude CLI as SDK bridge
	case name == "copilot":
		return "copilot"
	default:
		return name
	}
}

// adapterDisplayName returns a human-readable name for an adapter.
func adapterDisplayName(name string) string {
	switch name {
	case "claude-headless":
		return "Claude Headless"
	case "claude-sdk":
		return "Claude SDK"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini Headless"
	case "gemini-sdk":
		return "Gemini SDK"
	case "ollama":
		return "Ollama"
	case "lm-studio":
		return "LM Studio"
	case "copilot":
		return "GitHub Copilot"
	default:
		return name
	}
}
