package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

			// Fail before the client is built and before any network call: an
			// empty owner or repo would be stitched into a malformed "owner/"
			// slug and surface as an opaque "Could not resolve to a
			// Repository with the name 'owner/'" GitHub error (#536).
			ownerPart, repoPart, err := resolveGateRepo(owner, repo)
			if err != nil {
				return err
			}

			client, err := clientFromConfig()
			if err != nil {
				return fmt.Errorf("create GitHub client: %w", err)
			}

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
	repoNameFlag(cmd, &repo, "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number to evaluate (required)")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("issue")

	return cmd
}

// repoBackfillConfigPath returns the project-tier config.yaml path that
// config.Load consults when PersistentPreRunE back-fills --owner/--repo. It is
// named in the guard's error so the operator knows WHICH file was read: the
// back-fill resolves against os.Getwd(), which in a worktree or a multi-repo
// workspace is frequently not the file the operator just edited. It also makes
// the swallowed-config-error path discoverable — rootCmd's PersistentPreRunE
// silently ignores config.Load failures, so malformed YAML in this exact file
// presents as "nothing was configured".
func repoBackfillConfigPath() string {
	wd, err := os.Getwd()
	if err != nil {
		return filepath.Join(".nightgauge", "config.yaml")
	}
	return filepath.Join(wd, ".nightgauge", "config.yaml")
}

// resolveGateRepo normalizes --owner/--repo into the well-formed owner/name
// pair handed to the GitHub API, or returns an actionable error (#536).
//
// It is the single point where the size gate decides the target repository, so
// the values it RETURNS are the values the caller forwards — a check that
// trimmed for its own comparison and then forwarded the untrimmed input would
// let `--repo '  name '` reach GitHub and reproduce the very opaque
// "Could not resolve to a Repository" error this guard exists to eliminate.
//
// The error messages name the config keys that are actually honored. Both
// parsers must be described because which one runs depends on the file's shape:
// a `project:` MAPPING selects the nested parser (project.repo → repo →
// github.repo), anything else selects the flat parser (defaultRepo →
// github.repo). Flat configs never read a top-level `repo:` at all, so naming
// only `repo:` would tell an operator to do what they had already done.
func resolveGateRepo(owner, repo string) (string, string, error) {
	// Validate the RAW --repo before splitting. splitRepo("acme", "/") returns
	// ("", ""), which would otherwise be reported as a missing OWNER while
	// `owner: acme` sits in the operator's config — sending them to fix a
	// setting that is not broken.
	if trimmed := strings.TrimSpace(repo); strings.Contains(trimmed, "/") {
		parts := strings.Split(trimmed, "/")
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", "", fmt.Errorf("malformed --repo %q: expected \"name\" or \"owner/name\"", repo)
		}
	}

	ownerPart, repoPart := splitRepo(owner, repo)
	ownerPart, repoPart = strings.TrimSpace(ownerPart), strings.TrimSpace(repoPart)

	consulted := repoBackfillConfigPath()
	if ownerPart == "" {
		return "", "", fmt.Errorf("owner not configured (repo=%q, consulted %s): pass --owner, or set project.owner / owner (nested config) or owner / github.owner (flat config)", repoPart, consulted)
	}
	if repoPart == "" {
		return "", "", fmt.Errorf("repo not configured (owner=%q, consulted %s): pass --repo, or set project.repo / repo (nested config) or defaultRepo / github.repo (flat config)", ownerPart, consulted)
	}
	return ownerPart, repoPart, nil
}

// fetchGateIssueLabels fetches an issue's labels through the config-resolved
// GitHub client. It is the one seam the label-reading gates (scope-drift,
// version-downgrade) share, and a package-level variable so tests can record
// the exact owner/name slug a gate hands to the forge without a network.
var fetchGateIssueLabels = func(ctx context.Context, owner, repo string, issueNum int) ([]string, error) {
	client, err := clientFromConfig()
	if err != nil {
		return nil, fmt.Errorf("create GitHub client: %w", err)
	}
	issue, err := gh.NewIssueService(client).GetIssue(ctx, owner, repo, issueNum)
	if err != nil {
		return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, enrichError(err))
	}
	return issue.Labels, nil
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
