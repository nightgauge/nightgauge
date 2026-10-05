package workspacecmd

// `nightgauge workspace contract rollout|status` (#1480): roll one contract
// manifest out to every repository of the workspace, one pull request per
// repository after its own local gate, and one status table. The procedure
// and the manifest schema are in docs/MULTI_REPO_WORKSPACE.md § Contract
// rollout; the primitives are internal/contractrollout.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/contractrollout"
	gh "github.com/nightgauge/nightgauge/internal/github"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/nightgauge/nightgauge/internal/workspacemanifest"
	"github.com/spf13/cobra"
)

func contractCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "contract",
		Short: "Roll a cross-repository contract out to every workspace repository",
		Long: `contract rollout|status distributes one contract manifest (files copied
byte for byte, labels, a CI job, a pull request body) to every target
repository: one pull request per repository, opened only after that
repository's own local gate passed, and one status table for all of them.
See docs/MULTI_REPO_WORKSPACE.md § Contract rollout.`,
	}
	cmd.AddCommand(contractRolloutCmd())
	cmd.AddCommand(contractStatusCmd())
	return cmd
}

type contractFlags struct {
	targets []string
	root    string
	jsonOut bool
}

func (f *contractFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.targets, "target", nil, "Target repository as owner/name or owner/name=path (repeatable; replaces the manifest's targets)")
	cmd.Flags().StringVar(&f.root, "root", "", "Workspace root (default: auto-detect from CWD)")
	cmd.Flags().BoolVar(&f.jsonOut, "json", false, "Output the status rows as JSON")
}

func contractRolloutCmd() *cobra.Command {
	var (
		f       contractFlags
		apply   bool
		workDir string
	)
	cmd := &cobra.Command{
		Use:   "rollout <contract.yaml>",
		Short: "Plan, or with --apply open, one pull request per target repository",
		Long: `rollout reads the contract manifest and, for each target repository:
provisions the contract's labels, copies its files byte for byte and inserts
its CI job in a fresh worktree off origin/<base>, commits, runs the
repository's own local gate (the target's gate, else scripts/ci-local.sh),
and only when the gate passes pushes the branch and opens a pull request.
A repository already compliant gets nothing; one whose gate fails keeps its
worktree for inspection and gets no pull request.

Without --apply it plans: it reads each checkout's working tree and the
forge's labels and pull requests, and changes nothing.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, resolve, err := loadContract(args[0], f)
			if err != nil {
				return err
			}
			forge, err := newGitHubContractForge()
			if err != nil {
				return err
			}
			if apply && workDir == "" {
				if workDir, err = os.MkdirTemp("", "nightgauge-contract-"+c.Name+"-"); err != nil {
					return err
				}
			}
			rows := contractrollout.Rollout(cmd.Context(), c, contractrollout.Options{
				Apply:       apply,
				WorkDir:     workDir,
				ResolvePath: resolve,
				Forge:       forge,
				Log:         cmd.ErrOrStderr(),
			})
			return printContractRows(cmd, rows, f.jsonOut)
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&apply, "apply", false, "Push branches and open pull requests (default: plan only)")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "Directory for the per-target worktrees (default: a new temporary directory)")
	return cmd
}

func contractStatusCmd() *cobra.Command {
	var f contractFlags
	cmd := &cobra.Command{
		Use:   "status <contract.yaml>",
		Short: "Report each target's pull request and CI checks as one table",
		Long: `status reads each target's pull request from the contract's branch and
its CI check rollup. A target is rolled-out once its pull request merged, or
is open with every check green.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, err := loadContract(args[0], f)
			if err != nil {
				return err
			}
			forge, err := newGitHubContractForge()
			if err != nil {
				return err
			}
			return printContractRows(cmd, contractrollout.Refresh(cmd.Context(), c, forge), f.jsonOut)
		},
	}
	f.register(cmd)
	return cmd
}

// printContractRows writes the status table, and fails the command when any
// target is not rolled out, compliant or planned.
func printContractRows(cmd *cobra.Command, rows []contractrollout.TargetStatus, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return err
		}
	} else {
		contractrollout.WriteTable(cmd.OutOrStdout(), rows)
	}
	bad := 0
	for _, r := range rows {
		switch r.Status {
		case contractrollout.StatusError, contractrollout.StatusGateFailed,
			contractrollout.StatusGateMissing, contractrollout.StatusCIFailed:
			bad++
		}
	}
	if bad > 0 {
		cmd.SilenceUsage = true
		return fmt.Errorf("%d of %d target(s) need attention", bad, len(rows))
	}
	return nil
}

// loadContract loads the manifest, settles its targets (--target, else the
// manifest's, else every workspace repository) and returns how each
// target's checkout is found.
func loadContract(manifest string, f contractFlags) (*contractrollout.Contract, func(contractrollout.Target) (string, error), error) {
	c, err := contractrollout.Load(manifest)
	if err != nil {
		return nil, nil, err
	}
	members, wsRoot := workspaceMembers(f.root)

	targets := c.Targets
	if len(f.targets) > 0 {
		targets = nil
		for _, spec := range f.targets {
			repo, path, _ := strings.Cut(spec, "=")
			targets = append(targets, contractrollout.Target{Repo: repo, Path: path})
		}
	}
	if len(targets) == 0 {
		for _, m := range members {
			targets = append(targets, contractrollout.Target{Repo: m.repo, Path: m.path})
		}
	}
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("%s names no targets, no --target was given, and no workspace manifest was found from here", manifest)
	}
	if err := c.SetTargets(targets); err != nil {
		return nil, nil, err
	}

	resolve := func(t contractrollout.Target) (string, error) {
		p := t.Path
		if p == "" {
			for _, m := range members {
				if strings.EqualFold(m.repo, t.Repo) {
					p = m.path
				}
			}
		}
		if p == "" {
			return "", fmt.Errorf("no checkout for %s: give the target a path, or add it to the workspace manifest", t.Repo)
		}
		if !filepath.IsAbs(p) {
			base := c.Dir()
			if wsRoot != "" {
				base = wsRoot
			}
			p = filepath.Join(base, p)
		}
		if _, err := os.Stat(filepath.Join(p, ".git")); err != nil {
			return "", fmt.Errorf("%s: %s is not a git checkout", t.Repo, p)
		}
		return p, nil
	}
	return c, resolve, nil
}

type workspaceMember struct{ repo, path string }

// workspaceMembers lists the workspace manifest's repositories as
// owner/name with their checkout paths (absolute). owner/name comes from the
// member's own .nightgauge/config.yaml, as provision-board-sync resolves it;
// a member whose owner is unknown is left out.
func workspaceMembers(rootFlag string) ([]workspaceMember, string) {
	root := rootFlag
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, ""
		}
		if root, err = workspace.DetectWorkspaceRoot(wd); err != nil {
			return nil, ""
		}
	}
	m, err := workspacemanifest.Load(workspacemanifest.ManifestPath(root))
	if err != nil {
		return nil, ""
	}
	var out []workspaceMember
	for _, e := range m.Entries {
		if e.Path == "" {
			continue
		}
		path := filepath.Join(root, e.Path)
		owner, repo := "", e.Name
		if cfg, err := config.Load(path); err == nil && cfg != nil {
			owner = cfg.Owner
			if cfg.DefaultRepo != "" {
				repo = cfg.DefaultRepo
			}
		}
		if owner == "" {
			continue
		}
		out = append(out, workspaceMember{repo: owner + "/" + repo, path: path})
	}
	return out, root
}

// gitHubContractForge is contractrollout.Forge on the GitHub client.
type gitHubContractForge struct {
	client *gh.Client
	prs    *gh.PRService
}

func newGitHubContractForge() (*gitHubContractForge, error) {
	client, err := gh.NewClient()
	if err != nil {
		return nil, fmt.Errorf("contract: create GitHub client: %w", err)
	}
	return &gitHubContractForge{client: client, prs: gh.NewPRService(client)}, nil
}

func splitRepo(repo string) (string, string) {
	owner, name, _ := strings.Cut(repo, "/")
	return owner, name
}

func (g *gitHubContractForge) Labels(repo string) contractrollout.LabelClient {
	owner, name := splitRepo(repo)
	return gitHubContractLabels{svc: gh.NewLabelService(g.client, owner, name)}
}

func (g *gitHubContractForge) FindPR(ctx context.Context, repo, head string) (*contractrollout.PR, error) {
	owner, name := splitRepo(repo)
	for _, state := range []string{"OPEN", "MERGED"} {
		prs, err := g.prs.ListPRs(ctx, owner, name, state, head)
		if err != nil {
			return nil, err
		}
		if len(prs) > 0 {
			p := prs[0]
			return &contractrollout.PR{Number: p.Number, URL: p.URL, State: strings.ToUpper(p.State), Checks: p.CheckStatus}, nil
		}
	}
	return nil, nil
}

func (g *gitHubContractForge) CreatePR(ctx context.Context, repo, head, base, title, body string) (*contractrollout.PR, error) {
	owner, name := splitRepo(repo)
	id, err := g.client.GetRepositoryID(ctx, owner, name)
	if err != nil {
		return nil, err
	}
	p, err := g.prs.CreatePR(ctx, id, title, body, head, base)
	if err != nil {
		return nil, err
	}
	return &contractrollout.PR{Number: p.Number, URL: p.URL, State: "OPEN"}, nil
}

type gitHubContractLabels struct{ svc *gh.LabelService }

func (l gitHubContractLabels) List(ctx context.Context) ([]contractrollout.Label, error) {
	have, err := l.svc.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contractrollout.Label, 0, len(have))
	for _, h := range have {
		out = append(out, contractrollout.Label{Name: h.Name, Color: h.Color, Description: h.Description})
	}
	return out, nil
}

func (l gitHubContractLabels) Create(ctx context.Context, want contractrollout.Label) error {
	_, err := l.svc.Create(ctx, want.Name, want.Description, want.Color)
	return err
}
