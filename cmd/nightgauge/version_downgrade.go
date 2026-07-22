package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/versionDowngradeGate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// versionDowngradeCmd is the top-level "preflight version-downgrade" command.
// It exposes a single "check" subcommand that compares the working tree's
// tsconfig*.json and package.json against the merge-base on a baseline branch
// (default: main) and fails when any version moves backward.
//
// See Issue #3042.
func versionDowngradeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version-downgrade",
		Short: "Version downgrade preflight gate (tsconfig, dependencies, engines)",
		Long: `Detects regressions in TypeScript compilerOptions.target/lib, npm dependency
range minimums, and engines.node when a feature branch's tsconfig*.json or
package.json files lower a version relative to the baseline branch.

Bypassed when the issue carries the configured bypass label (default
"version:downgrade-allowed") or when dev-{N}.json sets allow_downgrade=true.`,
	}
	cmd.AddCommand(versionDowngradeCheckCmd())
	return cmd
}

// versionDowngradeCheckCmd runs the gate. Exit codes:
//
//	0 — pass (no downgrades, warn-mode drift, gate disabled, or bypassed)
//	1 — strict-mode block (downgrades detected, enforcement_mode == "strict")
//	2 — config or IO error
func versionDowngradeCheckCmd() *cobra.Command {
	var (
		owner          string
		repo           string
		issueNum       int
		baselineBranch string
		configPath     string
		workdir        string
		outputJSON     bool
		allowOverride  bool
	)

	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Check whether the feature branch downgrades any tracked versions",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := loadVersionDowngradeGateConfigFromYAML(configPath)

			work := workdir
			if work == "" {
				if wd, err := os.Getwd(); err == nil {
					work = wd
				}
			}

			// Read dev-{N}.json for allow_downgrade if --issue is provided.
			// Skip the read entirely when no issue was supplied (CLI-direct usage).
			allowFlag := allowOverride
			if !allowFlag && issueNum > 0 {
				if v, err := readAllowDowngradeFlag(work, issueNum); err == nil {
					allowFlag = v
				}
			}

			// Discover labels for bypass check (best-effort).
			var labels []string
			if issueNum > 0 {
				if client, err := clientFromConfig(); err == nil {
					ownerPart, repoPart := splitRepo(owner, repo)
					svc := gh.NewIssueService(client)
					if issue, gerr := svc.GetIssue(cmd.Context(), ownerPart, repoPart, issueNum); gerr == nil {
						labels = issue.Labels
					}
				}
			}

			// Load current and baseline file snapshots from disk + git.
			input, err := buildEvaluateInput(work, baselineBranch, labels, allowFlag)
			if err != nil {
				return fmt.Errorf("build evaluate input: %w", err)
			}

			result := versionDowngradeGate.NewEvaluator(cfg).Evaluate(input)

			status := "passed"
			if !result.Allowed {
				status = "failed"
			}
			if !cfg.Enabled {
				status = "skipped"
			}

			if outputJSON {
				out := versionDowngradeJSONResult{
					IssueNumber:     issueNum,
					Status:          status,
					Allowed:         result.Allowed,
					Bypassed:        result.Bypassed,
					Reason:          result.Reason,
					EnforcementMode: result.EnforcementMode,
					Downgrades:      result.Downgrades,
					SuggestedAction: result.SuggestedAction,
				}
				if err := printJSON(out); err != nil {
					return err
				}
			} else {
				renderVersionDowngradeHuman(result, status, issueNum)
			}

			if !result.Allowed {
				return fmt.Errorf("version downgrade detected: %s", result.Reason)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number for label bypass + dev context lookup (optional)")
	cmd.Flags().StringVar(&baselineBranch, "baseline", "main", "Baseline branch to compare against")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (defaults to cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&allowOverride, "allow-override", false, "Force-bypass the gate (equivalent to allow_downgrade=true)")
	return cmd
}

// versionDowngradeJSONResult is the JSON shape emitted by `version-downgrade check --json`.
type versionDowngradeJSONResult struct {
	IssueNumber     int                                     `json:"issue_number,omitempty"`
	Status          string                                  `json:"status"` // "passed" | "failed" | "skipped"
	Allowed         bool                                    `json:"allowed"`
	Bypassed        bool                                    `json:"bypassed,omitempty"`
	Reason          string                                  `json:"reason,omitempty"`
	EnforcementMode string                                  `json:"enforcement_mode,omitempty"`
	Downgrades      []versionDowngradeGate.VersionDowngrade `json:"downgrades,omitempty"`
	SuggestedAction string                                  `json:"suggested_action,omitempty"`
}

// readAllowDowngradeFlag reads dev-{N}.json and returns the allow_downgrade
// flag. When the file or field is absent, returns (false, nil) — the gate
// proceeds without bypass.
func readAllowDowngradeFlag(workdir string, issueNum int) (bool, error) {
	devPath := filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("dev-%d.json", issueNum))
	data, err := os.ReadFile(devPath)
	if err != nil {
		return false, err
	}
	var ctx struct {
		AllowDowngrade bool `json:"allow_downgrade"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return false, err
	}
	return ctx.AllowDowngrade, nil
}

// buildEvaluateInput collects baseline + current snapshots for tsconfig*.json
// and package.json. Baseline values come from `git show {branch}:{path}`;
// missing baseline files are treated as "no comparison" (the file is new).
func buildEvaluateInput(workdir, baseline string, labels []string, allowFlag bool) (versionDowngradeGate.EvaluateInput, error) {
	in := versionDowngradeGate.EvaluateInput{
		BaselineTSConfigs:  map[string][]byte{},
		CurrentTSConfigs:   map[string][]byte{},
		IssueLabels:        labels,
		AllowDowngradeFlag: allowFlag,
	}

	// tsconfig*.json — current directory only, non-recursive.
	tsconfigs, err := filepath.Glob(filepath.Join(workdir, "tsconfig*.json"))
	if err != nil {
		return in, fmt.Errorf("glob tsconfig*.json: %w", err)
	}
	for _, path := range tsconfigs {
		rel, _ := filepath.Rel(workdir, path)
		bytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		in.CurrentTSConfigs[rel] = bytes
		if base, ok := gitShow(workdir, baseline, rel); ok {
			in.BaselineTSConfigs[rel] = base
		}
	}

	// package.json — repo root only.
	pkgPath := filepath.Join(workdir, "package.json")
	if data, err := os.ReadFile(pkgPath); err == nil {
		in.CurrentPackageJSON = data
		in.PackageJSONPath = "package.json"
		if base, ok := gitShow(workdir, baseline, "package.json"); ok {
			in.BaselinePackageJSON = base
		}
	}

	return in, nil
}

// gitShow returns the bytes of `git show {branch}:{path}` from inside workdir,
// or (nil, false) if the file is absent on that branch.
func gitShow(workdir, branch, path string) ([]byte, bool) {
	cmd := exec.Command("git", "-C", workdir, "show", branch+":"+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	return out, true
}

func renderVersionDowngradeHuman(result *versionDowngradeGate.GateResult, status string, issueNum int) {
	header := func(suffix string) {
		if issueNum > 0 {
			fmt.Printf("Version downgrade gate: %s (issue #%d)\n", suffix, issueNum)
		} else {
			fmt.Printf("Version downgrade gate: %s\n", suffix)
		}
	}
	switch status {
	case "passed":
		if result.Bypassed {
			header("BYPASSED")
		} else {
			header("PASSED")
		}
		fmt.Printf("  %s\n", result.Reason)
		if len(result.Downgrades) > 0 {
			fmt.Printf("  Downgrades (warn-only): %d\n", len(result.Downgrades))
			for _, d := range result.Downgrades {
				fmt.Printf("    - %s %s: %s -> %s\n", d.File, d.Field, d.OldValue, d.NewValue)
			}
		}
	case "skipped":
		header("SKIPPED")
		fmt.Printf("  %s\n", result.Reason)
	case "failed":
		fmt.Fprintln(os.Stderr, "Version downgrade gate: BLOCKED (strict mode)")
		fmt.Fprintf(os.Stderr, "  %s\n", result.Reason)
		fmt.Fprintf(os.Stderr, "  Downgrades (%d):\n", len(result.Downgrades))
		for _, d := range result.Downgrades {
			fmt.Fprintf(os.Stderr, "    - %s %s: %s -> %s\n", d.File, d.Field, d.OldValue, d.NewValue)
		}
		if result.SuggestedAction != "" {
			fmt.Fprintf(os.Stderr, "  Suggested action: %s\n", result.SuggestedAction)
		}
	}
}

// versionDowngradeGateYAML mirrors the pipeline.version_downgrade_gate config section.
type versionDowngradeGateYAML struct {
	Pipeline struct {
		VersionDowngradeGate struct {
			Enabled         *bool   `yaml:"enabled"`
			EnforcementMode *string `yaml:"enforcement_mode"`
			BypassLabel     *string `yaml:"bypass_label"`
		} `yaml:"version_downgrade_gate"`
	} `yaml:"pipeline"`
}

// loadVersionDowngradeGateConfigFromYAML reads pipeline.version_downgrade_gate
// from the YAML config, applying defaults for missing fields. Same convention
// as loadScopeDriftGateConfigFromYAML.
func loadVersionDowngradeGateConfigFromYAML(configPath string) versionDowngradeGate.GateConfig {
	cfg := versionDowngradeGate.DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg
	}
	var y versionDowngradeGateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg
	}
	v := y.Pipeline.VersionDowngradeGate
	if v.Enabled != nil {
		cfg.Enabled = *v.Enabled
	}
	if v.EnforcementMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*v.EnforcementMode))
		if mode == versionDowngradeGate.EnforcementWarn || mode == versionDowngradeGate.EnforcementStrict {
			cfg.EnforcementMode = mode
		}
	}
	if v.BypassLabel != nil {
		cfg.BypassLabel = *v.BypassLabel
	}
	return cfg
}
