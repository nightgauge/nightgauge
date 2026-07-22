package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/hooks"
	"github.com/spf13/cobra"
)

// safeBranchName validates branch names at the CLI boundary before passing to hooks.
var safeBranchName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9\-_./]*$`)

func prePushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pre-push",
		Short: "Pre-push merge validation gate",
		Long:  "Validate changes against the target branch before pushing. Runs merged-state build+test+vet, security scan, and static checks.",
	}

	cmd.AddCommand(prePushValidateCmd(), prePushInstallCmd())
	return cmd
}

func prePushValidateCmd() *cobra.Command {
	var (
		target  string
		timeout int
		jsonOut bool
	)

	cmd := &cobra.Command{
		Use:   "validate [issue-number]",
		Short: "Run merged-state build+test+vet, security, and static checks",
		Long:  "Fetches target branch, creates temporary merge, runs build+test+vet, security scan, and static checks. Writes pre-push-{N}.json context file.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber, err := strconv.Atoi(args[0])
			if err != nil {
				return fmt.Errorf("invalid issue number %q: %w", args[0], err)
			}

			workDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			// Get current branch name
			runner := &hooks.ExecCmdRunner{}
			branchOut, err := runner.Run(cmd.Context(), workDir, "git", "branch", "--show-current")
			if err != nil {
				return fmt.Errorf("get current branch: %w", err)
			}
			featureBranch := strings.TrimSpace(string(branchOut))

			// Resolve target branch from issue context if not explicitly set
			if !cmd.Flags().Changed("target") {
				if resolved := resolveTargetBranch(workDir, issueNumber); resolved != "" {
					target = resolved
				}
			}

			ctx := cmd.Context()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
				defer cancel()
			}

			input := hooks.PrePushInput{
				IssueNumber:   issueNumber,
				WorkDir:       workDir,
				TargetBranch:  target,
				FeatureBranch: featureBranch,
			}

			fmt.Fprintf(os.Stderr, "=== Pre-Push Merge Validation Gate ===\n")
			fmt.Fprintf(os.Stderr, "Issue: #%d | Target: %s | Branch: %s\n\n", issueNumber, target, featureBranch)

			result := hooks.EvaluatePrePush(ctx, runner, input)

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Human-readable output
			printPrePushSummary(result)

			if result.Decision == "block" {
				return fmt.Errorf("pre-push validation failed: %s", result.Reason)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&target, "target", "main", "Target branch to merge against")
	cmd.Flags().IntVar(&timeout, "timeout", 180, "Timeout in seconds")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output JSON instead of human-readable")

	return cmd
}

func prePushInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install git pre-push hook",
		Long:  "Creates .git/hooks/pre-push that calls nightgauge pre-push validate before each push.",
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			hookDir := filepath.Join(workDir, ".git", "hooks")
			if err := os.MkdirAll(hookDir, 0o755); err != nil {
				return fmt.Errorf("create hooks directory: %w", err)
			}

			hookPath := filepath.Join(hookDir, "pre-push")

			hookScript := `#!/bin/bash
# Installed by 'nightgauge pre-push install'
# Runs pre-push merge validation gate before each push.

BRANCH=$(git branch --show-current)
ISSUE=$(echo "$BRANCH" | sed -n 's|^[^/]*/\([0-9]*\).*|\1|p')
[ -z "$ISSUE" ] && exit 0  # No issue number — skip (not a pipeline branch)

BINARY=$(command -v nightgauge 2>/dev/null)
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
[ -z "$BINARY" ] && exit 0  # No binary — skip gracefully

exec "$BINARY" pre-push validate "$ISSUE"
`
			if err := os.WriteFile(hookPath, []byte(hookScript), 0o755); err != nil {
				return fmt.Errorf("write pre-push hook: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Installed git pre-push hook: %s\n", hookPath)
			return nil
		},
	}
	return cmd
}

// resolveTargetBranch reads the base_branch from issue-{N}.json if available.
func resolveTargetBranch(workDir string, issueNumber int) string {
	contextFile := filepath.Join(workDir, ".nightgauge", "pipeline", fmt.Sprintf("issue-%d.json", issueNumber))
	data, err := os.ReadFile(contextFile)
	if err != nil {
		return ""
	}
	var ctx struct {
		BaseBranch string `json:"base_branch"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return ""
	}
	// Validate at the boundary: reject branch names that could be argument-injected.
	if ctx.BaseBranch != "" && !safeBranchName.MatchString(ctx.BaseBranch) {
		fmt.Fprintf(os.Stderr, "Warning: ignoring invalid base_branch %q from context file\n", ctx.BaseBranch)
		return ""
	}
	return ctx.BaseBranch
}

func printPrePushSummary(result hooks.PrePushResult) {
	fmt.Fprintf(os.Stderr, "=== Validation Results ===\n\n")

	phaseOrder := []string{"merged_state", "build", "test", "vet", "security", "static_checks"}
	for _, phase := range phaseOrder {
		status, ok := result.ValidationPhases[phase]
		if !ok {
			continue
		}
		icon := "✓"
		if status == "failed" {
			icon = "✗"
		} else if status == "skipped" {
			icon = "–"
		}
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", icon, phase, status)
	}

	fmt.Fprintf(os.Stderr, "\n")
	if result.Decision == "allow" {
		fmt.Fprintf(os.Stderr, "Result: PASSED — safe to push\n")
	} else {
		fmt.Fprintf(os.Stderr, "Result: BLOCKED — %s\n", result.Reason)
	}

	if result.ContextPath != "" {
		fmt.Fprintf(os.Stderr, "Context: %s\n", result.ContextPath)
	}
}
