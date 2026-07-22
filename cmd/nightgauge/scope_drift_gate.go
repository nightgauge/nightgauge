package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/scopeDriftGate"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// scopeDriftGateCmd is the top-level "scope-drift" command. It exposes a
// single "check" subcommand that evaluates a feature branch's modified files
// against a per-issue-type allowlist. Used in pr-create Phase 2.6 to prevent
// type:docs / type:chore PRs from silently shipping out-of-scope code changes.
//
// See Issue #3040.
func scopeDriftGateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope-drift",
		Short: "Scope drift gate for type:docs and type:chore issues",
		Long: `Verifies that files modified for a type:docs or type:chore issue fall
within a configured allowlist. Out-of-scope modifications indicate scope drift
— typically caused by stale worktrees reverting recently-merged work.`,
	}
	cmd.AddCommand(scopeDriftGateCheckCmd())
	return cmd
}

// scopeDriftGateCheckCmd evaluates dev-{N}.json.files_changed against the
// scope-drift allowlist. Exit codes:
//
//	0 — pass (in scope, drift in warn mode, gate disabled, or non-docs/chore type)
//	1 — strict-mode block (drift detected and enforcement_mode == "strict")
//	2 — config or IO error
func scopeDriftGateCheckCmd() *cobra.Command {
	var (
		owner             string
		repo              string
		issueNum          int
		configPath        string
		workdir           string
		outputJSON        bool
		issueTypeOverride string
	)

	cmd := &cobra.Command{
		Use:          "check",
		Short:        "Check whether the feature branch's modified files match the scope-drift allowlist",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNum <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			cfg := loadScopeDriftGateConfigFromYAML(configPath)

			// Resolve workdir for context file lookup.
			work := workdir
			if work == "" {
				if wd, err := os.Getwd(); err == nil {
					work = wd
				}
			}

			// Read dev-{N}.json for files_changed. The gate is no-op if the
			// file is missing — pre-PR creation is the right time to gate, but
			// the gate must not block when feature-dev never ran.
			changedFiles, devReadErr := readDevFilesChanged(work, issueNum)
			if devReadErr != nil {
				if outputJSON {
					return printJSON(scopeDriftJSONResult{
						IssueNumber: issueNum,
						Status:      "skipped",
						Reason:      fmt.Sprintf("dev-%d.json unavailable: %v", issueNum, devReadErr),
					})
				}
				fmt.Printf("Scope drift gate: SKIPPED (dev context unavailable: %v)\n", devReadErr)
				return nil
			}

			// Resolve issue type and labels. issueTypeOverride lets the caller
			// skip GitHub API calls when type is already known (e.g., from
			// issue-{N}.json in the pr-create skill).
			var labels []string
			issueType := strings.ToLower(strings.TrimSpace(issueTypeOverride))
			if issueType == "" {
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
				labels = issue.Labels
				issueType = inferIssueType(labels)
			} else {
				// Even when type is provided, fetch labels for the bypass check.
				if client, err := clientFromConfig(); err == nil {
					ownerPart, repoPart := splitRepo(owner, repo)
					svc := gh.NewIssueService(client)
					if issue, gerr := svc.GetIssue(cmd.Context(), ownerPart, repoPart, issueNum); gerr == nil {
						labels = issue.Labels
					}
				}
			}

			evaluator := scopeDriftGate.NewGateEvaluator(cfg)
			result := evaluator.Evaluate(issueType, labels, changedFiles)

			// Telemetry + audit counter — fire-and-forget on actual drift only.
			if len(result.DriftedFiles) > 0 && !result.Bypassed {
				emitScopeDriftTelemetry(cmd.Context(), issueNum, result)
				if err := appendScopeDriftAudit(work, issueNum, result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: scope-drift audit append failed: %v\n", err)
				}
			}

			status := "passed"
			if !result.Allowed {
				status = "failed"
			}
			if !cfg.Enabled || (issueType != scopeDriftGate.IssueTypeDocs && issueType != scopeDriftGate.IssueTypeChore) {
				status = "skipped"
			}

			if outputJSON {
				out := scopeDriftJSONResult{
					IssueNumber:       issueNum,
					Status:            status,
					Allowed:           result.Allowed,
					Bypassed:          result.Bypassed,
					Reason:            result.Reason,
					IssueType:         result.IssueType,
					EnforcementMode:   result.EnforcementMode,
					DriftedFiles:      result.DriftedFiles,
					AllowedFiles:      result.AllowedFiles,
					SuggestedAction:   result.SuggestedAction,
					HeuristicsApplied: result.HeuristicsApplied,
				}
				if err := printJSON(out); err != nil {
					return err
				}
			} else {
				renderScopeDriftHuman(result, status, issueNum)
			}

			if !result.Allowed {
				return fmt.Errorf("scope drift detected: %s", result.Reason)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&issueNum, "issue", 0, "GitHub issue number to evaluate (required)")
	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config.yaml")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (defaults to cwd) for locating dev-{N}.json")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&issueTypeOverride, "issue-type", "", "Override inferred issue type (docs|chore|...) — skips type label lookup")
	_ = cmd.MarkFlagRequired("issue")
	return cmd
}

// scopeDriftJSONResult is the JSON shape emitted by `scope-drift check --json`.
type scopeDriftJSONResult struct {
	IssueNumber       int      `json:"issue_number"`
	Status            string   `json:"status"` // "passed" | "failed" | "skipped"
	Allowed           bool     `json:"allowed"`
	Bypassed          bool     `json:"bypassed,omitempty"`
	Reason            string   `json:"reason,omitempty"`
	IssueType         string   `json:"issue_type,omitempty"`
	EnforcementMode   string   `json:"enforcement_mode,omitempty"`
	DriftedFiles      []string `json:"drifted_files,omitempty"`
	AllowedFiles      []string `json:"allowed_files,omitempty"`
	SuggestedAction   string   `json:"suggested_action,omitempty"`
	HeuristicsApplied []string `json:"heuristics_applied,omitempty"`
}

// inferIssueType extracts "docs" / "chore" from a labels slice. Returns "" for
// other types — the gate will then no-op.
func inferIssueType(labels []string) string {
	for _, l := range labels {
		switch l {
		case "type:docs":
			return scopeDriftGate.IssueTypeDocs
		case "type:chore":
			return scopeDriftGate.IssueTypeChore
		}
	}
	return ""
}

// readDevFilesChanged reads dev-{N}.json from the pipeline directory and returns
// the union of created+modified files (deletions are omitted — always allowed).
func readDevFilesChanged(workdir string, issueNum int) ([]string, error) {
	devPath := filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("dev-%d.json", issueNum))
	data, err := os.ReadFile(devPath)
	if err != nil {
		return nil, err
	}
	var ctx struct {
		FilesChanged struct {
			Created  []string `json:"created"`
			Modified []string `json:"modified"`
		} `json:"files_changed"`
	}
	if err := json.Unmarshal(data, &ctx); err != nil {
		return nil, fmt.Errorf("parse %s: %w", devPath, err)
	}
	out := make([]string, 0, len(ctx.FilesChanged.Created)+len(ctx.FilesChanged.Modified))
	out = append(out, ctx.FilesChanged.Created...)
	out = append(out, ctx.FilesChanged.Modified...)
	return out, nil
}

// emitScopeDriftTelemetry sends a fire-and-forget pipeline event when drift
// is detected. Best-effort — silently no-ops when no platform is configured.
func emitScopeDriftTelemetry(ctx context.Context, issueNum int, result *scopeDriftGate.GateResult) {
	pcfg := platform.DefaultConfig()
	if pcfg.BaseURL == "" {
		return
	}
	client, err := platform.NewClient(pcfg)
	if err != nil {
		return
	}
	svc := platform.NewTelemetryService(client)
	svc.EmitPipelineEvent(ctx, platform.PipelineEvent{
		IssueNumber: issueNum,
		EventType:   "scope_drift_detected",
		Stage:       "pr-create",
		Timestamp:   time.Now(),
		Metadata: map[string]interface{}{
			"drift_count":      len(result.DriftedFiles),
			"issue_type":       result.IssueType,
			"drifted_files":    result.DriftedFiles,
			"enforcement_mode": result.EnforcementMode,
		},
		SchemaVersion: "1",
	})
}

// scopeDriftAudit is the on-disk shape of .nightgauge/audit/scope-drift-stats.json.
type scopeDriftAudit struct {
	TotalDriftEvents int                 `json:"total_drift_events"`
	ByIssueType      map[string]int      `json:"by_issue_type"`
	LastDriftAt      string              `json:"last_drift_at"`
	Events           []scopeDriftAuditEv `json:"events"`
}

type scopeDriftAuditEv struct {
	IssueNumber  int      `json:"issue_number"`
	IssueType    string   `json:"issue_type"`
	DriftedFiles []string `json:"drifted_files"`
	Mode         string   `json:"enforcement_mode"`
	Timestamp    string   `json:"timestamp"`
}

// appendScopeDriftAudit updates the local audit counter file. Best-effort:
// failures are reported via stderr but do not fail the gate.
func appendScopeDriftAudit(workdir string, issueNum int, result *scopeDriftGate.GateResult) error {
	auditDir := filepath.Join(workdir, ".nightgauge", "audit")
	if err := os.MkdirAll(auditDir, 0o755); err != nil {
		return err
	}
	auditPath := filepath.Join(auditDir, "scope-drift-stats.json")

	var audit scopeDriftAudit
	audit.ByIssueType = map[string]int{}
	if data, err := os.ReadFile(auditPath); err == nil {
		_ = json.Unmarshal(data, &audit)
		if audit.ByIssueType == nil {
			audit.ByIssueType = map[string]int{}
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	audit.TotalDriftEvents++
	audit.ByIssueType[result.IssueType]++
	audit.LastDriftAt = now
	audit.Events = append(audit.Events, scopeDriftAuditEv{
		IssueNumber:  issueNum,
		IssueType:    result.IssueType,
		DriftedFiles: result.DriftedFiles,
		Mode:         result.EnforcementMode,
		Timestamp:    now,
	})
	// Cap retained events to avoid unbounded growth.
	const maxEvents = 200
	if len(audit.Events) > maxEvents {
		audit.Events = audit.Events[len(audit.Events)-maxEvents:]
	}

	data, err := json.MarshalIndent(audit, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(auditPath, data, 0o644)
}

func renderScopeDriftHuman(result *scopeDriftGate.GateResult, status string, issueNum int) {
	switch status {
	case "passed":
		if result.Bypassed {
			fmt.Printf("Scope drift gate: BYPASSED (issue #%d)\n", issueNum)
		} else {
			fmt.Printf("Scope drift gate: PASSED (issue #%d)\n", issueNum)
		}
		fmt.Printf("  %s\n", result.Reason)
		if len(result.DriftedFiles) > 0 {
			fmt.Printf("  Drifted (warn-only): %d file(s)\n", len(result.DriftedFiles))
			for _, f := range result.DriftedFiles {
				fmt.Printf("    - %s\n", f)
			}
		}
	case "skipped":
		fmt.Printf("Scope drift gate: SKIPPED (issue #%d) — %s\n", issueNum, result.Reason)
	case "failed":
		fmt.Fprintf(os.Stderr, "Scope drift gate: BLOCKED (issue #%d, strict mode)\n", issueNum)
		fmt.Fprintf(os.Stderr, "  %s\n", result.Reason)
		fmt.Fprintf(os.Stderr, "  Drifted files (%d):\n", len(result.DriftedFiles))
		for _, f := range result.DriftedFiles {
			fmt.Fprintf(os.Stderr, "    - %s\n", f)
		}
		if result.SuggestedAction != "" {
			fmt.Fprintf(os.Stderr, "  Suggested action: %s\n", result.SuggestedAction)
		}
	}
}

// scopeDriftGateYAML mirrors the pipeline.scope_drift_gate config section.
type scopeDriftGateYAML struct {
	Pipeline struct {
		ScopeDriftGate struct {
			Enabled         *bool    `yaml:"enabled"`
			EnforcementMode *string  `yaml:"enforcement_mode"`
			BypassLabel     *string  `yaml:"bypass_label"`
			AllowlistDocs   []string `yaml:"allowlist_docs"`
			AllowlistChore  []string `yaml:"allowlist_chore"`
		} `yaml:"scope_drift_gate"`
	} `yaml:"pipeline"`
}

// loadScopeDriftGateConfigFromYAML reads pipeline.scope_drift_gate from the
// YAML config, applying defaults for missing fields. Same convention as
// loadSizeGateConfigFromYAML and loadBaselineGateConfigFromYAML.
func loadScopeDriftGateConfigFromYAML(configPath string) scopeDriftGate.GateConfig {
	cfg := scopeDriftGate.DefaultGateConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg
	}
	var y scopeDriftGateYAML
	if err := yaml.Unmarshal(data, &y); err != nil {
		return cfg
	}
	sg := y.Pipeline.ScopeDriftGate
	if sg.Enabled != nil {
		cfg.Enabled = *sg.Enabled
	}
	if sg.EnforcementMode != nil {
		mode := strings.ToLower(strings.TrimSpace(*sg.EnforcementMode))
		if mode == scopeDriftGate.EnforcementWarn || mode == scopeDriftGate.EnforcementStrict {
			cfg.EnforcementMode = mode
		}
	}
	if sg.BypassLabel != nil {
		cfg.BypassLabel = *sg.BypassLabel
	}
	if len(sg.AllowlistDocs) > 0 {
		cfg.AllowlistDocs = sg.AllowlistDocs
	}
	if len(sg.AllowlistChore) > 0 {
		cfg.AllowlistChore = sg.AllowlistChore
	}
	return cfg
}
