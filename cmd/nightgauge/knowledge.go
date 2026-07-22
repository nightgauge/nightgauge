package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/recall"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/nightgauge/nightgauge/internal/pipeline"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

// emitKnowledgeTelemetry is the single hook used by every knowledge
// subcommand's success path. It is intentionally fire-and-forget: config
// loads and emit failures are swallowed because telemetry must NEVER fail a
// user-facing operation (see internal/knowledge/telemetry doc). Any error is
// logged to stderr and dropped.
func emitKnowledgeTelemetry(workdir string, ev telemetry.Event) {
	if workdir == "" {
		return
	}
	cfg, err := config.Load(workdir)
	if err != nil || cfg == nil || cfg.Knowledge == nil {
		// No config → no opt-in to KB → no telemetry. Silent.
		return
	}
	if !cfg.Knowledge.IsTelemetryEnabled() {
		return
	}
	if err := telemetry.Emit(workdir, ev); err != nil {
		fmt.Fprintf(os.Stderr, "[telemetry] emit %s failed: %v\n", ev.Type, err)
	}
}

func knowledgeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "Knowledge base operations",
		Long:  "Manage the .nightgauge/knowledge/ directory: scaffold entries, prune empty files, generate the index, and scaffold workspace-level entries.",
	}
	cmd.AddCommand(knowledgeScaffoldCmd(), knowledgePruneCmd(), knowledgeIndexCmd(), knowledgeWorkspaceCreateCmd(), knowledgeWorkspaceInitCmd(), knowledgeGraduateCmd(), knowledgeGraduateCandidatesCmd(), knowledgeStatsCmd(), knowledgeRenderCmd(), knowledgeRenderPRSectionCmd(), knowledgeNewCmd(), knowledgeValidateCmd(), knowledgeRecordOutcomeCmd(), knowledgeTelemetryCmd(), knowledgeMetricsCmd(), knowledgeRecallCmd(), knowledgeReindexCmd())
	return cmd
}

func knowledgeRecallCmd() *cobra.Command {
	var (
		workdir     string
		outputJSON  bool
		scopes      string
		limit       int
		updateCache bool
	)

	cmd := &cobra.Command{
		Use:          "recall <query>",
		Short:        "Find and rank prior decisions by semantic similarity",
		Long:         "Search all knowledge base documents using BM25 scoring with tag and path boosting. Caches the index at .nightgauge/knowledge/.recall-cache/.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  nightgauge knowledge recall "BM25 scoring" --json
  nightgauge knowledge recall "auth flow" --scopes local --limit 5
  nightgauge knowledge recall "deployment strategy" --update-cache --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			// Load config for BM25 params and telemetry.
			cfg, loadErr := config.Load(workdir)
			var knowledgeCfg *config.KnowledgeConfig
			if loadErr != nil || cfg == nil {
				knowledgeCfg = &config.KnowledgeConfig{}
			} else {
				knowledgeCfg = cfg.Knowledge
				if knowledgeCfg == nil {
					knowledgeCfg = &config.KnowledgeConfig{}
				}
			}

			// Parse scopes flag.
			scopeList := parseScopeList(scopes)

			// Invalidate cache if --update-cache.
			if updateCache {
				_ = os.Remove(filepath.Join(workdir, ".nightgauge", "knowledge", ".recall-cache", "index.jsonl"))
			}

			start := time.Now()

			idx, err := recall.BuildIndex(workdir, scopeList, knowledgeCfg)
			if err != nil {
				return fmt.Errorf("build recall index: %w", err)
			}

			result, err := recall.Query(idx, query, limit, scopeList)
			if err != nil {
				return fmt.Errorf("recall query: %w", err)
			}

			queryID := uuid.New().String()
			result.QueryID = queryID

			durationMs := time.Since(start).Milliseconds()
			resultCount := result.TotalHits
			querySummary := query
			if len(querySummary) > telemetry.QuerySummaryMaxChars {
				querySummary = querySummary[:telemetry.QuerySummaryMaxChars]
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:         telemetry.EventRecall,
				Scope:        "local",
				QuerySummary: querySummary,
				RecallID:     queryID,
				ResultCount:  &resultCount,
				DurationMs:   durationMs,
				Status:       "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			// Human-readable table output.
			if len(result.Hits) == 0 {
				fmt.Printf("No results for query: %q\n", query)
				return nil
			}
			fmt.Printf("Recall results for %q (%d total hits, showing top %d)\n\n", query, result.TotalHits, len(result.Hits))
			fmt.Printf("%-4s %-8s %-60s %s\n", "RANK", "SCORE", "PATH", "SNIPPET")
			fmt.Println(strings.Repeat("-", 100))
			for _, h := range result.Hits {
				snippet := h.Snippet
				if len(snippet) > 40 {
					snippet = snippet[:40] + "…"
				}
				path := h.Path
				if len(path) > 60 {
					path = "…" + path[len(path)-57:]
				}
				fmt.Printf("%-4d %-8.3f %-60s %s\n", h.Rank, h.Score, path, snippet)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&scopes, "scopes", "local,cross-repo,workspace", "Comma-separated scopes: local, cross-repo, workspace")
	cmd.Flags().IntVar(&limit, "limit", 10, "Maximum number of results to return")
	cmd.Flags().BoolVar(&updateCache, "update-cache", false, "Force cache rebuild before querying")

	return cmd
}

func knowledgeReindexCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "reindex",
		Short:        "Rebuild the persistent knowledge metadata index (#2964)",
		Long:         "Force a full rebuild of .nightgauge/knowledge/.index.json — the metadata + backlink index consumed by VSCode's KnowledgeTreeProvider. Distinct from the BM25 recall cache rebuilt by 'knowledge recall --update-cache'.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: `  nightgauge knowledge reindex
  nightgauge knowledge reindex --json
  nightgauge knowledge reindex --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			start := time.Now()
			idx, err := knowledge.BuildMetadataIndex(workdir)
			if err != nil {
				return fmt.Errorf("build metadata index: %w", err)
			}
			elapsed := time.Since(start)

			indexPath := filepath.Join(".nightgauge", "knowledge", ".index.json")

			if outputJSON {
				type result struct {
					IndexPath string `json:"index_path"`
					Entries   int    `json:"entries"`
					BuiltAt   string `json:"built_at"`
					DurationMs int64 `json:"duration_ms"`
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result{
					IndexPath:  indexPath,
					Entries:    len(idx.Entries),
					BuiltAt:    idx.BuiltAt,
					DurationMs: elapsed.Milliseconds(),
				})
			}

			fmt.Printf("Indexed %d files in %dms\n", len(idx.Entries), elapsed.Milliseconds())
			fmt.Printf("Written: %s\n", indexPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

func parseScopeList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func knowledgeNewCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
	)

	validTypes := make([]string, len(knowledge.ValidRepoTopicTypes))
	for i, t := range knowledge.ValidRepoTopicTypes {
		validTypes[i] = string(t)
	}

	cmd := &cobra.Command{
		Use:   "new <type> <slug>",
		Short: "Scaffold a repo-topic knowledge entry",
		Long: `Scaffold a repo-topic knowledge entry at .nightgauge/knowledge/<type>/<slug>.md.

When the category directory is new, README.md and _template.md are also created.
The operation is idempotent — calling it a second time with the same type and slug
returns without modifying the existing file.

Valid types: ` + strings.Join(validTypes, ", "),
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		Example: `  nightgauge knowledge new architecture six-stage-pipeline
  nightgauge knowledge new glossary knowledge-path
  nightgauge knowledge new runbook recover-stuck-autonomous
  nightgauge knowledge new post-mortem agent-timeout-cascade
  nightgauge knowledge new glossary wave --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			rawType, rawSlug := args[0], args[1]

			topicType := knowledge.RepoTopicType(rawType)
			if !knowledge.IsValidRepoTopicType(topicType) {
				return fmt.Errorf("type %q is not valid; must be one of: %s",
					rawType, strings.Join(validTypes, ", "))
			}

			slug := workspace.GenerateSlug(rawSlug)
			if slug == "" {
				return fmt.Errorf("slug %q normalizes to empty string; provide a non-empty alphanumeric slug", rawSlug)
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			start := time.Now()
			result, err := knowledge.ScaffoldRepoTopic(workdir, topicType, slug)
			if err != nil {
				return fmt.Errorf("scaffold repo-topic: %w", err)
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:       telemetry.EventWrite,
				Scope:      "repo:" + string(topicType),
				Path:       result.FilePath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if result.Skipped {
				fmt.Printf("Skipped (already exists): %s\n", result.FilePath)
				return nil
			}
			fmt.Printf("Created: %s\n", result.FilePath)
			for _, f := range result.FilesCreated {
				fmt.Printf("  + %s/%s\n", result.KnowledgePath, f)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")

	return cmd
}

func knowledgeGraduateCmd() *cobra.Command {
	var (
		section       string
		adrAnchor     string
		workdir       string
		outputJSON    bool
		autoMode      bool
		adrIndex      int
		dryRun        bool
		allCandidates bool
		baseBranch    string
		forgeFlag     string
		repoFlag      string
		ownerFlag     string
		projectFlag   int
		ownerType     string
	)

	cmd := &cobra.Command{
		Use:   "graduate <issue> [--auto | --section <docs-path> --adr ADR-NNN]",
		Short: "Graduate a decisions.md ADR block to docs/ (auto-mode default, manual override)",
		Long: `Graduate one or more ADR blocks from a per-issue decisions.md to a
stable docs/ destination.

By default ("--auto") the command performs the entire ritual: selects a
candidate via the same ranking used by graduate-candidates, creates a
branch (docs/graduate-<N>-adr-NNN), appends the verbatim Decision block
plus a "<!-- graduated-from: -->" companion to the destination doc,
writes the source-side "<!-- graduated-to: -->" backlink, commits,
pushes, opens a PR, applies the three default labels, and adds the PR
to the project board with Status=Ready.

Manual override mode (without --auto) preserves the previous behavior:
prints the source ADR, writes the backlink, and opens $EDITOR on the
target docs file. Required flags in manual mode: --section and --adr.

See docs/KNOWLEDGE_BASE.md#graduation-workflow for the full workflow.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example: `  # Auto-mode (default ritual)
  nightgauge knowledge graduate 1234 --auto
  nightgauge knowledge graduate 1234 --auto --adr-index 2
  nightgauge knowledge graduate 1234 --auto --dry-run --json
  nightgauge knowledge graduate 1234 --auto --all-candidates

  # Manual override (legacy)
  nightgauge knowledge graduate 1234 --section docs/ARCHITECTURE.md#sse-pipeline-events --adr ADR-001`,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber := 0
			if _, err := fmt.Sscanf(args[0], "%d", &issueNumber); err != nil || issueNumber <= 0 {
				return fmt.Errorf("<issue> must be a positive integer, got %q", args[0])
			}

			if autoMode {
				return runGraduateAuto(cmd, issueNumber, autoCLIOptions{
					Workdir:       workdir,
					ADRIndex:      adrIndex,
					DryRun:        dryRun,
					AllCandidates: allCandidates,
					OutputJSON:    outputJSON,
					BaseBranch:    baseBranch,
				})
			}

			if strings.TrimSpace(section) == "" {
				return fmt.Errorf("--section is required (e.g., docs/ARCHITECTURE.md#sse-pipeline-events) when --auto is not set")
			}
			if strings.TrimSpace(adrAnchor) == "" {
				return fmt.Errorf("--adr is required (e.g., ADR-001) when --auto is not set — explicit anchor avoids ambiguity")
			}

			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			decisionsPath, err := knowledge.FindDecisionsPath(workdir, issueNumber)
			if err != nil {
				return err
			}
			absDecisions := decisionsPath
			if !filepath.IsAbs(absDecisions) {
				absDecisions = filepath.Join(workdir, decisionsPath)
			}

			start := time.Now()
			block, err := knowledge.ReadADRBlock(absDecisions, adrAnchor)
			if err != nil {
				return err
			}

			if err := knowledge.WriteBacklink(knowledge.GraduateInput{
				DecisionsPath: absDecisions,
				ADRAnchor:     adrAnchor,
				DocsSection:   section,
			}); err != nil {
				return err
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventGraduate,
				Mode:        "manual",
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				Path:        decisionsPath,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      "success",
			})

			graduatedFrom := knowledge.FormatGraduatedFromComment(decisionsPath, adrAnchor)
			docsPath := strings.SplitN(section, "#", 2)[0]
			editor := os.Getenv("EDITOR")

			if outputJSON {
				type result struct {
					IssueNumber       int    `json:"issue_number"`
					DecisionsPath     string `json:"decisions_path"`
					DocsSection       string `json:"docs_section"`
					DocsPath          string `json:"docs_path"`
					ADRAnchor         string `json:"adr_anchor"`
					ADRBlock          string `json:"adr_block"`
					GraduatedFrom     string `json:"graduated_from_comment"`
					EditorOpened      bool   `json:"editor_opened"`
					EditorEnvironment string `json:"editor"`
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result{
					IssueNumber:       issueNumber,
					DecisionsPath:     decisionsPath,
					DocsSection:       section,
					DocsPath:          docsPath,
					ADRAnchor:         adrAnchor,
					ADRBlock:          block,
					GraduatedFrom:     graduatedFrom,
					EditorOpened:      editor != "",
					EditorEnvironment: editor,
				})
			}

			fmt.Printf("Source: %s (%s)\n", decisionsPath, adrAnchor)
			fmt.Printf("Target: %s\n", section)
			fmt.Println()
			fmt.Println("--- Source ADR block ---")
			fmt.Print(block)
			if !strings.HasSuffix(block, "\n") {
				fmt.Println()
			}
			fmt.Println("--- end ---")
			fmt.Println()
			fmt.Printf("Backlink written to %s\n", decisionsPath)
			fmt.Println()
			fmt.Println("Paste the following comment under the destination heading in the target docs file:")
			fmt.Println("  " + graduatedFrom)
			fmt.Println()

			if editor == "" {
				fmt.Printf("$EDITOR is not set. Open the target file manually: %s\n", docsPath)
				fmt.Println("Tip: set $EDITOR to auto-open the target docs file next time.")
				return nil
			}

			fmt.Printf("Opening %s in %s ...\n", docsPath, editor)
			editorCmd := exec.Command(editor, docsPath)
			editorCmd.Stdin = os.Stdin
			editorCmd.Stdout = os.Stdout
			editorCmd.Stderr = os.Stderr
			if err := editorCmd.Run(); err != nil {
				return fmt.Errorf("run %s: %w", editor, err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&section, "section", "", "Destination docs section, e.g. docs/ARCHITECTURE.md#sse-pipeline-events (manual mode)")
	cmd.Flags().StringVar(&adrAnchor, "adr", "", "ADR anchor to graduate, e.g. ADR-001 (manual mode)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON (skips $EDITOR in manual mode)")

	// Auto-mode flags.
	cmd.Flags().BoolVar(&autoMode, "auto", false, "Run end-to-end graduation: branch + commit + push + PR + labels + project sync")
	cmd.Flags().IntVar(&adrIndex, "adr-index", 0, "Specific ADR index to graduate in auto mode (default: highest-scoring)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print planned changes without making them (auto mode)")
	cmd.Flags().BoolVar(&allCandidates, "all-candidates", false, "Open one PR per qualifying candidate (auto mode)")
	cmd.Flags().StringVar(&baseBranch, "base", "", "Base branch for the graduation PR (default: repo default branch)")

	// Forge router flags — surfaced so resolveForgeForGraduate can resolve the
	// right ForgeClient without requiring callers to set them when defaults
	// in config.yaml suffice.
	cmd.Flags().StringVar(&forgeFlag, "forge", "", "Forge id override (e.g. 'github')")
	cmd.Flags().StringVar(&repoFlag, "repo", "", "Target repo as owner/name")
	cmd.Flags().StringVar(&ownerFlag, "owner", "", "Owner namespace (org or user)")
	cmd.Flags().IntVar(&projectFlag, "project", 0, "Project board number (1-based)")
	cmd.Flags().StringVar(&ownerType, "owner-type", "", "Owner type: org or user (default org)")

	return cmd
}

func knowledgeScaffoldCmd() *cobra.Command {
	var (
		issueNumber      int
		title            string
		criteria         []string
		workdir          string
		outputJSON       bool
		knowledgeEnabled bool
		workspaceScoped  bool
	)

	cmd := &cobra.Command{
		Use:          "scaffold",
		Short:        "Scaffold a knowledge directory for an issue",
		SilenceUsage: true,
		Example: `  nightgauge knowledge scaffold --issue-number 42 --title "Add photo upload"
  nightgauge knowledge scaffold --issue-number 42 --title "Add photo upload" --criteria "User can upload JPEG" --criteria "Max 5 MB"
  nightgauge knowledge scaffold --issue-number 42 --title "Add photo upload" --knowledge-enabled true --workspace-scoped true --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNumber <= 0 {
				return fmt.Errorf("--issue-number must be a positive integer")
			}
			if strings.TrimSpace(title) == "" {
				return fmt.Errorf("--title is required")
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			start := time.Now()
			result, err := knowledge.ScaffoldWithConfig(workdir, issueNumber, title, criteria, knowledgeEnabled, workspaceScoped)
			if err != nil {
				return fmt.Errorf("scaffold knowledge: %w", err)
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventScaffold,
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				Path:        result.KnowledgePath,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if result.Skipped {
				if result.SkipReason != "" {
					fmt.Printf("Skipped (%s)\n", result.SkipReason)
				} else {
					fmt.Printf("Skipped (directory already exists): %s\n", result.KnowledgePath)
				}
				return nil
			}
			fmt.Printf("Scaffolded: %s\n", result.KnowledgePath)
			for _, f := range result.FilesCreated {
				fmt.Printf("  created: %s/%s\n", result.KnowledgePath, f)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue-number", 0, "GitHub issue number (required)")
	cmd.Flags().StringVar(&title, "title", "", "Issue title used for slug generation (required)")
	cmd.Flags().StringArrayVar(&criteria, "criteria", nil, "Acceptance criteria lines (repeatable)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&knowledgeEnabled, "knowledge-enabled", false, "Honor knowledge.enabled config flag; when false, returns skipped immediately")
	cmd.Flags().BoolVar(&workspaceScoped, "workspace-scoped", true, "Honor knowledge.workspace_scoped config flag")
	_ = cmd.MarkFlagRequired("issue-number")
	_ = cmd.MarkFlagRequired("title")

	return cmd
}

func knowledgePruneCmd() *cobra.Command {
	var (
		workdir    string
		dryRun     bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "prune",
		Short:        "Remove knowledge directories containing only boilerplate content",
		SilenceUsage: true,
		Example: `  nightgauge knowledge prune
  nightgauge knowledge prune --dry-run
  nightgauge knowledge prune --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			start := time.Now()
			pruned, err := knowledge.PruneEmpty(workdir, dryRun)
			if err != nil {
				return fmt.Errorf("prune knowledge: %w", err)
			}

			prunedCount := len(pruned)
			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventPrune,
				DurationMs:  time.Since(start).Milliseconds(),
				ResultCount: &prunedCount,
				Status:      "success",
			})

			if outputJSON {
				type result struct {
					DryRun bool     `json:"dry_run"`
					Pruned []string `json:"pruned"`
					Count  int      `json:"count"`
				}
				out := result{DryRun: dryRun, Pruned: pruned, Count: len(pruned)}
				if out.Pruned == nil {
					out.Pruned = []string{}
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(pruned) == 0 {
				action := "removed"
				if dryRun {
					action = "would remove"
				}
				fmt.Printf("No empty knowledge directories to %s.\n", action)
				return nil
			}

			action := "Removed"
			if dryRun {
				action = "Would remove"
			}
			fmt.Printf("%s %d empty knowledge director(ies):\n", action, len(pruned))
			for _, p := range pruned {
				fmt.Printf("  %s\n", p)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "List directories that would be removed without deleting them")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")

	return cmd
}

func knowledgeIndexCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
		crossRepo  bool
		wsKB       bool
		limit      int
	)

	cmd := &cobra.Command{
		Use:          "index",
		Short:        "Generate the knowledge base index (README.md)",
		SilenceUsage: true,
		Example: `  nightgauge knowledge index
  nightgauge knowledge index --json
  nightgauge knowledge index --cross-repo --workspace --limit 20 --json
  nightgauge knowledge index --cross-repo --limit 5 --json
  nightgauge knowledge index --workspace --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			if limit <= 0 {
				limit = 20
			}

			start := time.Now()
			relPath, err := knowledge.GenerateIndex(workdir)
			if err != nil {
				return fmt.Errorf("generate knowledge index: %w", err)
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:       telemetry.EventIndex,
				Scope:      "workspace",
				Path:       relPath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			var crossRepoEntries []knowledge.CrossRepoEntry
			if crossRepo {
				crossRepoEntries, err = knowledge.ScanCrossRepoKnowledge(workdir, limit)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: cross-repo scan: %v\n", err)
					crossRepoEntries = []knowledge.CrossRepoEntry{}
				}
			}

			var workspaceKBEntries []knowledge.WorkspaceKBEntry
			if wsKB {
				workspaceKBEntries, err = knowledge.ScanWorkspaceKB(workdir, limit)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: workspace KB scan: %v\n", err)
					workspaceKBEntries = []knowledge.WorkspaceKBEntry{}
				}
			}

			if outputJSON {
				type result struct {
					IndexPath          string                       `json:"index_path"`
					CrossRepoKnowledge []knowledge.CrossRepoEntry   `json:"cross_repo_knowledge,omitempty"`
					WorkspaceKB        []knowledge.WorkspaceKBEntry `json:"workspace_kb,omitempty"`
				}
				out := result{IndexPath: relPath}
				if crossRepo {
					if crossRepoEntries == nil {
						crossRepoEntries = []knowledge.CrossRepoEntry{}
					}
					out.CrossRepoKnowledge = crossRepoEntries
				}
				if wsKB {
					if workspaceKBEntries == nil {
						workspaceKBEntries = []knowledge.WorkspaceKBEntry{}
					}
					out.WorkspaceKB = workspaceKBEntries
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("Index written: %s\n", relPath)
			if crossRepo && len(crossRepoEntries) > 0 {
				fmt.Printf("Cross-repo knowledge: %d repo(s)\n", len(crossRepoEntries))
				for _, e := range crossRepoEntries {
					fmt.Printf("  %s: %d entries at %s\n", e.Repo, len(e.Entries), e.Path)
				}
			}
			if wsKB && len(workspaceKBEntries) > 0 {
				fmt.Printf("Workspace KB: %d namespace(s)\n", len(workspaceKBEntries))
				for _, e := range workspaceKBEntries {
					fmt.Printf("  %s: %d entries at %s\n", e.Namespace, len(e.Entries), e.Path)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&crossRepo, "cross-repo", false, "Scan sibling repositories' knowledge directories (requires .vscode/nightgauge-workspace.yaml)")
	cmd.Flags().BoolVar(&wsKB, "workspace", false, "Scan workspace-level KB categories (product/, cross-repo/, architecture/)")
	cmd.Flags().IntVar(&limit, "limit", 20, "Max entries per repository or category")

	return cmd
}

func knowledgeWorkspaceCreateCmd() *cobra.Command {
	var (
		repos      []string
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "workspace-create <category> <slug>",
		Short:        "Scaffold a workspace-level knowledge directory",
		Long:         "Creates <workspace-root>/.nightgauge/knowledge/<category>/<slug>/ with PRD.md and decisions.md.",
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		Example: `  nightgauge knowledge workspace-create product my-feature
  nightgauge knowledge workspace-create cross-repo auth-flow --repos nightgauge,acme-platform
  nightgauge knowledge workspace-create product my-feature --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			category, rawSlug := args[0], args[1]

			if !workspace.IsValidCategory(category) {
				return fmt.Errorf("category %q is not valid; must be one of: %s",
					category, strings.Join(workspace.ValidCategories, ", "))
			}

			slug := workspace.GenerateSlug(rawSlug)
			if slug == "" {
				return fmt.Errorf("slug %q normalizes to empty string; provide a non-empty alphanumeric slug", rawSlug)
			}

			startDir := workdir
			if startDir == "" {
				var err error
				startDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
			if err != nil {
				return err
			}

			var flatRepos []string
			for _, r := range repos {
				for _, part := range strings.Split(r, ",") {
					if trimmed := strings.TrimSpace(part); trimmed != "" {
						flatRepos = append(flatRepos, trimmed)
					}
				}
			}

			start := time.Now()
			result, err := workspace.Create(workspace.CreateInput{
				WorkspaceRoot: wsRoot,
				Category:      category,
				Slug:          slug,
				Repos:         flatRepos,
			})
			if err != nil {
				return err
			}

			emitKnowledgeTelemetry(wsRoot, telemetry.Event{
				Type:       telemetry.EventScaffold,
				Scope:      "workspace",
				Path:       result.KnowledgePath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if result.Skipped {
				fmt.Printf("Skipped (already exists): %s\n", result.KnowledgePath)
			} else {
				fmt.Printf("Created: %s\n", result.KnowledgePath)
				for _, f := range result.FilesCreated {
					fmt.Printf("  + %s\n", f)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&repos, "repos", nil, "Repository names for frontmatter scope (repeatable; also accepts comma-separated values)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Starting directory for workspace detection (default: cwd)")

	return cmd
}

func knowledgeWorkspaceInitCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:   "workspace-init",
		Short: "Scaffold the workspace-level knowledge tree (product/, cross-repo/, architecture/)",
		Long: `Create the three-category workspace knowledge tree at
<workspace-root>/.nightgauge/knowledge/{product,cross-repo,architecture}/ and
seed each category with README.md plus starter topic entries.

Idempotent — existing files are never overwritten. On re-run, returns Skipped=true
with an empty files_created list.

Workspace root is detected by walking up from --workdir (or CWD) looking for
.vscode/nightgauge-workspace.yaml.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		Example: `  nightgauge knowledge workspace-init
  nightgauge knowledge workspace-init --json
  nightgauge knowledge workspace-init --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			startDir := workdir
			if startDir == "" {
				var err error
				startDir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			wsRoot, err := workspace.DetectWorkspaceRoot(startDir)
			if err != nil {
				return err
			}

			start := time.Now()
			result, err := workspace.InitTree(workspace.InitTreeInput{WorkspaceRoot: wsRoot})
			if err != nil {
				return err
			}

			emitKnowledgeTelemetry(wsRoot, telemetry.Event{
				Type:       telemetry.EventScaffold,
				Scope:      "workspace",
				Path:       result.KnowledgePath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if result.Skipped {
				fmt.Printf("Skipped (already initialized): %s\n", result.KnowledgePath)
				return nil
			}
			fmt.Printf("Initialized workspace KB at %s\n", result.KnowledgePath)
			for _, f := range result.FilesCreated {
				fmt.Printf("  + %s\n", f)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Starting directory for workspace detection (default: cwd)")

	return cmd
}

func knowledgeRenderCmd() *cobra.Command {
	var workdir string

	cmd := &cobra.Command{
		Use:          "render <path>",
		Short:        "Render a knowledge file with wiki-links rewritten to Markdown links",
		Long:         "Reads <path>, resolves all [[wiki-links]] to standard Markdown links, and writes the result to stdout. Broken links are preserved as-is; warnings go to stderr.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Example:      `  nightgauge knowledge render .nightgauge/knowledge/features/2959-kb-v2/decisions.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			absPath := filePath
			if !filepath.IsAbs(absPath) {
				absPath = filepath.Join(workdir, filePath)
			}

			content, err := os.ReadFile(absPath)
			if err != nil {
				return fmt.Errorf("read file %s: %w", filePath, err)
			}

			start := time.Now()
			rendered, warnings, err := knowledge.ResolveWikiLinks(string(content), absPath, workdir)
			if err != nil {
				return fmt.Errorf("resolve wiki-links: %w", err)
			}

			for _, w := range warnings {
				fmt.Fprintf(os.Stderr, "warning: %s\n", w)
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:       telemetry.EventRead,
				Path:       absPath,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			fmt.Print(rendered)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	return cmd
}

func knowledgeRenderPRSectionCmd() *cobra.Command {
	var (
		issueNumber int
		workdir     string
		coverageMap string
	)

	cmd := &cobra.Command{
		Use:   "render-pr-section",
		Short: "Render the ## Knowledge section for a PR body",
		Long: `Emit the Markdown ## Knowledge block for the given issue's knowledge directory.

Walks .nightgauge/knowledge/features/{N}-*/ and emits one bullet per top-level
.md file (excluding README.md and _template.md). Well-known filenames (PRD.md,
decisions.md, outcomes.md) render with fixed descriptions in deterministic order;
remaining files render with title-cased labels in case-insensitive alphabetical order.

When --coverage-map is provided and the file exists, a "## PRD Coverage" section
is prepended to the output before the ## Knowledge section.

Prints nothing and exits 0 when the directory is missing or contains no qualifying
entries — matches the no-op semantics of the bash dictionary loop it replaces.`,
		SilenceUsage: true,
		Example: `  nightgauge knowledge render-pr-section --issue 1234
  nightgauge knowledge render-pr-section --issue 1234 --workdir /path/to/repo
  nightgauge knowledge render-pr-section --issue 1234 --coverage-map .nightgauge/pipeline/coverage-map-1234.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNumber <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			start := time.Now()

			// Render coverage section first (if --coverage-map provided).
			if coverageMap != "" {
				coverageSection, err := knowledge.RenderCoverageMapSection(coverageMap)
				if err != nil {
					// Non-fatal: warn and continue without coverage section.
					fmt.Fprintf(os.Stderr, "[knowledge] WARNING: coverage map render failed: %v\n", err)
				} else if coverageSection != "" {
					fmt.Print(coverageSection)
					fmt.Println()
				}
			}

			rendered, err := knowledge.RenderPRSection(workdir, issueNumber)
			if err != nil {
				return fmt.Errorf("render PR section: %w", err)
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventRead,
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      "success",
			})

			fmt.Print(rendered)
			return nil
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "Issue number (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().StringVar(&coverageMap, "coverage-map", "", "Path to coverage-map-{N}.json (optional; prepends ## PRD Coverage section)")
	_ = cmd.MarkFlagRequired("issue")

	return cmd
}

func knowledgeStatsCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
		stale      bool
		staleDays  int
	)

	cmd := &cobra.Command{
		Use:          "stats",
		Short:        "Print per-issue knowledge write counts",
		SilenceUsage: true,
		Example: `  nightgauge knowledge stats
  nightgauge knowledge stats --json
  nightgauge knowledge stats --stale --stale-days=30
  nightgauge knowledge stats --stale --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}
			// Defensively guard against negative day counts; explicit 0 is
			// allowed and means "any read is fresh" — flags ADRs with no
			// reads at all.
			if staleDays < 0 {
				staleDays = 30
			}

			start := time.Now()

			if stale {
				report, err := buildStaleReport(workdir, staleDays)
				if err != nil {
					return fmt.Errorf("stale scan: %w", err)
				}
				emitStatsTelemetry(workdir, start, len(report.Stale))

				if outputJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(report)
				}
				if len(report.Stale) == 0 {
					fmt.Printf("No stale ADRs (threshold: %d days).\n", staleDays)
					return nil
				}
				fmt.Printf("%-60s %-22s %s\n", "PATH", "LAST_READ", "DAYS_SINCE_READ")
				fmt.Println(strings.Repeat("-", 100))
				for _, s := range report.Stale {
					last := s.LastReadAt
					if last == "" {
						last = "never"
					}
					fmt.Printf("%-60s %-22s %d\n", s.Path, last, s.DaysSinceRead)
				}
				fmt.Printf("\nStale ADRs (>%d days): %d\n", staleDays, len(report.Stale))
				return nil
			}

			stats, err := knowledge.Stats(workdir)
			if err != nil {
				return fmt.Errorf("knowledge stats: %w", err)
			}

			emitStatsTelemetry(workdir, start, len(stats))

			if outputJSON {
				type result struct {
					Entries []knowledge.IssueStats `json:"entries"`
					Count   int                    `json:"count"`
				}
				entries := stats
				if entries == nil {
					entries = []knowledge.IssueStats{}
				}
				out := result{Entries: entries, Count: len(entries)}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(stats) == 0 {
				fmt.Println("No knowledge entries found.")
				return nil
			}

			// Human-readable table: issue# | prd_bytes | decisions_bytes | outcomes_bytes | last_write
			fmt.Printf("%-8s %-12s %-16s %-14s %s\n", "ISSUE", "PRD_BYTES", "DECISIONS_BYTES", "OUTCOMES_BYTES", "LAST_WRITE")
			fmt.Println(strings.Repeat("-", 72))
			for _, s := range stats {
				lastWrite := s.LastWrite
				if lastWrite == "" {
					lastWrite = "—"
				}
				fmt.Printf("%-8d %-12d %-16d %-14d %s\n",
					s.IssueNumber, s.PRDBytes, s.DecisionsBytes, s.OutcomesBytes, lastWrite)
			}
			fmt.Printf("\nTotal entries: %d\n", len(stats))
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&stale, "stale", false, "List ADRs whose last read/recall_hit telemetry event is older than --stale-days (or never)")
	cmd.Flags().IntVar(&staleDays, "stale-days", 30, "Staleness threshold in days (used with --stale)")

	return cmd
}

func emitStatsTelemetry(workdir string, start time.Time, resultCount int) {
	rc := resultCount
	emitKnowledgeTelemetry(workdir, telemetry.Event{
		Type:        telemetry.EventStats,
		DurationMs:  time.Since(start).Milliseconds(),
		ResultCount: &rc,
		Status:      "success",
	})
}

// StaleADRReport is the JSON shape produced by `knowledge stats --stale`.
// One entry per decisions.md whose last read or recall_hit event is older
// than ThresholdDays (or who has never been read). Sorted by DaysSinceRead
// descending so the most-dormant ADRs surface first.
type StaleADRReport struct {
	ThresholdDays int        `json:"threshold_days"`
	Stale         []StaleADR `json:"stale"`
}

// StaleADR is one row in the stale report.
type StaleADR struct {
	Path          string `json:"path"`
	LastReadAt    string `json:"last_read_at,omitempty"`
	DaysSinceRead int    `json:"days_since_read"`
}

func buildStaleReport(workdir string, thresholdDays int) (StaleADRReport, error) {
	report := StaleADRReport{
		ThresholdDays: thresholdDays,
		Stale:         []StaleADR{},
	}

	adrPaths, err := scanADRPaths(workdir)
	if err != nil {
		return report, err
	}

	events, err := pipeline.LoadKnowledgeEvents(workdir)
	if err != nil {
		return report, err
	}

	lastRead := map[string]time.Time{}
	for _, ev := range events {
		if ev.Type != telemetry.EventRead && ev.Type != telemetry.EventRecallHit {
			continue
		}
		if ev.Path == "" || ev.Timestamp == "" {
			continue
		}
		ts, perr := time.Parse(time.RFC3339, ev.Timestamp)
		if perr != nil {
			continue
		}
		key := normalizeADRPath(workdir, ev.Path)
		if prev, ok := lastRead[key]; !ok || ts.After(prev) {
			lastRead[key] = ts
		}
	}

	now := time.Now()
	cutoff := now.AddDate(0, 0, -thresholdDays)
	for _, adr := range adrPaths {
		key := normalizeADRPath(workdir, adr)
		last, ok := lastRead[key]
		if !ok {
			report.Stale = append(report.Stale, StaleADR{
				Path:          key,
				DaysSinceRead: thresholdDays + 1,
			})
			continue
		}
		if last.Before(cutoff) {
			report.Stale = append(report.Stale, StaleADR{
				Path:          key,
				LastReadAt:    last.UTC().Format(time.RFC3339),
				DaysSinceRead: int(now.Sub(last).Hours() / 24),
			})
		}
	}

	// Most-dormant first so humans see the worst offenders.
	for i := 0; i < len(report.Stale); i++ {
		for j := i + 1; j < len(report.Stale); j++ {
			if report.Stale[j].DaysSinceRead > report.Stale[i].DaysSinceRead {
				report.Stale[i], report.Stale[j] = report.Stale[j], report.Stale[i]
			}
		}
	}
	return report, nil
}

// scanADRPaths walks .nightgauge/knowledge/ collecting every decisions.md
// (per-issue and workspace categories). Returns workspace-relative paths.
func scanADRPaths(workdir string) ([]string, error) {
	root := filepath.Join(workdir, ".nightgauge", "knowledge")
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var paths []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Base(p) != "decisions.md" {
			return nil
		}
		rel, rerr := filepath.Rel(workdir, p)
		if rerr != nil {
			rel = p
		}
		paths = append(paths, rel)
		return nil
	})
	return paths, err
}

// normalizeADRPath converts an absolute or workspace-relative path to a
// stable workspace-relative key used to match telemetry events with ADRs on
// disk. Telemetry callers may emit either absolute paths (in-binary handlers)
// or already-relative paths (skill callers), so the normalization is one-way.
func normalizeADRPath(workdir, p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(workdir, p); err == nil {
			return rel
		}
	}
	return p
}

func knowledgeRecordOutcomeCmd() *cobra.Command {
	var (
		issueNumber    int
		status         string
		durationMins   int
		tokens         int
		costUSD        float64
		whatWentWell   string
		whatDidnt      string
		lessonsLearned string
		workdir        string
		outputJSON     bool
	)

	cmd := &cobra.Command{
		Use:          "record-outcome",
		Short:        "Append a pipeline outcome block to the knowledge base",
		SilenceUsage: true,
		Long: `Append a structured ## Outcome Markdown block to the knowledge base file for
the given issue. Prefers decisions.md when it exists; otherwise creates and
writes to outcomes.md. Idempotent — re-running with the same issue number is
a no-op when the outcome block already exists.`,
		Example: `  nightgauge knowledge record-outcome --issue 42 --status complete --duration 30 --tokens 5000 --cost 1.23
  nightgauge knowledge record-outcome --issue 42 --status partial --duration 15 --tokens 2000 --cost 0.50 \
    --what-went-well "Tests passed." --what-didnt "CI flaked once." --lessons-learned "Cache builds."
  nightgauge knowledge record-outcome --issue 42 --status failed --duration 5 --tokens 200 --cost 0.05 --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if issueNumber <= 0 {
				return fmt.Errorf("--issue must be a positive integer")
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			start := time.Now()
			result, err := knowledge.RecordOutcome(workdir, knowledge.RecordOutcomeInput{
				IssueNumber:    issueNumber,
				Status:         status,
				DurationMins:   durationMins,
				Tokens:         tokens,
				CostUSD:        costUSD,
				WhatWentWell:   whatWentWell,
				WhatDidnt:      whatDidnt,
				LessonsLearned: lessonsLearned,
			})
			if err != nil {
				return err
			}

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventWrite,
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				Path:        result.TargetFile,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if !result.Appended {
				fmt.Printf("Skipped (outcome already recorded): %s\n", result.TargetFile)
				return nil
			}
			action := "Appended to"
			if result.FileCreated {
				action = "Created"
			}
			fmt.Printf("%s: %s\n", action, result.TargetFile)
			return nil
		},
	}

	cmd.Flags().IntVar(&issueNumber, "issue", 0, "GitHub issue number (required)")
	cmd.Flags().StringVar(&status, "status", "", "Outcome status: complete, partial, failed (required)")
	cmd.Flags().IntVar(&durationMins, "duration", 0, "Pipeline duration in minutes")
	cmd.Flags().IntVar(&tokens, "tokens", 0, "Total tokens used")
	cmd.Flags().Float64Var(&costUSD, "cost", 0, "Estimated cost in USD")
	cmd.Flags().StringVar(&whatWentWell, "what-went-well", "", "Narrative: what went well (agent-provided)")
	cmd.Flags().StringVar(&whatDidnt, "what-didnt", "", "Narrative: what didn't go well (agent-provided)")
	cmd.Flags().StringVar(&lessonsLearned, "lessons-learned", "", "Narrative: lessons learned (agent-provided)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	_ = cmd.MarkFlagRequired("issue")
	_ = cmd.MarkFlagRequired("status")

	return cmd
}

func knowledgeValidateCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "validate <issue-number>",
		Short:        "Validate that decisions.md is populated when the plan has tradeoff signals",
		SilenceUsage: true,
		Long: `Validate decisions.md population for a given issue.

When knowledge.require_decisions is true in .nightgauge/config.yaml, exits non-zero
if the plan for <issue-number> contains 2+ distinct tradeoff keywords but decisions.md
lacks at least one ADR block (with Status, Context, Decision, Consequences fields).

Tradeoff keywords are loaded from configs/knowledge-tradeoff-keywords.yaml (falls back to
built-in defaults when the file is absent).

Exits 0 when validation passes. Exits 1 when validation fails with an actionable message
listing tradeoff signal locations and an ADR block template.

To disable this gate: set knowledge.require_decisions: false in .nightgauge/config.yaml`,
		Args: cobra.ExactArgs(1),
		Example: `  nightgauge knowledge validate 42
  nightgauge knowledge validate 42 --json
  nightgauge knowledge validate 42 --workdir /path/to/repo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			issueNumber := 0
			if _, err := fmt.Sscanf(args[0], "%d", &issueNumber); err != nil || issueNumber <= 0 {
				return fmt.Errorf("<issue-number> must be a positive integer, got %q", args[0])
			}

			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			// Load knowledge config from project config.yaml; use safe defaults on error.
			cfg, loadErr := config.Load(workdir)
			var knowledgeCfg *config.KnowledgeConfig
			if loadErr != nil {
				knowledgeCfg = &config.KnowledgeConfig{}
			} else {
				knowledgeCfg = cfg.Knowledge
				if knowledgeCfg == nil {
					knowledgeCfg = &config.KnowledgeConfig{}
				}
			}

			start := time.Now()
			result, valErr := knowledge.ValidateDecisionsPopulation(issueNumber, workdir, knowledgeCfg)

			validateStatus := "success"
			if valErr != nil {
				validateStatus = "failure"
			}
			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:        telemetry.EventValidate,
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				DurationMs:  time.Since(start).Milliseconds(),
				Status:      validateStatus,
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			if valErr != nil {
				fmt.Fprintln(os.Stderr, result.Message)
				return fmt.Errorf("validation failed for issue #%d", issueNumber)
			}

			fmt.Println(result.Message)
			return nil
		},
	}

	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")

	return cmd
}
