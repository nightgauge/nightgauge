package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	spikepkg "github.com/nightgauge/nightgauge/internal/cmd/spike"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// spikeCmd is the top-level "spike" command family.
//
// Subcommands:
//   - materialize <issue-number>: parse the spike artifact and create follow-up
//     issues per docs/SPIKE_CONTRACT.md.
func spikeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spike",
		Short: "Spike pipeline operations (materialize follow-ups, dry-run validation)",
		Long: `Tooling for type:spike issues. Materializes the YAML recommendations block in a
spike artifact (docs/spikes/<N>-*.md) into actionable follow-up GitHub issues
linked as sub-issues of the spike. See docs/SPIKE_CONTRACT.md.`,
	}
	cmd.AddCommand(spikeMaterializeCmd())
	cmd.AddCommand(spikeValidateCmd())
	return cmd
}

// spikeMaterializeCmd implements `nightgauge spike materialize <N>`.
func spikeMaterializeCmd() *cobra.Command {
	var (
		owner         string
		repo          string
		projectNumber int
		artifactPath  string
		workdir       string
		dryRun        bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:          "materialize [issue-number]",
		Short:        "Create follow-up issues from a spike artifact",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			number := 0
			if _, err := fmt.Sscanf(args[0], "%d", &number); err != nil || number <= 0 {
				return fmt.Errorf("invalid issue number: %s", args[0])
			}

			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			path := artifactPath
			if path == "" {
				located, err := spikepkg.LocateArtifact(workdir, number)
				if err != nil {
					return fmt.Errorf("locate artifact for spike #%d: %w", number, err)
				}
				path = located
			}

			art, err := spikepkg.ParseArtifact(path)
			if err != nil {
				return fmt.Errorf("parse artifact %s: %w", path, err)
			}
			if art.Spike != number {
				return fmt.Errorf("artifact %s declares spike=%d but command targeted #%d", path, art.Spike, number)
			}
			if err := spikepkg.ValidateSchema(art); err != nil {
				return fmt.Errorf("validate artifact %s: %w", path, err)
			}

			ownerPart, repoPart := splitRepo(owner, repo)
			repoFull := ownerPart + "/" + repoPart

			var materializer spikepkg.Materializer
			if dryRun {
				materializer = &noopMaterializer{}
			} else {
				client, err := clientFromConfig()
				if err != nil {
					return fmt.Errorf("create GitHub client: %w", err)
				}
				materializer = newGitHubMaterializer(client, ownerPart, repoPart, projectNumber, getOwnerType(cmd))
			}

			res, err := spikepkg.Materialize(cmd.Context(), art, repoFull, materializer, dryRun)
			if err != nil {
				return enrichError(err)
			}

			if outputJSON {
				return printJSON(res)
			}
			renderMaterializeHuman(res, path)
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "nightgauge", "GitHub repository owner (defaults to config)")
	cmd.Flags().StringVar(&repo, "repo", "nightgauge", "GitHub repository name (defaults to config)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "Project board number (defaults to config)")
	cmd.Flags().StringVar(&artifactPath, "artifact-path", "", "Override the artifact path (default: docs/spikes/<N>-*.md)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Validate and plan only — no GraphQL mutations")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

// spikeValidateCmd implements `nightgauge spike validate`.
// It reads a spike issue body from --body-file or stdin and validates it
// against the spike contract: yaml recommendations block, schema, Path
// declaration, and artifact path. See docs/SPIKE_CONTRACT.md.
func spikeValidateCmd() *cobra.Command {
	var bodyFile string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate a spike issue body before creation",
		Long: `Validates a spike issue body string (read from --body-file or stdin)
against the spike contract: checks for a yaml recommendations block,
validates the schema, and enforces a Path A/B/C declaration.

Exit 0: valid. Exit 1: invalid (prints human-readable error).

See docs/SPIKE_CONTRACT.md for the full contract.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var data []byte
			var err error
			if bodyFile != "" {
				data, err = os.ReadFile(bodyFile)
			} else {
				data, err = io.ReadAll(cmd.InOrStdin())
			}
			if err != nil {
				return fmt.Errorf("read body: %w", err)
			}
			if err := spikepkg.ValidateBody(string(data)); err != nil {
				fmt.Fprintf(os.Stderr, "spike validate: %v\n\nSee docs/SPIKE_CONTRACT.md for remediation.\n", err)
				os.Exit(1)
			}
			fmt.Println("spike validate: OK")
			return nil
		},
	}
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "Path to file containing the spike issue body (default: stdin)")
	return cmd
}

func renderMaterializeHuman(res *spikepkg.MaterializeResult, path string) {
	mode := "live"
	if res.DryRun {
		mode = "dry-run"
	}
	fmt.Printf("Spike materialize (%s) — spike #%d, artifact %s\n", mode, res.Spike, path)
	if len(res.Issues) == 0 {
		fmt.Println("  (no recommendations)")
		return
	}
	for _, mi := range res.Issues {
		switch {
		case mi.Skipped:
			fmt.Printf("  · %s  [skip]  %s\n", mi.ID, mi.Title)
		case mi.AlreadyExists:
			fmt.Printf("  ✓ %s  [%s]  #%d %s (already materialized)\n", mi.ID, mi.Action, mi.IssueNumber, mi.Title)
		case mi.DryRun:
			fmt.Printf("  → %s  [%s]  would create: %s\n", mi.ID, mi.Action, mi.Title)
		default:
			fmt.Printf("  ✓ %s  [%s]  #%d %s\n", mi.ID, mi.Action, mi.IssueNumber, mi.Title)
		}
	}
	if len(res.BlockedBy) > 0 {
		fmt.Println("  Dependencies:")
		for _, e := range res.BlockedBy {
			fmt.Printf("    %s blocked by %s\n", e.BlockedID, e.BlockerID)
		}
	}
}

// noopMaterializer satisfies the Materializer interface for dry-run mode
// without contacting GitHub. FindExistingByID always reports "not found" so
// the planner sees every recommendation as a new issue. This is a deliberate
// choice for offline dry-run preview — running without --dry-run with a
// credentialed client provides true idempotency detection.
type noopMaterializer struct{}

func (n *noopMaterializer) FindExistingByID(_ context.Context, _ int, _ string) (int, string, error) {
	return 0, "", nil
}
func (n *noopMaterializer) CreateIssue(_ context.Context, _ int, _ spikepkg.Recommendation, _ string) (int, string, error) {
	return 0, "", fmt.Errorf("noop materializer cannot create issues")
}
func (n *noopMaterializer) AddBlockedByByNumber(_ context.Context, _, _ int) error {
	return fmt.Errorf("noop materializer cannot add blockedBy")
}

// githubMaterializer is the live Materializer that talks to GitHub.
type githubMaterializer struct {
	client    *gh.Client
	owner     string
	repo      string
	ownerType gh.OwnerType
	projNum   int
	issueSvc  *gh.IssueService
	projSvc   *gh.ProjectService
}

func newGitHubMaterializer(client *gh.Client, owner, repo string, projectNumber int, ownerType gh.OwnerType) *githubMaterializer {
	return &githubMaterializer{
		client:    client,
		owner:     owner,
		repo:      repo,
		ownerType: ownerType,
		projNum:   projectNumber,
		issueSvc:  gh.NewIssueService(client),
		projSvc:   gh.NewProjectService(client, owner, projectNumber, ownerType),
	}
}

func (g *githubMaterializer) FindExistingByID(ctx context.Context, spikeNumber int, id string) (int, string, error) {
	spike, err := g.issueSvc.GetIssue(ctx, g.owner, g.repo, spikeNumber)
	if err != nil {
		return 0, "", fmt.Errorf("fetch spike #%d: %w", spikeNumber, err)
	}
	marker := spikepkg.MarkerFor(spikeNumber, id)
	for _, sub := range spike.SubIssues {
		subOwner, subRepo := g.owner, g.repo
		if parts := strings.SplitN(sub.Repo, "/", 2); len(parts) == 2 && parts[0] != "" {
			subOwner, subRepo = parts[0], parts[1]
		}
		fullSub, err := g.issueSvc.GetIssue(ctx, subOwner, subRepo, sub.Number)
		if err != nil {
			continue
		}
		if strings.Contains(fullSub.Body, marker) {
			return fullSub.Number, fullSub.URL, nil
		}
	}
	return 0, "", nil
}

func (g *githubMaterializer) CreateIssue(ctx context.Context, spikeNumber int, rec spikepkg.Recommendation, body string) (int, string, error) {
	labelNames := append([]string{"type:" + rec.Type}, rec.Labels...)
	repoLabels, err := g.issueSvc.GetRepoLabels(ctx, g.owner, g.repo)
	if err != nil {
		return 0, "", fmt.Errorf("fetch repo labels: %w", err)
	}
	var labelIDs []string
	for _, name := range labelNames {
		if id, ok := repoLabels[name]; ok {
			labelIDs = append(labelIDs, id)
		}
		// Missing labels are tolerated — the issue is created without them.
	}

	repoID, err := g.client.GetRepositoryID(ctx, g.owner, g.repo)
	if err != nil {
		return 0, "", fmt.Errorf("fetch repo id: %w", err)
	}

	issue, err := g.issueSvc.CreateIssue(ctx, repoID, rec.Title, body, labelIDs)
	if err != nil {
		return 0, "", err
	}

	if spike, err := g.issueSvc.GetIssue(ctx, g.owner, g.repo, spikeNumber); err == nil {
		_ = g.issueSvc.AddSubIssue(ctx, spike.NodeID, issue.NodeID)
	}

	if itemID, err := g.projSvc.AddItem(ctx, issue.NodeID); err == nil {
		_ = g.projSvc.SetSingleSelectField(ctx, itemID, "Priority", priorityToBoardOption(rec.Priority))
		_ = g.projSvc.SetSingleSelectField(ctx, itemID, "Size", rec.Size)
		_ = g.projSvc.SetSingleSelectField(ctx, itemID, "Status", statusForAction(rec.Action))
	}

	return issue.Number, issue.URL, nil
}

func (g *githubMaterializer) AddBlockedByByNumber(ctx context.Context, blockedNumber, blockerNumber int) error {
	return g.projSvc.AddBlockedByNumber(ctx, g.owner, g.repo, blockedNumber, blockerNumber)
}

// priorityToBoardOption maps the contract's lowercase priority to the project
// board's "P0..P3" option names (mirrors syncPriorityLabel in project.go).
func priorityToBoardOption(priority string) string {
	switch priority {
	case "critical":
		return "P0"
	case "high":
		return "P1"
	case "medium":
		return "P2"
	case "low":
		return "P3"
	}
	return "P2"
}

// statusForAction returns the board Status name for an action: adopt → Ready,
// defer → Backlog. skip is filtered earlier so it never reaches here.
func statusForAction(action string) string {
	if action == "adopt" {
		return "Ready"
	}
	return "Backlog"
}
