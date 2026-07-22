package workspacecmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/nightgauge/nightgauge/internal/config"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/spf13/cobra"
	yaml "gopkg.in/yaml.v3"
)

// memberRepo is one resolved member of a multi-repo workspace, carrying the
// canonical owner/repo/project that board-sync workflows target. Owner, Repo and
// Project come from the member's own .nightgauge/config.yaml when present,
// falling back to the manifest entry. A shared GitHub Project (N:1 topology) is
// expressed by every member resolving to the same Project number.
type memberRepo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Owner     string `json:"owner"`
	Repo      string `json:"repo"`
	OwnerType string `json:"ownerType"`
	Project   int    `json:"project"`
	Primary   bool   `json:"primary"`
}

// boardSyncPlan is the resolved, deterministic provisioning plan for a workspace.
type boardSyncPlan struct {
	WorkspaceName string        `json:"workspaceName"`
	Root          string        `json:"root"`
	PrimaryRepo   string        `json:"primaryRepo"`
	Members       []memberRepo  `json:"members"`
	Files         []plannedFile `json:"files"`
}

// plannedFile is one workflow file the provisioner will (or did) write.
type plannedFile struct {
	Path    string `json:"path"`    // absolute path on disk
	Kind    string `json:"kind"`    // lifecycle-sweep | epic-sweep | board-done
	Repo    string `json:"repo"`    // owner/repo this file lives in
	Written bool   `json:"written"` // true when --write actually wrote it
	content string `json:"-"`
}

// provisionTemplateData feeds the workflow templates. Templates use << >> delims
// so GitHub Actions' own ${{ ... }} expressions pass through verbatim.
type provisionTemplateData struct {
	WorkspaceName string
	Runner        string
	InstallCmd    string
	TokenSecret   string
	// For the primary sweeps: the full member list.
	Members []memberRepo
	// For a per-repo board-done workflow: the repo this file is installed in.
	Self memberRepo
}

func provisionBoardSyncCmd() *cobra.Command {
	var (
		write       bool
		printFull   bool
		jsonOut     bool
		rootFlag    string
		runner      string
		tokenSecret string
		installCmd  string
	)

	cmd := &cobra.Command{
		Use:   "provision-board-sync",
		Short: "Generate board-sync GitHub Actions workflows for every repo in a multi-repo workspace",
		Long: `provision-board-sync reads .vscode/nightgauge-workspace.yaml (walking up
from CWD, or --root) and generates the project-board lifecycle automation that a
shared GitHub Project needs but a member repo does not get on its own:

  - Lifecycle Sweep + Epic Sweep (nightly) — installed in the PRIMARY member repo,
    iterating every member repo against the shared project. Catches board-status
    drift, stale blockers, premature/missing Done, and completed-but-open epics.

  - Board Sync on close (per-event) — installed in EVERY member repo, reconciling
    that repo's just-closed issue/PR to Done on the shared project so Done does not
    depend solely on the nightly sweep.

The shared project is resolved per member from its own .nightgauge/config.yaml
(N:1 topology: all members resolve to the same project number). The generated
workflows install the CLI from the Homebrew tap and authenticate with a token
secret you provide (--token-secret) that must carry project + issues:write across
every member repo's owner.

Default is a dry run that prints the plan. Pass --write to create the files.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runProvisionBoardSync(cmd, provisionOpts{
				write:       write,
				printFull:   printFull,
				jsonOut:     jsonOut,
				root:        rootFlag,
				runner:      runner,
				tokenSecret: tokenSecret,
				installCmd:  installCmd,
			})
		},
	}

	cmd.Flags().BoolVar(&write, "write", false, "Write the workflow files (default: dry-run preview)")
	cmd.Flags().BoolVar(&printFull, "print", false, "Print full rendered workflow contents")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the plan as JSON")
	cmd.Flags().StringVar(&rootFlag, "root", "", "Workspace root (default: auto-detect from CWD)")
	cmd.Flags().StringVar(&runner, "runner", "self-hosted", "runs-on value for the generated jobs")
	cmd.Flags().StringVar(&tokenSecret, "token-secret", "BOARD_SYNC_TOKEN", "Name of the Actions secret holding a cross-repo project+issues PAT")
	cmd.Flags().StringVar(&installCmd, "install-cmd", defaultInstallCmd, "Shell command that installs the nightgauge CLI onto PATH")

	return cmd
}

const defaultInstallCmd = "brew install nightgauge/tap/nightgauge 2>/dev/null || brew upgrade nightgauge/tap/nightgauge 2>/dev/null || true"

type provisionOpts struct {
	write       bool
	printFull   bool
	jsonOut     bool
	root        string
	runner      string
	tokenSecret string
	installCmd  string
}

func runProvisionBoardSync(cmd *cobra.Command, opts provisionOpts) error {
	root := opts.root
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("provision-board-sync: getwd: %w", err)
		}
		detected, derr := workspace.DetectWorkspaceRoot(wd)
		if derr != nil {
			return fmt.Errorf("provision-board-sync: no .vscode/nightgauge-workspace.yaml found (this command is for multi-repo workspaces): %w", derr)
		}
		root = detected
	}

	plan, err := buildBoardSyncPlan(root, opts)
	if err != nil {
		return fmt.Errorf("provision-board-sync: %w", err)
	}

	if opts.write {
		for i := range plan.Files {
			f := &plan.Files[i]
			if mkErr := os.MkdirAll(filepath.Dir(f.Path), 0o755); mkErr != nil {
				return fmt.Errorf("provision-board-sync: mkdir %s: %w", filepath.Dir(f.Path), mkErr)
			}
			if wErr := os.WriteFile(f.Path, []byte(f.content), 0o644); wErr != nil {
				return fmt.Errorf("provision-board-sync: write %s: %w", f.Path, wErr)
			}
			f.Written = true
		}
	}

	if opts.jsonOut {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(plan)
	}

	printPlanHuman(cmd, plan, opts)
	return nil
}

// buildBoardSyncPlan resolves the workspace members and renders every workflow
// file, returning a deterministic plan (members and files are sorted).
func buildBoardSyncPlan(root string, opts provisionOpts) (boardSyncPlan, error) {
	manifestPath := filepath.Join(root, ".vscode", "nightgauge-workspace.yaml")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return boardSyncPlan{}, fmt.Errorf("read manifest %s: %w", manifestPath, err)
	}

	var manifest struct {
		Workspace struct {
			Name string `yaml:"name"`
		} `yaml:"workspace"`
		Repositories []struct {
			Name          string `yaml:"name"`
			Path          string `yaml:"path"`
			Role          string `yaml:"role"`
			ProjectNumber int    `yaml:"project_number"`
		} `yaml:"repositories"`
		Routing struct {
			DefaultRepository string `yaml:"default_repository"`
		} `yaml:"routing"`
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return boardSyncPlan{}, fmt.Errorf("parse manifest %s: %w", manifestPath, err)
	}

	// Resolve each member against its own config.yaml (canonical owner/repo/project).
	var members []memberRepo
	for _, r := range manifest.Repositories {
		if r.Path == "" {
			continue
		}
		m := memberRepo{Name: r.Name, Path: r.Path, Repo: r.Name, Project: r.ProjectNumber, OwnerType: "org"}
		if cfg, cfgErr := config.Load(filepath.Join(root, r.Path)); cfgErr == nil && cfg != nil {
			if cfg.Owner != "" {
				m.Owner = cfg.Owner
			}
			if cfg.DefaultRepo != "" {
				m.Repo = cfg.DefaultRepo
			}
			if cfg.ProjectNumber > 0 {
				m.Project = cfg.ProjectNumber
			}
			if cfg.OwnerType != "" {
				m.OwnerType = cfg.OwnerType
			}
		}
		if m.Owner == "" || m.Repo == "" || m.Project == 0 {
			// Not enough to target a board — skip but keep going so a partial
			// workspace still provisions the repos that are fully configured.
			continue
		}
		members = append(members, m)
	}
	if len(members) == 0 {
		return boardSyncPlan{}, fmt.Errorf("manifest %s yielded no members with owner+repo+project (check each member's .nightgauge/config.yaml)", manifestPath)
	}

	// Pick the primary: routing.default_repository → first role:primary → first.
	primaryIdx := -1
	if def := manifest.Routing.DefaultRepository; def != "" {
		for i, m := range members {
			if m.Name == def || m.Repo == def {
				primaryIdx = i
				break
			}
		}
	}
	if primaryIdx < 0 {
		for _, r := range manifest.Repositories {
			if r.Role != "primary" {
				continue
			}
			for i, m := range members {
				if m.Name == r.Name {
					primaryIdx = i
					break
				}
			}
			if primaryIdx >= 0 {
				break
			}
		}
	}
	if primaryIdx < 0 {
		primaryIdx = 0
	}
	members[primaryIdx].Primary = true

	// Deterministic member order: owner/repo ascending (primary flag preserved).
	sort.Slice(members, func(i, j int) bool {
		return members[i].Owner+"/"+members[i].Repo < members[j].Owner+"/"+members[j].Repo
	})

	var primary memberRepo
	for _, m := range members {
		if m.Primary {
			primary = m
			break
		}
	}

	td := provisionTemplateData{
		WorkspaceName: strings.TrimSpace(manifest.Workspace.Name),
		Runner:        opts.runner,
		InstallCmd:    opts.installCmd,
		TokenSecret:   opts.tokenSecret,
		Members:       members,
	}
	if td.WorkspaceName == "" {
		td.WorkspaceName = "Workspace"
	}

	plan := boardSyncPlan{
		WorkspaceName: td.WorkspaceName,
		Root:          root,
		PrimaryRepo:   primary.Owner + "/" + primary.Repo,
		Members:       members,
	}

	// Primary repo: the two nightly sweeps.
	lifecycle, err := render(lifecycleSweepTemplate, td)
	if err != nil {
		return boardSyncPlan{}, err
	}
	epic, err := render(epicSweepTemplate, td)
	if err != nil {
		return boardSyncPlan{}, err
	}
	primaryWF := filepath.Join(root, primary.Path, ".github", "workflows")
	plan.Files = append(plan.Files,
		plannedFile{Path: filepath.Join(primaryWF, "nightgauge-lifecycle-sweep.yml"), Kind: "lifecycle-sweep", Repo: plan.PrimaryRepo, content: lifecycle},
		plannedFile{Path: filepath.Join(primaryWF, "nightgauge-epic-sweep.yml"), Kind: "epic-sweep", Repo: plan.PrimaryRepo, content: epic},
	)

	// Every member repo: a per-event board-done reconciler for its own closes.
	for _, m := range members {
		mtd := td
		mtd.Self = m
		body, rErr := render(boardDoneTemplate, mtd)
		if rErr != nil {
			return boardSyncPlan{}, rErr
		}
		wf := filepath.Join(root, m.Path, ".github", "workflows", "nightgauge-board-done.yml")
		plan.Files = append(plan.Files, plannedFile{Path: wf, Kind: "board-done", Repo: m.Owner + "/" + m.Repo, content: body})
	}

	return plan, nil
}

func render(tmpl string, td provisionTemplateData) (string, error) {
	t, err := template.New("wf").Delims("<<", ">>").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, td); err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return buf.String(), nil
}

func printPlanHuman(cmd *cobra.Command, plan boardSyncPlan, opts provisionOpts) {
	out := cmd.OutOrStdout()
	mode := "DRY RUN (pass --write to create files)"
	if opts.write {
		mode = "WROTE FILES"
	}
	fmt.Fprintf(out, "Board-sync provisioning for %q — %s\n", plan.WorkspaceName, mode)
	fmt.Fprintf(out, "Workspace root: %s\n", plan.Root)
	fmt.Fprintf(out, "Primary repo:   %s\n\n", plan.PrimaryRepo)

	fmt.Fprintln(out, "Members (all targeting their shared project):")
	for _, m := range plan.Members {
		flag := ""
		if m.Primary {
			flag = "  [primary]"
		}
		fmt.Fprintf(out, "  - %s/%s → project #%d%s\n", m.Owner, m.Repo, m.Project, flag)
	}
	fmt.Fprintln(out)

	fmt.Fprintln(out, "Files:")
	for _, f := range plan.Files {
		status := "would write"
		if f.Written {
			status = "wrote"
		}
		fmt.Fprintf(out, "  [%s] %-15s %s\n", status, f.Kind, f.Path)
	}

	fmt.Fprintf(out, "\nToken secret required in every member repo: %s\n", opts.tokenSecret)
	fmt.Fprintln(out, "  (a PAT with project write + issues:write across all member-repo owners)")

	if opts.printFull {
		for _, f := range plan.Files {
			fmt.Fprintf(out, "\n==================== %s ====================\n%s\n", f.Path, f.content)
		}
	}
}
