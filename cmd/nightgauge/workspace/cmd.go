// Package workspacecmd implements the `nightgauge workspace` Cobra
// command tree. Today it exposes `workspace doctor` which validates the
// multi-forge workspace configuration and reports per-repo forge resolution.
package workspacecmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	forgecmd "github.com/nightgauge/nightgauge/cmd/nightgauge/forge"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// Cmd returns the top-level `workspace` Cobra command.
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workspace",
		Short: "Workspace management commands (multi-forge configuration, doctor checks)",
	}
	cmd.AddCommand(doctorCmd())
	cmd.AddCommand(syncPayloadCmd())
	cmd.AddCommand(reposFromProjectCmd())
	cmd.AddCommand(provisionBoardSyncCmd())
	return cmd
}

// RepoStatus describes the resolved forge state for a single repository.
type RepoStatus struct {
	Spec       string `json:"spec"`
	ForgeID    string `json:"forge_id"`
	ForgeKind  string `json:"forge_kind"`
	Reachable  bool   `json:"reachable"`
	AuthStatus string `json:"auth_status"`
}

// DoctorResult is the full output of `workspace doctor`.
type DoctorResult struct {
	Repos            []RepoStatus          `json:"repos"`
	ValidationErrors []ValidationErrorJSON `json:"validation_errors"`
	RegisteredForges []string              `json:"registered_forges"`
}

// ValidationErrorJSON is the JSON-serialisable form of forge.ValidationError.
type ValidationErrorJSON struct {
	Path    string `json:"path"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal"`
}

func doctorCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate multi-forge workspace configuration and report per-repo forge resolution",
		Long: `doctor checks the workspace configuration for multi-forge misconfigurations:

  - Dangling forge refs (repo mapped to an unregistered forge ID)
  - Orphan forges (registered but no repos mapped)
  - Per-repo forge resolution (which forge client each repo resolves to)

Exit code 1 when fatal validation errors are found.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, jsonOutput)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	return cmd
}

func runDoctor(cmd *cobra.Command, jsonOutput bool) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("workspace doctor: getwd: %w", err)
	}
	cfg, cfgErr := config.Load(wd)
	if cfgErr != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: config load failed (%v) — using empty config\n", cfgErr)
		cfg = &config.Config{}
	}

	router, err := forgecmd.BuildRouter("", 0, "")
	if err != nil {
		return fmt.Errorf("workspace doctor: build router: %w", err)
	}

	// Collect per-repo statuses from autonomous.repositories config.
	var repos []RepoStatus
	if cfg.Autonomous != nil {
		for spec := range cfg.Autonomous.Repositories {
			forgeID, ferr := router.ForgeIDFor(spec)
			forgeKind := string(router.KindFor(forgeID))
			authStatus := "ok"
			if ferr != nil {
				authStatus = "unknown"
			}
			repos = append(repos, RepoStatus{
				Spec:       spec,
				ForgeID:    forgeID,
				ForgeKind:  forgeKind,
				Reachable:  ferr == nil,
				AuthStatus: authStatus,
			})
		}
	}

	validationErrs := router.Validate()
	var jsonErrs []ValidationErrorJSON
	hasFatal := false
	for _, ve := range validationErrs {
		jsonErrs = append(jsonErrs, ValidationErrorJSON{
			Path:    ve.Path,
			Message: ve.Message,
			Fatal:   ve.Fatal,
		})
		if ve.Fatal {
			hasFatal = true
		}
	}
	if jsonErrs == nil {
		jsonErrs = []ValidationErrorJSON{}
	}
	if repos == nil {
		repos = []RepoStatus{}
	}

	result := DoctorResult{
		Repos:            repos,
		ValidationErrors: jsonErrs,
		RegisteredForges: router.IDs(),
	}

	if jsonOutput {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return fmt.Errorf("workspace doctor: encode json: %w", err)
		}
	} else {
		printDoctorHuman(cmd, result)
	}

	if hasFatal {
		return fmt.Errorf("workspace doctor: %d fatal validation error(s) found", countFatal(validationErrs))
	}
	return nil
}

func printDoctorHuman(cmd *cobra.Command, result DoctorResult) {
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "Registered forges: %v\n\n", result.RegisteredForges)

	if len(result.Repos) > 0 {
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "REPO\tFORGE ID\tKIND\tREACHABLE\tAUTH")
		for _, r := range result.Repos {
			reachable := "yes"
			if !r.Reachable {
				reachable = "no"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", r.Spec, r.ForgeID, r.ForgeKind, reachable, r.AuthStatus)
		}
		w.Flush()
		fmt.Fprintln(out)
	}

	if len(result.ValidationErrors) == 0 {
		fmt.Fprintln(out, "No validation errors.")
		return
	}
	fmt.Fprintln(out, "Validation errors:")
	for _, ve := range result.ValidationErrors {
		severity := "WARNING"
		if ve.Fatal {
			severity = "ERROR"
		}
		fmt.Fprintf(out, "  [%s] %s: %s\n", severity, ve.Path, ve.Message)
	}
}

func countFatal(errs []forge.ValidationError) int {
	n := 0
	for _, e := range errs {
		if e.Fatal {
			n++
		}
	}
	return n
}

// repoRefJSON is the JSON shape returned by `workspace repos-from-project`.
type repoRefJSON struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
}

// reposFromProjectCmd returns `workspace repos-from-project` — queries GitHub
// for all repositories linked to a ProjectV2 and outputs them as JSON.
// Called from WorkspaceManager.deriveReposFromProject() via execAsync.
func reposFromProjectCmd() *cobra.Command {
	var (
		owner     string
		ownerType string
		project   int
	)

	cmd := &cobra.Command{
		Use:   "repos-from-project",
		Short: "List repositories linked to a GitHub ProjectV2",
		Long: `repos-from-project queries GitHub's ProjectV2.repositories connection and
outputs a JSON array of {name, owner} objects. Useful for deriving the
workspace repository list from a shared project (N:1 topology).

Example:
  nightgauge workspace repos-from-project --owner nightgauge --project 6

Reads --owner and --project from flags; falls back to config.yaml when absent.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runReposFromProject(cmd, owner, ownerType, project)
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub org or user that owns the project")
	cmd.Flags().StringVar(&ownerType, "owner-type", "org", "Owner type: org or user")
	cmd.Flags().IntVar(&project, "project", 0, "GitHub Project number (required)")
	_ = cmd.MarkFlagRequired("project")

	return cmd
}

func runReposFromProject(cmd *cobra.Command, owner, ownerType string, project int) error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("repos-from-project: getwd: %w", err)
	}

	// Fall back to config for owner and owner-type when not provided via flag.
	cfg, cfgErr := config.Load(wd)
	if owner == "" {
		if cfgErr == nil && cfg != nil {
			owner = cfg.Owner
		}
		if owner == "" {
			return fmt.Errorf("repos-from-project: --owner is required (or set 'owner' in .nightgauge/config.yaml)")
		}
	}
	// Inherit owner-type from config when not explicitly overridden.
	if ownerType == "org" && cfgErr == nil && cfg != nil && cfg.OwnerType == "user" {
		ownerType = "user"
	}

	client, err := gh.NewClient()
	if err != nil {
		return fmt.Errorf("repos-from-project: create client: %w", err)
	}

	ot := gh.OwnerTypeOrg
	if ownerType == "user" {
		ot = gh.OwnerTypeUser
	}

	refs, err := gh.FetchProjectLinkedRepos(context.Background(), client, owner, ot, project)
	if err != nil {
		return fmt.Errorf("repos-from-project: %w", err)
	}

	out := make([]repoRefJSON, 0, len(refs))
	for _, r := range refs {
		out = append(out, repoRefJSON{Name: r.Name, Owner: r.Owner})
	}

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}
