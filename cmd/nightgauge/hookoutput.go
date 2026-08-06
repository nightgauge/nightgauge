package main

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/hooks"
)

// preToolUseOutput is Claude Code's PreToolUse hook output contract.
//
// The internal hooks.GateDecision shape (`{"decision":"allow"|"block"}`) is not
// that contract. `decision` *is* a recognized top-level hook key, but its
// PreToolUse enum never contained "allow"/"block", so a known key carrying an
// out-of-enum value fails the root union and every invocation returns:
//
//	PreToolUse hook error — Hook JSON output validation failed — (root): Invalid input
//
// Claude Code surfaces that as a *non-blocking* error, so the tool call
// proceeds: a gate that correctly decided "block" was silently discarded. That
// is #354 — `git push origin main` was never actually blocked by workflow-gate.
//
// Keeping this translation at the cmd layer leaves hooks.GateDecision (and the
// internal callers and tests that depend on its shape) untouched.
type preToolUseOutput struct {
	HookSpecificOutput preToolUseDecision `json:"hookSpecificOutput"`
}

type preToolUseDecision struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// preToolUseFallbackReason is used when a gate blocks without stating why, so a
// denial is never rendered with an empty explanation.
const preToolUseFallbackReason = "Blocked by a nightgauge PreToolUse gate."

// printPreToolUse renders a gate decision in Claude Code's PreToolUse schema.
//
// Only the block path emits output. An allow decision deliberately prints
// NOTHING, because exit 0 with empty stdout is the documented way to say "no
// decision — continue through the normal permission flow". Both ways of
// speaking up on the allow path would be actively harmful:
//
//   - permissionDecision:"allow" *bypasses* the permission system and
//     auto-approves the tool call, so emitting it from a gate matched on
//     Bash|Edit|Write would silently disable the user's permission prompts for
//     every command the gate did not block — a far worse hole than the bug.
//   - permissionDecision:"defer" (added in Claude Code 2.1.89) pauses headless
//     sessions so they can resume with `-p --resume` and re-evaluate, which
//     would stall pipeline runs at every tool call.
//
// Silence is the only mapping that both validates and preserves existing
// permission behavior.
func printPreToolUse(decision hooks.GateDecision) error {
	if decision.Decision != "block" {
		return nil
	}

	reason := decision.Reason
	if reason == "" {
		reason = preToolUseFallbackReason
	}

	return printJSON(preToolUseOutput{
		HookSpecificOutput: preToolUseDecision{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: reason,
		},
	})
}

// readHookInput reads a hook payload from stdin.
//
// PreToolUse, PostToolUse, and Notification hooks are invoked with the payload
// on stdin and NO argv, so a hook command that declares a required flag is
// rejected by cobra before any work runs (#354). Reading through
// cmd.InOrStdin() rather than os.Stdin keeps the commands drivable in-process
// from tests.
//
// A read failure yields nil, which every caller treats as "no payload" and
// fails open — a hook that cannot read its input must not block the session.
// resolveSanitizationMode reads the configured sanitization enforcement level,
// defaulting to warn when there is no readable config.
func resolveSanitizationMode() config.SanitizationMode {
	workdir, err := os.Getwd()
	if err != nil {
		return config.SanitizationModeWarn
	}
	cfg, loadErr := config.Load(workdir)
	if loadErr != nil || cfg == nil || cfg.Sanitization == nil {
		return config.SanitizationModeWarn
	}
	return cfg.Sanitization.ResolvedMode()
}

func readHookInput(cmd *cobra.Command) []byte {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return nil
	}
	return data
}
