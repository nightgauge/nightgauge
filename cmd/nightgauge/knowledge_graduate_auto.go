package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	forgecmd "github.com/nightgauge/nightgauge/cmd/nightgauge/forge"
	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	gitpkg "github.com/nightgauge/nightgauge/internal/git"
	"github.com/nightgauge/nightgauge/internal/knowledge/graduation"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

// autoCLIOptions captures the flags read off knowledgeGraduateCmd for auto
// mode. Kept in a dedicated struct so the flag-wiring stays in knowledge.go
// while the orchestration shim stays here.
type autoCLIOptions struct {
	Workdir       string
	ADRIndex      int
	DryRun        bool
	AllCandidates bool
	OutputJSON    bool
	BaseBranch    string
	Owner         string
	Repo          string
}

// runGraduateAuto is the entrypoint dispatched from knowledgeGraduateCmd when
// --auto is set. Builds the deterministic services (git, forge), invokes the
// orchestrator, prints results, and emits telemetry per processed candidate.
func runGraduateAuto(cmd *cobra.Command, issueNumber int, opts autoCLIOptions) error {
	if opts.Workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("get working directory: %w", err)
		}
		opts.Workdir = wd
	}

	// In --dry-run mode the orchestrator does not touch git or the forge, so
	// skip opening either. This lets the dry-run path work in non-git
	// contexts (e.g., CI smoke tests against a fixture tree).
	var (
		svc *gitpkg.Service
		git graduation.GitService
	)
	if opts.DryRun {
		git = stubGitService{}
	} else {
		var err error
		svc, err = gitpkg.NewService(opts.Workdir)
		if err != nil {
			return fmt.Errorf("open git repo at %s: %w", opts.Workdir, err)
		}
		git = newGitAutoAdapter(svc, opts.Workdir)
	}

	var forgeAdapter graduation.ForgeClient
	if !opts.DryRun {
		client, err := resolveForgeForGraduate(cmd)
		if err != nil {
			return fmt.Errorf("resolve forge client: %w", err)
		}
		forgeAdapter = newForgeAutoAdapter(client)
	}

	if opts.BaseBranch == "" {
		// Best-effort default-branch resolution via the git service. Only
		// runs when we opened a real git repo (non-dry-run path).
		if svc != nil {
			if def, derr := svc.DefaultBranch(); derr == nil && def != "" {
				opts.BaseBranch = def
			}
		}
		if opts.BaseBranch == "" {
			opts.BaseBranch = "main"
		}
	}
	if (opts.Owner == "" || opts.Repo == "") && svc != nil {
		owner, repoName, rerr := resolveOwnerRepo(svc, cmd)
		if rerr == nil {
			if opts.Owner == "" {
				opts.Owner = owner
			}
			if opts.Repo == "" {
				opts.Repo = repoName
			}
		}
	}

	start := time.Now()
	res, err := graduation.AutoGraduate(context.Background(), graduation.AutoGraduateInput{
		WorkspaceRoot: opts.Workdir,
		IssueNumber:   issueNumber,
		ADRIndex:      opts.ADRIndex,
		DryRun:        opts.DryRun,
		AllCandidates: opts.AllCandidates,
		BaseBranch:    opts.BaseBranch,
		Owner:         opts.Owner,
		Repo:          opts.Repo,
		Git:           git,
		Forge:         forgeAdapter,
		Now:           time.Now,
	})
	duration := time.Since(start).Milliseconds()
	if err != nil {
		// Emit a failure telemetry event so aggregators surface the loss.
		emitKnowledgeTelemetry(opts.Workdir, telemetry.Event{
			Type:        telemetry.EventGraduate,
			Mode:        "auto",
			Scope:       fmt.Sprintf("issue:%d", issueNumber),
			IssueNumber: issueNumber,
			DurationMs:  duration,
			Status:      "failure",
			ErrorKind:   "auto_orchestration",
		})
		return err
	}

	// One telemetry event per processed candidate so aggregators can count
	// distinct graduations rather than command invocations.
	if len(res.PerCandidate) == 0 {
		emitKnowledgeTelemetry(opts.Workdir, telemetry.Event{
			Type:        telemetry.EventGraduate,
			Mode:        "auto",
			Scope:       fmt.Sprintf("issue:%d", issueNumber),
			IssueNumber: issueNumber,
			Path:        res.DecisionsPath,
			DurationMs:  duration,
			Status:      res.Status,
		})
	} else {
		perDur := duration / int64(len(res.PerCandidate))
		for _, o := range res.PerCandidate {
			status := "success"
			if o.Status == graduation.AutoStatusError {
				status = "failure"
			}
			emitKnowledgeTelemetry(opts.Workdir, telemetry.Event{
				Type:        telemetry.EventGraduate,
				Mode:        "auto",
				Scope:       fmt.Sprintf("issue:%d", issueNumber),
				IssueNumber: issueNumber,
				Path:        res.DecisionsPath,
				DurationMs:  perDur,
				Status:      status,
			})
		}
	}

	if opts.OutputJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}

	printAutoHuman(cmd, res)

	// Exit non-zero on tie_unresolved or error so CI/automation can detect.
	switch res.Status {
	case graduation.AutoStatusTieUnresolved:
		return fmt.Errorf("tie unresolved between ADR indexes: pass --adr-index <N> to select one")
	case graduation.AutoStatusError:
		return fmt.Errorf("at least one candidate failed; see per-candidate skip_reason in JSON output")
	case graduation.AutoStatusNoCandidates:
		return fmt.Errorf("no graduation candidates found for issue #%d", issueNumber)
	}
	return nil
}

func printAutoHuman(cmd *cobra.Command, res graduation.AutoGraduateResult) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Issue #%d  status=%s  decisions=%s\n", res.Issue, res.Status, res.DecisionsPath)
	if len(res.TiedADRIndexes) > 0 {
		fmt.Fprintf(out, "Tied ADR indexes: %v — pass --adr-index <N> to select one\n", res.TiedADRIndexes)
	}
	for _, o := range res.PerCandidate {
		fmt.Fprintf(out, "\n  %s — %s\n", o.ADRAnchor, o.ADRTitle)
		fmt.Fprintf(out, "    status:      %s\n", o.Status)
		fmt.Fprintf(out, "    destination: %s#%s\n", o.DestinationDoc, o.DestinationAnchor)
		fmt.Fprintf(out, "    branch:      %s\n", o.Branch)
		if o.PRNumber > 0 {
			fmt.Fprintf(out, "    PR:          #%d %s\n", o.PRNumber, o.PRURL)
		}
		if o.SkipReason != "" {
			fmt.Fprintf(out, "    note:        %s\n", o.SkipReason)
		}
	}
}

// resolveForgeForGraduate builds a forge.Router via the shared forgecmd
// helper and returns the ForgeClient targeting --forge / --repo (or the
// default).
func resolveForgeForGraduate(cmd *cobra.Command) (forge.ForgeClient, error) {
	forgeFlag := strFlag(cmd, "forge")
	repoSpec := strFlag(cmd, "repo")
	owner := strFlag(cmd, "owner")
	project := intFlag(cmd, "project")
	ownerType := strFlag(cmd, "owner-type")

	router, err := forgecmd.BuildRouter(owner, project, ownerType)
	if err != nil {
		return nil, err
	}
	return router.For(forgeFlag, repoSpec)
}

func strFlag(cmd *cobra.Command, name string) string {
	if f := cmd.Flag(name); f != nil {
		return f.Value.String()
	}
	return ""
}

func intFlag(cmd *cobra.Command, name string) int {
	f := cmd.Flag(name)
	if f == nil {
		return 0
	}
	var n int
	_, _ = fmt.Sscanf(f.Value.String(), "%d", &n)
	return n
}

// resolveOwnerRepo infers owner and repo by inspecting the origin remote URL
// when the user has not explicitly passed --owner / --repo. Returns the first
// error so the caller can fall back to user-supplied values.
func resolveOwnerRepo(svc *gitpkg.Service, cmd *cobra.Command) (string, string, error) {
	if cmd != nil {
		if v := strFlag(cmd, "repo"); v != "" {
			// "owner/repo" form
			for i := 0; i < len(v); i++ {
				if v[i] == '/' {
					return v[:i], v[i+1:], nil
				}
			}
		}
	}
	slug, err := svc.RemoteRepoSlug()
	if err != nil {
		return "", "", err
	}
	for i := 0; i < len(slug); i++ {
		if slug[i] == '/' {
			return slug[:i], slug[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid owner/repo slug: %q", slug)
}

// --- adapters wrapping the concrete services into the small interfaces -----

// stubGitService is a no-op GitService used during --dry-run so the
// orchestrator never needs to touch a real git repo. processCandidate only
// reads CurrentBranch before short-circuiting on DryRun, so the other
// methods return zero values and would be a contract violation if called.
type stubGitService struct{}

func (stubGitService) CurrentBranch() (string, error)         { return "", nil }
func (stubGitService) LocalBranchExists(string) (bool, error) { return false, nil }
func (stubGitService) BranchCreateFrom(string, string) error  { return nil }
func (stubGitService) BranchDelete(string) error              { return nil }
func (stubGitService) Checkout(string) error                  { return nil }
func (stubGitService) Commit(string) (string, error)          { return "", nil }
func (stubGitService) PushBranch(string) error                { return nil }

type gitAutoAdapter struct {
	svc  *gitpkg.Service
	root string
}

func newGitAutoAdapter(svc *gitpkg.Service, root string) *gitAutoAdapter {
	return &gitAutoAdapter{svc: svc, root: root}
}

func (g *gitAutoAdapter) CurrentBranch() (string, error) { return g.svc.CurrentBranch() }
func (g *gitAutoAdapter) LocalBranchExists(n string) (bool, error) {
	return g.svc.LocalBranchExists(n)
}
func (g *gitAutoAdapter) BranchCreateFrom(n, base string) error {
	return g.svc.BranchCreateFrom(n, base)
}
func (g *gitAutoAdapter) BranchDelete(n string) error     { return g.svc.BranchDelete(n) }
func (g *gitAutoAdapter) Checkout(b string) error         { return g.svc.Checkout(b) }
func (g *gitAutoAdapter) Commit(m string) (string, error) { return g.svc.Commit(m) }
func (g *gitAutoAdapter) PushBranch(n string) error       { return g.svc.PushBranch(n) }

type forgeAutoAdapter struct {
	client forge.ForgeClient
}

func newForgeAutoAdapter(c forge.ForgeClient) *forgeAutoAdapter {
	return &forgeAutoAdapter{client: c}
}

// GetRepoID resolves a repository's GraphQL node ID via the forge's
// GraphQLService surface. Adapters that implement forge.GraphQLService (the
// github adapter does) satisfy this via a one-shot query; adapters that
// don't return ErrUnsupported propagate it verbatim.
func (a *forgeAutoAdapter) GetRepoID(ctx context.Context, owner, repo string) (string, error) {
	gql, ok := a.client.(forge.GraphQLService)
	if !ok {
		return "", fmt.Errorf("forge client does not expose GraphQL surface (cannot resolve repo node ID)")
	}
	const query = `query($owner:String!,$name:String!){repository(owner:$owner,name:$name){id}}`
	raw, err := gql.ExecuteGraphQL(ctx, query, map[string]interface{}{"owner": owner, "name": repo})
	if err != nil {
		return "", fmt.Errorf("execute repo id query: %w", err)
	}
	var envelope struct {
		Data struct {
			Repository struct {
				ID string `json:"id"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", fmt.Errorf("parse repo id envelope: %w", err)
	}
	if envelope.Data.Repository.ID == "" {
		return "", fmt.Errorf("repository %s/%s returned empty node ID", owner, repo)
	}
	return envelope.Data.Repository.ID, nil
}

func (a *forgeAutoAdapter) ListOpenPRsForBranch(ctx context.Context, owner, repo, head string) ([]forgetypes.PullRequest, error) {
	return a.client.PRs().ListPRs(ctx, owner, repo, "OPEN", head)
}

func (a *forgeAutoAdapter) CreatePR(ctx context.Context, repoID, title, body, head, base string) (*forgetypes.PullRequest, error) {
	return a.client.PRs().CreatePR(ctx, repoID, title, body, head, base)
}

func (a *forgeAutoAdapter) UpdatePR(ctx context.Context, prID string, opts forge.UpdatePROptions) (*forgetypes.PullRequest, error) {
	return a.client.PRs().UpdatePR(ctx, prID, opts)
}

func (a *forgeAutoAdapter) AddProjectItem(ctx context.Context, contentNodeID string) (string, error) {
	return a.client.Project().AddItem(ctx, contentNodeID)
}

func (a *forgeAutoAdapter) SetProjectStatus(ctx context.Context, itemID, fieldName, optionName string) error {
	return a.client.Project().SetSingleSelectField(ctx, itemID, fieldName, optionName)
}
