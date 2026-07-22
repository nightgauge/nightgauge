package heal

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HealConfig holds throttle + budget settings read from
// `.nightgauge/config.yaml`. Defaults are conservative: 1 active heal PR
// per repo, 3 in any rolling 24h window, 30-line diff budget, and a
// require-human-first gate on the first occurrence of every pattern.
type HealConfig struct {
	MaxActivePerRepo  int
	Max24hPerRepo     int
	DiffBudgetLines   int
	RequireHumanFirst bool
}

// Default heal config values. Used both as the fallback when the YAML key is
// missing and as the package-level documentation for safe defaults.
const (
	DefaultMaxActivePerRepo  = 1
	DefaultMax24hPerRepo     = 3
	DefaultDiffBudgetLines   = 30
	DefaultRequireHumanFirst = true
)

// DefaultConfig returns a HealConfig pre-populated with the safe defaults.
func DefaultConfig() HealConfig {
	return HealConfig{
		MaxActivePerRepo:  DefaultMaxActivePerRepo,
		Max24hPerRepo:     DefaultMax24hPerRepo,
		DiffBudgetLines:   DefaultDiffBudgetLines,
		RequireHumanFirst: DefaultRequireHumanFirst,
	}
}

// GetHealConfig reads `pipeline.heal.*` from
// `<workspaceRoot>/.nightgauge/config.yaml`. Missing keys fall back to
// the defaults defined above. The parser mirrors recovery.GetMaxAttemptsPerRun's
// inline indented-line approach so the heal package stays free of a YAML
// library dependency.
//
// YAML shape:
//
//	pipeline:
//	  heal:
//	    max_active_per_repo: 1
//	    max_24h_per_repo: 3
//	    diff_budget_lines: 30
//	    require_human_first: true
func GetHealConfig(workspaceRoot string) HealConfig {
	cfg := DefaultConfig()
	if workspaceRoot == "" {
		return cfg
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".nightgauge", "config.yaml"))
	if err != nil {
		return cfg
	}

	inPipeline := false
	inHeal := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = trimmed == "pipeline:"
			inHeal = false
			continue
		}
		if !inPipeline {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= 2 {
			inHeal = trimmed == "heal:"
			continue
		}
		if !inHeal {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "max_active_per_repo:"):
			if n, ok := parseIntKV(trimmed); ok {
				cfg.MaxActivePerRepo = n
			}
		case strings.HasPrefix(trimmed, "max_24h_per_repo:"):
			if n, ok := parseIntKV(trimmed); ok {
				cfg.Max24hPerRepo = n
			}
		case strings.HasPrefix(trimmed, "diff_budget_lines:"):
			if n, ok := parseIntKV(trimmed); ok {
				cfg.DiffBudgetLines = n
			}
		case strings.HasPrefix(trimmed, "require_human_first:"):
			if b, ok := parseBoolKV(trimmed); ok {
				cfg.RequireHumanFirst = b
			}
		}
	}
	return cfg
}

func parseIntKV(line string) (int, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	val := strings.TrimSpace(parts[1])
	val = strings.Trim(val, `"'`)
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func parseBoolKV(line string) (bool, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return false, false
	}
	val := strings.ToLower(strings.TrimSpace(parts[1]))
	val = strings.Trim(val, `"'`)
	switch val {
	case "true", "yes", "on", "1":
		return true, true
	case "false", "no", "off", "0":
		return false, true
	}
	return false, false
}

func leadingSpaces(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ' ':
			n++
		case '\t':
			n += 2
		default:
			return n
		}
	}
	return n
}
