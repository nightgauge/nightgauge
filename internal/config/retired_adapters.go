package config

import (
	"fmt"
	"sort"
	"strings"
)

// retiredAdapters are adapter names that no longer exist (#2128). Local
// models run through the opencode adapter against any OpenAI-compatible
// server, and the eval judge uses the openai-compatible backend. A config,
// flag or environment variable that still names one fails loudly rather than
// falling back to a different adapter.
var retiredAdapters = map[string]bool{
	"lm-studio": true,
	"lmstudio":  true,
	"ollama":    true,
}

// RetiredAdapterError returns the migration error for a retired adapter name,
// or nil when name is not retired. where names the setting that carried it.
func RetiredAdapterError(name, where string) error {
	n := strings.ToLower(strings.TrimSpace(name))
	if !retiredAdapters[n] {
		return nil
	}
	return fmt.Errorf(
		"%s names adapter %q, which was removed (#2128): local models now run "+
			"through the opencode adapter against any OpenAI-compatible server "+
			"(LM Studio, Ollama, llama.cpp, vLLM), declared under "+
			"opencode.endpoints; the eval judge uses the openai-compatible "+
			"backend. Set the adapter to opencode (or openai-compatible for the judge)",
		where, name)
}

// ValidateNoRetiredAdapters rejects a config whose ui.core.adapter or any
// pipeline.stage_adapters entry names a retired adapter.
func ValidateNoRetiredAdapters(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	if cfg.UI != nil && cfg.UI.Core != nil {
		if err := RetiredAdapterError(cfg.UI.Core.Adapter, "ui.core.adapter"); err != nil {
			return err
		}
	}
	if cfg.Pipeline != nil {
		stages := make([]string, 0, len(cfg.Pipeline.StageAdapters))
		for s := range cfg.Pipeline.StageAdapters {
			stages = append(stages, s)
		}
		sort.Strings(stages)
		for _, s := range stages {
			where := "pipeline.stage_adapters." + s
			if err := RetiredAdapterError(cfg.Pipeline.StageAdapters[s], where); err != nil {
				return err
			}
		}
	}
	return nil
}
