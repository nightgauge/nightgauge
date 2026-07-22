// Package forgecmd implements the `nightgauge forge` Cobra
// command tree — a parallel CLI surface that routes every operation
// through internal/forge.ForgeClient (ADR-006). The existing top-level
// `issue`, `pr`, `project`, `label`, and `auth` commands remain
// unchanged; `forge` exposes the forge-agnostic verbs that downstream
// skill files migrate to in W4-2.
//
// N:1 topology note (#3756): the forge router resolves one project per
// invocation, using the project number from the invoking repo's
// .nightgauge/config.yaml. In a workspace where multiple repos share
// a single GitHub Project (N:1), each per-repo pipeline invocation still
// routes correctly because each repo carries its own config.yaml with
// project.number set. The workspace manifest's shared_project_number field
// is consumed only by the Repositories view (view-layer listing), not here.
package forgecmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/forge"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"

	// Side-effect imports register the GitHub adapter (and any others
	// the binary ships with) into forge.New's dispatch table.
	_ "github.com/nightgauge/nightgauge/internal/github"
)

// Cmd returns the top-level `forge` Cobra command. It is registered
// from cmd/nightgauge/main.go::rootCmd().
func Cmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "forge",
		Short:   "Unified forge operations (issues, PRs, projects, labels, auth) across GitHub and GitLab",
		Long:    longRoot,
		Aliases: []string{},
	}

	cmd.PersistentFlags().String("forge", "", "Forge id (overrides IB_FORGE and config); e.g. 'github'")
	cmd.PersistentFlags().String("repo", "", "Target repo as owner/name; used for inference when --forge is not given")
	cmd.PersistentFlags().String("owner", "", "Owner namespace (org or user)")
	cmd.PersistentFlags().Int("project", 0, "Project board number (1-based)")
	cmd.PersistentFlags().Bool("json", false, "Output as JSON (mutually exclusive with --template)")
	cmd.PersistentFlags().String("template", "", "Render output through a Go text/template")
	cmd.PersistentFlags().String("owner-type", "", "Owner type: org or user (default org)")

	cmd.AddCommand(issueCmd())
	cmd.AddCommand(prCmd())
	cmd.AddCommand(projectCmd())
	cmd.AddCommand(labelCmd())
	cmd.AddCommand(authCmd())
	cmd.AddCommand(repoCmd())
	cmd.AddCommand(graphqlCmd())
	cmd.AddCommand(webhookCmd())
	return cmd
}

// forgeFromContext is the package-level resolver swapped by tests. It
// reads the persistent flags off the root `forge` command and asks the
// router for the corresponding ForgeClient.
//
// Production path: build a router from config + env, route via
// --forge/--repo. Tests inject a fake by reassigning forgeFromContext.
var forgeFromContext = realForgeFromContext

// realForgeFromContext is the production resolver — kept as a separate
// function so tests can call it without recursing through the indirect
// forgeFromContext variable.
func realForgeFromContext(cmd *cobra.Command) (forge.ForgeClient, error) {
	forgeFlag, _ := flagString(cmd, "forge")
	repoSpec, _ := flagString(cmd, "repo")
	owner, _ := flagString(cmd, "owner")
	project, _ := flagInt(cmd, "project")
	ownerType, _ := flagString(cmd, "owner-type")

	router, err := BuildRouter(owner, project, ownerType)
	if err != nil {
		return nil, err
	}
	return router.For(forgeFlag, repoSpec)
}

// BuildRouter constructs a forge.Router from the workspace config. When
// no `forges:` block is present in config.yaml, the router falls back
// to a single GitHub adapter using the legacy top-level fields.
//
// Exported so that sibling cmd packages (e.g. workspacecmd) can reuse
// the same router construction logic without duplicating config loading.
func BuildRouter(ownerOverride string, projectOverride int, ownerTypeOverride string) (*forge.Router, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("forge: getwd: %w", err)
	}
	cfg, err := config.Load(wd)
	if err != nil {
		// A missing or malformed config is recoverable — the user can
		// still pass --owner/--token explicitly. Continue with an
		// empty config.
		cfg = &config.Config{}
	}

	r := forge.NewRouter()

	owner := firstNonEmpty(ownerOverride, cfg.Owner)
	ownerType := firstNonEmpty(ownerTypeOverride, cfg.OwnerType, "org")
	projectNumber := projectOverride
	if projectNumber == 0 {
		projectNumber = cfg.ProjectNumber
	}

	// Resolve token via the standard chain (config → env → gh CLI). Failures
	// are non-fatal so subcommands that don't need auth (e.g. `--help`)
	// still work; downstream service calls will surface auth errors.
	token, _ := gh.ResolveTokenChain(cfg, owner)

	r.Register("github", forge.Config{
		Kind:          forge.KindGitHub,
		Token:         token,
		Owner:         owner,
		ProjectNumber: projectNumber,
		OwnerType:     ownerType,
	})
	r.SetDefault("github")

	if len(cfg.Forges) > 0 {
		for id, fc := range cfg.Forges {
			if fc == nil {
				continue
			}
			t := token
			if fc.TokenEnv != "" {
				if v := os.Getenv(fc.TokenEnv); v != "" {
					t = v
				}
			}
			k := forge.Kind(fc.Kind)
			if k == "" {
				k = forge.KindGitHub
			}
			o := fc.Owner
			if o == "" {
				o = owner
			}
			p := fc.ProjectNumber
			if p == 0 {
				p = projectNumber
			}
			ot := fc.OwnerType
			if ot == "" {
				ot = ownerType
			}
			r.Register(id, forge.Config{
				Kind:          k,
				Token:         t,
				Owner:         o,
				ProjectNumber: p,
				OwnerType:     ot,
				Host:          fc.Host,
			})
		}
	}

	// Wire per-repo forge mappings from autonomous.repositories[].forge.
	if cfg.Autonomous != nil {
		for repoSpec, rc := range cfg.Autonomous.Repositories {
			if rc != nil && rc.Forge != "" {
				r.MapRepo(repoSpec, rc.Forge)
			}
		}
	}

	// Validate the router; log warnings and return error on fatal misconfigurations.
	if validationErrs := r.Validate(); len(validationErrs) > 0 {
		for _, ve := range validationErrs {
			if ve.Fatal {
				return nil, fmt.Errorf("forge config: %s: %s", ve.Path, ve.Message)
			}
			fmt.Fprintf(os.Stderr, "forge config warning: %s: %s\n", ve.Path, ve.Message)
		}
	}

	return r, nil
}

// flagString reads a persistent flag value from the command tree. It
// walks parents because Cobra binds persistent flags to whichever
// command declared them.
func flagString(cmd *cobra.Command, name string) (string, error) {
	if f := cmd.Flag(name); f != nil {
		return f.Value.String(), nil
	}
	return "", fmt.Errorf("flag %q not found", name)
}

func flagInt(cmd *cobra.Command, name string) (int, error) {
	f := cmd.Flag(name)
	if f == nil {
		return 0, fmt.Errorf("flag %q not found", name)
	}
	var n int
	if _, err := fmt.Sscanf(f.Value.String(), "%d", &n); err != nil {
		return 0, nil
	}
	return n, nil
}

func flagBool(cmd *cobra.Command, name string) bool {
	f := cmd.Flag(name)
	if f == nil {
		return false
	}
	return f.Value.String() == "true"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// emitError prints a stable `error: ` prefix to stderr and returns the
// unmodified error so cobra's RunE can surface a non-zero exit code.
// Callers wrap their errors with verb context before calling this.
func emitError(cmd *cobra.Command, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, forge.ErrUnsupported) {
		fmt.Fprintf(cmd.ErrOrStderr(), "error: %v (operation not supported by this forge)\n", err)
	} else {
		fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
	}
	return err
}
