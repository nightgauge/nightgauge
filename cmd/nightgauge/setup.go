package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/setup"
	"github.com/spf13/cobra"
)

// setupCmd groups deterministic project-bootstrap verbs. Today it houses
// scaffold-tooling (audit row B37); future setup verbs (e.g. AGENTS.md
// scaffolding, CLAUDE.md generation) will land alongside under the same
// "emit fixed templates with brownfield-safe skips" contract.
func setupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Project-bootstrap verbs (scaffold-tooling)",
		Long: `Deterministic project-bootstrap verbs. Each subcommand emits fixed
templates from compile-time embedded fixtures, never overwriting existing
files. Replaces the heredoc-based config emission in
skills/smart-setup/SKILL.md (audit row B37 et seq.).`,
	}
	cmd.AddCommand(setupScaffoldToolingCmd())
	return cmd
}

func setupScaffoldToolingCmd() *cobra.Command {
	var (
		workdir    string
		selectStr  string
		dryRun     bool
		jsonOutput bool
		runsOn     string
		noPolicy   bool
	)
	cmd := &cobra.Command{
		Use:   "scaffold-tooling",
		Short: "Emit fixed tooling configs (tsconfig, vitest, eslint, prettier, ci.yml) with brownfield-safe skips",
		Long: `Emits any combination of tsconfig.json, vitest.config.ts,
eslint.config.js, .prettierrc, and .github/workflows/ci.yml from fixed
embedded templates. Existing files are never overwritten — for ESLint
and Prettier the verb also probes legacy filenames (.eslintrc.js,
.eslintrc.json, .prettierrc.json, prettier.config.js) before writing.

The CI workflow is generated from package.json: one step per gate
script that exists (typecheck, lint, test, build, in that order), each
run as "npm run <script>" with no appended arguments. The Node major
version comes from engines.node (default "20"); the runner from
--runs-on (default ubuntu-latest). Every "uses:" ref is pinned to a
full 40-char commit SHA with the release tag as a trailing comment, so
orgs enforcing sha_pinning_required accept it. When ci is selected the
verb reads the repository's Actions policy via "gh api" (best-effort;
skipped with a warning when the token lacks access) and warns if the
policy would reject the workflow. outcomes' sibling fields ci_steps
and runs_on report which scripts became steps and the runner used.

Schema version 1 — field names (v, workdir, selected, detected,
outcomes, warnings) and the closed enums for outcomes[].key
(tsconfig, vitest, eslint, prettier, ci) and outcomes[].outcome
(created, skipped_existing, skipped_missing_dep, skipped_disabled,
error) are stable. Skills parse the JSON via fixed jq paths; any
breaking change requires bumping v.

Exit codes:
  0  scan completed (per-file errors land in outcomes[].outcome)
  2  hard error (e.g. unresolvable workdir, unknown --select key)`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := setup.RunScaffoldTooling(cmd.Context(), setup.ScaffoldToolingOptions{
				Workdir: workdir,
				Select:  parseSelect(selectStr),
				DryRun:  dryRun,
				RunsOn:  runsOn,
				PolicyProbe: func() func(context.Context, string) (*setup.ActionsPolicy, error) {
					if noPolicy {
						return nil
					}
					return ghActionsPolicy
				}(),
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "setup scaffold-tooling: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printScaffoldToolingHuman(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: current working directory)")
	cmd.Flags().StringVar(&selectStr, "select", "", "Comma-list of templates to emit (tsconfig,vitest,eslint,prettier,ci); empty = all")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report intended outcomes without writing files")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	cmd.Flags().StringVar(&runsOn, "runs-on", "", "Runner label for ci.yml runs-on (default ubuntu-latest), e.g. self-hosted")
	cmd.Flags().BoolVar(&noPolicy, "no-policy-check", false, "Skip reading the repository's GitHub Actions policy")
	return cmd
}

// parseSelect splits a comma-list, trimming whitespace and dropping empty
// segments. Returns nil for empty input so RunScaffoldTooling expands to all
// keys.
func parseSelect(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ghActionsPolicy reads the Actions policy of the repository at workdir via
// the gh CLI. Any failure (gh missing, no GitHub remote, token without admin
// read) is returned as an error, which the verb turns into a warning.
func ghActionsPolicy(ctx context.Context, workdir string) (*setup.ActionsPolicy, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	run := func(args ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "gh", args...)
		c.Dir = workdir
		out, err := c.Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return nil, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
			}
			return nil, err
		}
		return out, nil
	}
	slugOut, err := run("repo", "view", "--json", "nameWithOwner", "--jq", ".nameWithOwner")
	if err != nil {
		return nil, fmt.Errorf("resolve repository: %w", err)
	}
	slug := strings.TrimSpace(string(slugOut))
	permOut, err := run("api", "repos/"+slug+"/actions/permissions")
	if err != nil {
		return nil, fmt.Errorf("read %s actions permissions: %w", slug, err)
	}
	var perm struct {
		Enabled            bool   `json:"enabled"`
		AllowedActions     string `json:"allowed_actions"`
		SHAPinningRequired bool   `json:"sha_pinning_required"`
	}
	if err := json.Unmarshal(permOut, &perm); err != nil {
		return nil, fmt.Errorf("parse actions permissions: %w", err)
	}
	p := &setup.ActionsPolicy{Enabled: perm.Enabled, AllowedActions: perm.AllowedActions, SHAPinningRequired: perm.SHAPinningRequired}
	if perm.AllowedActions == "selected" {
		if selOut, selErr := run("api", "repos/"+slug+"/actions/permissions/selected-actions"); selErr == nil {
			var sel struct {
				GithubOwnedAllowed bool     `json:"github_owned_allowed"`
				PatternsAllowed    []string `json:"patterns_allowed"`
			}
			if json.Unmarshal(selOut, &sel) == nil {
				p.SelectedKnown = true
				p.GithubOwnedAllowed = sel.GithubOwnedAllowed
				p.PatternsAllowed = sel.PatternsAllowed
			}
		}
	}
	return p, nil
}

func printScaffoldToolingHuman(r *setup.ScaffoldToolingResult) {
	fmt.Printf("nightgauge setup scaffold-tooling — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n", r.Workdir)
	fmt.Printf("selected: %s\n", strings.Join(r.Selected, ","))
	fmt.Printf("detected: package.json=%t node=%s ts=%t vitest=%t eslint=%t prettier=%t\n",
		r.Detected.PackageJSONFound,
		r.Detected.NodeVersion,
		r.Detected.HasTypeScript,
		r.Detected.HasVitest,
		r.Detected.HasESLint,
		r.Detected.HasPrettier,
	)
	for _, o := range r.Outcomes {
		switch o.Outcome {
		case setup.OutcomeCreated:
			if o.Reason == "dry-run" {
				fmt.Printf("  + would create %s (%d bytes, dry-run)\n", o.Path, o.Bytes)
			} else {
				fmt.Printf("  + created %s (%d bytes)\n", o.Path, o.Bytes)
			}
		case setup.OutcomeSkippedExisting:
			fmt.Printf("  ✓ %s already exists — skipping\n", o.Path)
		case setup.OutcomeSkippedMissingDep:
			fmt.Printf("  ⚠ %s skipped — %s\n", o.Path, o.Reason)
		case setup.OutcomeSkippedDisabled:
			fmt.Printf("  · %s skipped — not selected\n", o.Path)
		case setup.OutcomeError:
			fmt.Printf("  ✗ %s error — %s\n", o.Path, o.Reason)
		}
	}
	if r.RunsOn != "" {
		fmt.Printf("ci: runs-on=%s steps=%s\n", r.RunsOn, strings.Join(r.CISteps, ","))
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}
