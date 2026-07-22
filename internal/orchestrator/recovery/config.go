package recovery

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultMaxAttemptsPerRun bounds the number of FailureRecovery attempts that
// can fire within a single pipeline run. Picked conservatively to keep the
// cap above the 6-stage pipeline length while bounded enough that a sick
// pipeline cannot loop indefinitely.
const DefaultMaxAttemptsPerRun = 3

// EnvMaxAttemptsPerRun is the environment override for
// DefaultMaxAttemptsPerRun. Higher precedence than the YAML config, mirroring
// the existing GetPipelineFailureMode pattern.
const EnvMaxAttemptsPerRun = "NIGHTGAUGE_RECOVERY_MAX_ATTEMPTS"

// GetMaxAttemptsPerRun reads `pipeline.recovery.max_attempts_per_run` from
// `.nightgauge/config.yaml` with env-var override and a safe default.
//
// Mirrors the inline indented-line parser pattern from
// failure_handler.GetPipelineFailureMode — avoids dragging a YAML library
// into the recovery package and stays consistent with the surrounding code.
//
// YAML shape:
//
//	pipeline:
//	  recovery:
//	    max_attempts_per_run: 3
//
// Env override: NIGHTGAUGE_RECOVERY_MAX_ATTEMPTS (integer)
func GetMaxAttemptsPerRun(workspaceRoot string) int {
	if v := os.Getenv(EnvMaxAttemptsPerRun); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	if workspaceRoot == "" {
		return DefaultMaxAttemptsPerRun
	}
	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return DefaultMaxAttemptsPerRun
	}

	// Two-level nested parser. Walks the file line-by-line, tracking whether
	// we're inside `pipeline:` and then inside `pipeline.recovery:`. Indented
	// `max_attempts_per_run:` is matched and parsed.
	inPipeline := false
	inRecovery := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A non-indented top-level key resets both contexts.
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = trimmed == "pipeline:"
			inRecovery = false
			continue
		}
		if !inPipeline {
			continue
		}
		// We are indented and inside pipeline. Detect the recovery: subkey,
		// using the count of leading spaces to discriminate first-level
		// (recovery:) from second-level (max_attempts_per_run:) entries.
		indent := leadingSpaces(line)
		if indent <= 2 {
			// First-level child of pipeline:.
			inRecovery = trimmed == "recovery:"
			// Allow a flat `pipeline.recovery_max_attempts_per_run: N` form
			// even though the canonical shape is nested.
			if strings.HasPrefix(trimmed, "recovery_max_attempts_per_run:") {
				if n, ok := parseIntKV(trimmed); ok {
					return n
				}
			}
			continue
		}
		if inRecovery && strings.HasPrefix(trimmed, "max_attempts_per_run:") {
			if n, ok := parseIntKV(trimmed); ok {
				return n
			}
		}
	}
	return DefaultMaxAttemptsPerRun
}

// DefaultConflictMaxDevRedispatch bounds how many times the conflict-recovery
// loop re-dispatches feature-dev to resolve a rebase conflict before escalating
// with the specific files (#4072). Lower than the legacy fresh-branch
// MaxConflictRestarts (3) because a dev re-dispatch is more expensive than a
// fresh restart. Kept as a recovery-package constant so this reader carries no
// config import (the typed config block in internal/config mirrors the same
// default value).
const DefaultConflictMaxDevRedispatch = 2

// DefaultConflictRecoveryEnabled is the default for
// `pipeline.recovery.conflict_recovery.enabled` (mirrors the typed config
// default). When false, the conflict-recovery action is not registered, so an
// unresolvable pr-merge conflict falls through to branch-out-of-date / triage.
const DefaultConflictRecoveryEnabled = true

// EnvConflictMaxDevRedispatch is the environment override for the conflict
// re-dispatch bound. Higher precedence than the YAML config, mirroring
// EnvMaxAttemptsPerRun.
const EnvConflictMaxDevRedispatch = "NIGHTGAUGE_CONFLICT_MAX_REDISPATCH"

// EnvConflictRecoveryEnabled is the environment override for enabling/disabling
// the conflict-recovery loop ("0"/"false" disables; "1"/"true" enables).
const EnvConflictRecoveryEnabled = "NIGHTGAUGE_CONFLICT_RECOVERY_ENABLED"

// GetConflictMaxDevRedispatch reads
// `pipeline.recovery.conflict_recovery.max_dev_redispatch` from
// `.nightgauge/config.yaml` with env-var override and a safe default.
// Mirrors GetMaxAttemptsPerRun's inline indented-line parser, extended one
// level deeper for the conflict_recovery: sub-block.
//
// YAML shape:
//
//	pipeline:
//	  recovery:
//	    conflict_recovery:
//	      max_dev_redispatch: 2
//
// Env override: NIGHTGAUGE_CONFLICT_MAX_REDISPATCH (integer)
func GetConflictMaxDevRedispatch(workspaceRoot string) int {
	if v := os.Getenv(EnvConflictMaxDevRedispatch); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	if workspaceRoot == "" {
		return DefaultConflictMaxDevRedispatch
	}
	configPath := filepath.Join(workspaceRoot, ".nightgauge", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return DefaultConflictMaxDevRedispatch
	}

	// Three-level nested parser: pipeline: → recovery: → conflict_recovery:.
	// Indent thresholds discriminate the levels (≤2 = pipeline child,
	// ≤4 = recovery child, deeper = conflict_recovery leaf).
	inPipeline := false
	inRecovery := false
	inConflict := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = trimmed == "pipeline:"
			inRecovery = false
			inConflict = false
			continue
		}
		if !inPipeline {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= 2 {
			inRecovery = trimmed == "recovery:"
			inConflict = false
			continue
		}
		if !inRecovery {
			continue
		}
		if indent <= 4 {
			inConflict = trimmed == "conflict_recovery:"
			continue
		}
		if inConflict && strings.HasPrefix(trimmed, "max_dev_redispatch:") {
			if n, ok := parseIntKV(trimmed); ok {
				return n
			}
		}
	}
	return DefaultConflictMaxDevRedispatch
}

// GetConflictRecoveryEnabled reads
// `pipeline.recovery.conflict_recovery.enabled` (env override
// NIGHTGAUGE_CONFLICT_RECOVERY_ENABLED) with a default of true. When false,
// Default() does not register the conflict-recovery action. Mirrors
// GetConflictMaxDevRedispatch's nested-line parser.
func GetConflictRecoveryEnabled(workspaceRoot string) bool {
	if v := strings.TrimSpace(os.Getenv(EnvConflictRecoveryEnabled)); v != "" {
		switch strings.ToLower(v) {
		case "0", "false", "no", "off":
			return false
		case "1", "true", "yes", "on":
			return true
		}
	}
	if workspaceRoot == "" {
		return DefaultConflictRecoveryEnabled
	}
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".nightgauge", "config.yaml"))
	if err != nil {
		return DefaultConflictRecoveryEnabled
	}
	inPipeline, inRecovery, inConflict := false, false, false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inPipeline = trimmed == "pipeline:"
			inRecovery, inConflict = false, false
			continue
		}
		if !inPipeline {
			continue
		}
		indent := leadingSpaces(line)
		if indent <= 2 {
			inRecovery = trimmed == "recovery:"
			inConflict = false
			continue
		}
		if !inRecovery {
			continue
		}
		if indent <= 4 {
			inConflict = trimmed == "conflict_recovery:"
			continue
		}
		if inConflict && strings.HasPrefix(trimmed, "enabled:") {
			val := strings.ToLower(strings.Trim(strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1]), `"'`))
			return val != "false" && val != "no" && val != "off" && val != "0"
		}
	}
	return DefaultConflictRecoveryEnabled
}

func parseIntKV(line string) (int, bool) {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) != 2 {
		return 0, false
	}
	val := strings.TrimSpace(parts[1])
	val = strings.Trim(val, `"'`)
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func leadingSpaces(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			n++
			continue
		}
		if s[i] == '\t' {
			n += 2
			continue
		}
		break
	}
	return n
}
