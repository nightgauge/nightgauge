package main

import (
	"encoding/json"
	"fmt"
	"os"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/sizeGate"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// sizeGateCmd returns the top-level "size-gate" command.
func sizeGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "size-gate",
		Short: "Issue size gate preflight checks",
	}
	cmd.AddCommand(sizeGateCheckCmd())
	return cmd
}

// sizeGateCheckCmd evaluates whether an issue passes the size gate.
// Exit codes:
//
//	0 — issue passes (PASSED)
//	1 — issue rejected (REJECTED or error)
func sizeGateCheckCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		issueNum   int
		configPath string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Check whether an issue passes the pipeline size gate",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNum <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			// Load gate config from YAML, falling back to defaults when absent.
			cfg := loadSizeGateConfigFromYAML(configPath)

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			svc := gh.NewIssueService(client)
			issue, err := svc.GetIssue(cmd.Context(), ownerPart, repoPart, issueNum)
			if err != nil {
				return fmt.Errorf("fetch issue #%d: %w", issueNum, enrichError(err))
			}

			evaluator := sizeGate.NewGateEvaluator(cfg)
			result := evaluator.Evaluate(issue.Title, issue.Labels, len(issue.SubIssues))

			if outputJSON {
				type jsonResult struct {
					Allowed           bool     `json:"allowed"`
					Reason            string   `json:"reason,omitempty"`
					Severity          string   `json:"severity,omitempty"`
					SuggestedAction   string   `json:"suggested_action,omitempty"`
					HeuristicsApplied []string `json:"heuristics_applied"`
				}
				out := jsonResult{
					Allowed:           result.Allowed,
					Reason:            result.Reason,
					Severity:          result.Severity,
					SuggestedAction:   result.SuggestedAction,
					HeuristicsApplied: result.HeuristicsApplied,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if result.Allowed {
				fmt.Printf("Size gate: PASSED\n")
				fmt.Printf("Issue #%d: %q\n", issue.Number, issue.Title)
				return nil
			}

			fmt.Fprintf(os.Stderr, "Size gate: REJECTED\n")
			fmt.Fprintf(os.Stderr, "Issue #%d: %q\n", issue.Number, issue.Title)
			fmt.Fprintf(os.Stderr, "Reason: %s\n", result.Reason)
			fmt.Fprintf(os.Stderr, "Suggested action: %s\n", result.SuggestedAction)
			if len(result.HeuristicsApplied) > 0 {
				fmt.Fprintf(os.Stderr, "Heuristics triggered: %v\n", result.HeuristicsApplied)
			}
			return fmt.Errorf("issue too large: %s", result.Reason)
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number to evaluate (required)")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("issue")

	return cmd
}

// sizeGateYAML is the YAML shape for pipeline.size_gate config section.
type sizeGateYAML struct {
	Pipeline struct {
		SizeGate struct {
			Enabled           *bool `yaml:"enabled"`
			RejectOnOversized *bool `yaml:"reject_on_oversized"`
			Thresholds        struct {
				MaxLocInTitle      *int `yaml:"max_loc_in_title"`
				DecomposedItemsMin *int `yaml:"decomposed_items_min"`
			} `yaml:"thresholds"`
			Heuristics struct {
				LocPatternEnabled         *bool `yaml:"loc_pattern_enabled"`
				DecompositionCheckEnabled *bool `yaml:"decomposition_check_enabled"`
			} `yaml:"heuristics"`
		} `yaml:"size_gate"`
	} `yaml:"pipeline"`
}

// loadSizeGateConfigFromYAML reads pipeline.size_gate from the YAML config file,
// applying defaults for any missing fields. When the file is absent or cannot be
// parsed, all defaults are used — the gate is never disabled by a missing config.
func loadSizeGateConfigFromYAML(configPath string) sizeGate.GateConfig {
	cfg := sizeGate.DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg // config absent — use defaults
	}

	var y sizeGateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg // parse error — use defaults
	}

	sg := y.Pipeline.SizeGate

	// Respect explicit disable: if enabled is explicitly false, disable all heuristics.
	if sg.Enabled != nil && !*sg.Enabled {
		cfg.LocPatternEnabled = false
		cfg.DecompositionCheckEnabled = false
		return cfg
	}

	if sg.RejectOnOversized != nil {
		cfg.RejectOnOversized = *sg.RejectOnOversized
	}
	if sg.Thresholds.MaxLocInTitle != nil {
		cfg.MaxLocInTitle = *sg.Thresholds.MaxLocInTitle
	}
	if sg.Thresholds.DecomposedItemsMin != nil {
		cfg.DecomposedItemsMin = *sg.Thresholds.DecomposedItemsMin
	}
	if sg.Heuristics.LocPatternEnabled != nil {
		cfg.LocPatternEnabled = *sg.Heuristics.LocPatternEnabled
	}
	if sg.Heuristics.DecompositionCheckEnabled != nil {
		cfg.DecompositionCheckEnabled = *sg.Heuristics.DecompositionCheckEnabled
	}

	return cfg
}
