package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/sizeGate"
	"github.com/nightgauge/nightgauge/internal/orchestrator"
	"github.com/nightgauge/nightgauge/pkg/types"
	"github.com/spf13/cobra"
)

// sizeGateCmd returns the top-level "size-gate" command.
func sizeGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "size-gate",
		Short: "Issue size gate preflight checks",
	}
	cmd.AddCommand(sizeGateCheckCmd())
	cmd.AddCommand(sizeGateCapacityCmd())
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
		window     int
		adapter    string
		model      string
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
			cfg := sizeGate.LoadGateConfigFromYAML(configPath)

			// The capacity check (#1655) runs only when a window, or the
			// model that resolves one, is named; without these flags the
			// gate is exactly what it was before.
			var capacity *sizeGate.CapacityInput
			if window < 0 {
				return fmt.Errorf("--context-window must not be negative")
			}
			if err := requireAdapterModelPair(adapter, model); err != nil {
				return err
			}
			if window > 0 || model != "" {
				capacity = resolveCapacityInput(cmd.Context(), cfg, window, adapter, model)
			}

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

			issue, result, err := evaluateSizeGate(cmd.Context(), gh.NewIssueService(client), ownerPart, repoPart, issueNum, cfg, capacity)
			if err != nil {
				return fmt.Errorf("fetch issue #%d: %w", issueNum, enrichError(err))
			}

			if outputJSON {
				type jsonResult struct {
					Allowed           bool     `json:"allowed"`
					Reason            string   `json:"reason,omitempty"`
					Severity          string   `json:"severity,omitempty"`
					SuggestedAction   string   `json:"suggested_action,omitempty"`
					HeuristicsApplied []string `json:"heuristics_applied"`
					ContextWindow     int      `json:"context_window,omitempty"`
					MaxSize           string   `json:"max_size,omitempty"`
					Recovery          string   `json:"recovery,omitempty"`
					RoutedModel       string   `json:"routed_model,omitempty"`
				}
				out := jsonResult{
					Allowed:           result.Allowed,
					Reason:            result.Reason,
					Severity:          result.Severity,
					SuggestedAction:   result.SuggestedAction,
					HeuristicsApplied: result.HeuristicsApplied,
					RoutedModel:       result.RoutedModel,
				}
				if result.Capacity != nil {
					out.ContextWindow = result.Capacity.Window
					out.MaxSize = result.Capacity.MaxSize
					out.Recovery = result.Capacity.Recovery
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if result.Allowed {
				fmt.Printf("Size gate: PASSED\n")
				fmt.Printf("Issue #%d: %q\n", issue.Number, issue.Title)
				if result.RoutedModel != "" {
					fmt.Printf("Soft-routed to: %s\n", result.RoutedModel)
				}
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
	cmd.Flags().IntVar(&window, "context-window", 0, "Context window (tokens) of the model that will run the issue; enables the capacity check")
	cmd.Flags().StringVar(&adapter, "adapter", "", "Adapter the issue will run on (with --model, resolves the context window)")
	cmd.Flags().StringVar(&model, "model", "", "Model the issue will run on; enables the capacity check with the model's resolved window")
	_ = cmd.MarkFlagRequired("issue")

	return cmd
}

// evaluateSizeGate reads an issue and runs the size gate over it. The gate
// judges the issue's title, labels and number of sub-issues, so the read
// follows the sub-issue list to its end and no other list. A long blocking or
// blocked-by list must not fail the read: the issue-pickup skill records any
// failure of `size-gate check` as the issue being too large.
//
// capacity, when non-nil, adds the capacity check (#1655) against the size:*
// labels; its one log line goes to stderr.
func evaluateSizeGate(ctx context.Context, issues issueReader, owner, repo string, number int, cfg sizeGate.GateConfig, capacity *sizeGate.CapacityInput) (*types.Issue, *sizeGate.GateResult, error) {
	issue, err := issues.GetIssueWithRelations(ctx, owner, repo, number, gh.RelationSubIssues)
	if err != nil {
		return nil, nil, err
	}
	result := sizeGate.NewGateEvaluator(cfg).WithLogger(stderrLogf).EvaluateIssue(sizeGate.GateInput{
		Title:     issue.Title,
		Labels:    issue.Labels,
		SubIssues: len(issue.SubIssues),
		Body:      issue.Body,
		Capacity:  capacity,
	})
	return issue, result, nil
}

// stderrLogf writes one line to stderr, keeping stdout for the verdict and
// the --json document.
func stderrLogf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// resolveCapacityInput builds the capacity check's model side: an explicit
// --context-window wins, else the window adapter/model dispatches with
// (orchestrator.DispatchContextWindow, the scheduler's own resolution). The
// soft-route fallbacks are resolved on the same adapter.
func resolveCapacityInput(ctx context.Context, cfg sizeGate.GateConfig, window int, adapter, model string) *sizeGate.CapacityInput {
	root := capacityWorkspaceRoot()
	in := &sizeGate.CapacityInput{Window: window}
	if in.Window <= 0 && model != "" {
		in.Window = orchestrator.DispatchContextWindow(ctx, root, adapter, model)
	}
	if cfg.SoftRoute {
		for _, m := range cfg.CapacityFallbackModels {
			w := orchestrator.DispatchContextWindow(ctx, root, adapter, m)
			if w <= 0 {
				stderrLogf("capacity: fallback %s skipped: its context window did not resolve on adapter %q", m, adapter)
				continue
			}
			in.Fallbacks = append(in.Fallbacks, sizeGate.CapacityCandidate{Model: m, Window: w})
		}
	}
	return in
}

// requireAdapterModelPair refuses --adapter without --model and the reverse:
// either alone names no model whose window could be resolved, and silently
// ignoring it would run the gate without the capacity check the caller asked for.
func requireAdapterModelPair(adapter, model string) error {
	if (adapter == "") != (model == "") {
		return fmt.Errorf("--adapter and --model must be given together")
	}
	return nil
}

// capacityWorkspaceRoot is the directory whose .nightgauge config describes
// the models: the working directory.
func capacityWorkspaceRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// sizeGateCapacityCmd reports the largest issue size a model may take — the
// ADR 023 capacity table read for one model's context window. issue-create's
// scope gate calls it to force decomposition of work above the cap (#1655).
//
// The model is --context-window, or --adapter/--model, or — with neither —
// the repository's configured target: the adapter feature-dev resolves to and,
// for opencode, the machine-tier opencode.model, otherwise
// pipeline.stage_models.feature-dev. An unknown window reports no cap
// (max_size "") and logs one line saying so.
func sizeGateCapacityCmd() *cobra.Command {
	var (
		window     int
		adapter    string
		model      string
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:          "capacity",
		Short:        "Report the largest issue size a model's context window admits",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if window < 0 {
				return fmt.Errorf("--context-window must not be negative")
			}
			if err := requireAdapterModelPair(adapter, model); err != nil {
				return err
			}
			root := capacityWorkspaceRoot()
			if window == 0 && adapter == "" && model == "" {
				adapter, model = configuredCapacityTarget(root)
			}
			if window == 0 && model != "" {
				window = orchestrator.DispatchContextWindow(cmd.Context(), root, adapter, model)
			}
			maxSize, known := sizeGate.MaxSizeForWindow(window)
			if !known {
				stderrLogf("capacity: window unknown — no capacity cap applied (adapter %q, model %q)", adapter, model)
			}
			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(struct {
					Adapter       string `json:"adapter,omitempty"`
					Model         string `json:"model,omitempty"`
					ContextWindow int    `json:"context_window"`
					WindowKnown   bool   `json:"window_known"`
					MaxSize       string `json:"max_size"`
				}{adapter, model, window, known, maxSize})
			}
			if !known {
				fmt.Println("Capacity: no cap (context window unknown)")
				return nil
			}
			fmt.Printf("Capacity: up to size %s (context window %d)\n", maxSize, window)
			return nil
		},
	}
	cmd.Flags().IntVar(&window, "context-window", 0, "Context window in tokens (wins over --adapter/--model)")
	cmd.Flags().StringVar(&adapter, "adapter", "", "Adapter the model runs on")
	cmd.Flags().StringVar(&model, "model", "", "Model whose context window decides the cap")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

// configuredCapacityTarget is the repository's configured target model for
// capacity purposes: the adapter feature-dev resolves to and, on opencode,
// the machine-tier opencode.model, else pipeline.stage_models.feature-dev.
// Either value may be "" when nothing is configured.
func configuredCapacityTarget(root string) (adapter, model string) {
	cfg, err := config.Load(root)
	if err != nil {
		cfg = nil
	}
	adapter = config.ResolveStageAdapter(cfg, "feature-dev", os.Getenv).Adapter
	if adapter == "opencode" {
		if oc, err := config.LoadOpenCodeConfig(root); err == nil {
			model = oc.Model
		}
		return adapter, model
	}
	if cfg != nil && cfg.Pipeline != nil {
		model = cfg.Pipeline.StageModels["feature-dev"]
	}
	return adapter, model
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
	issue, err := gh.NewIssueService(client).GetIssueWithRelations(ctx, owner, repo, issueNum, gh.NoRelations)
	if err != nil {
		return nil, fmt.Errorf("fetch issue #%d: %w", issueNum, enrichError(err))
	}
	return issue.Labels, nil
}
