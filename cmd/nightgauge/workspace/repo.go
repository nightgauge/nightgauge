package workspacecmd

// `nightgauge workspace repo add|remove|list` (#703) — the deterministic writer
// for .vscode/nightgauge-workspace.yaml. Every surface that mutates the
// manifest (settings UI, the coverage-gap repair verb, an agent in a terminal)
// goes through this one validated path rather than reimplementing YAML editing.
//
// Splice mechanics and the comment-ownership rule live in manifest.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/nightgauge/nightgauge/internal/config"
	workspace "github.com/nightgauge/nightgauge/internal/knowledge/workspace"
	"github.com/spf13/cobra"
)

func repoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage repositories in the workspace manifest (.vscode/nightgauge-workspace.yaml)",
		Long: `repo add|remove|list is the only supported writer for the workspace
manifest. Writes are validated with the same rules every reader applies,
performed atomically, and preserve the file's comments, key order and
formatting — the manifest carries explanatory comments that are load-bearing
(the project_number zero-value footgun is documented only there).`,
	}
	cmd.AddCommand(repoListCmd())
	cmd.AddCommand(repoAddCmd())
	cmd.AddCommand(repoRemoveCmd())
	return cmd
}

// resolveManifest locates the workspace manifest from the current directory.
func resolveManifest() (*manifest, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := workspace.DetectWorkspaceRoot(wd)
	if err != nil {
		return nil, fmt.Errorf("no workspace manifest found from %s (this command is for multi-repo workspaces): %w", wd, err)
	}
	return loadManifest(manifestPath(root))
}

type repoListItem struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Role          string `json:"role,omitempty"`
	ProjectNumber int    `json:"project_number"`
}

func repoListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List repositories declared in the workspace manifest",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := resolveManifest()
			if err != nil {
				return err
			}
			items := make([]repoListItem, 0, len(m.entries))
			for _, e := range m.entries {
				items = append(items, repoListItem{
					Name:          e.Name,
					Path:          e.Path,
					Role:          e.Role,
					ProjectNumber: e.ProjectNumber,
				})
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(items)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPATH\tROLE\tPROJECT")
			for _, it := range items {
				role := it.Role
				if role == "" {
					role = "-"
				}
				project := fmt.Sprintf("%d", it.ProjectNumber)
				if it.ProjectNumber == 0 {
					project = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", it.Name, it.Path, role, project)
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit JSON")
	return cmd
}

func repoAddCmd() *cobra.Command {
	var name, path, role string
	var project int
	cmd := &cobra.Command{
		Use:   "add --name <name> --path <path> [--role <role>] [--project <n>]",
		Short: "Add a repository to the workspace manifest",
		Long: `add appends a validated repositories[] entry.

--project is resolved through the single authoritative resolver when omitted.
A zero project number is rejected from every path: it would resolve to project
0 and silently misroute issues.`,
		Example: `  nightgauge workspace repo add --name nightgauge-docs --path ../nightgauge-docs --project 9
  nightgauge workspace repo add --name nightgauge-docs --path ../nightgauge-docs --role secondary`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			if strings.TrimSpace(path) == "" {
				return fmt.Errorf("--path is required")
			}
			if role != "" && !containsString(validRoles, role) {
				return fmt.Errorf("--role must be one of: %s", strings.Join(validRoles, ", "))
			}

			m, err := resolveManifest()
			if err != nil {
				return err
			}
			if _, exists := m.find(name); exists {
				return fmt.Errorf("repository %q is already in the manifest — names must be unique", name)
			}

			// The path is resolved relative to the manifest's own directory,
			// matching how every reader resolves repositories[].path.
			base := filepath.Dir(filepath.Dir(m.path)) // strip /.vscode
			abs := path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(base, path)
			}
			st, statErr := os.Stat(abs)
			if statErr != nil || !st.IsDir() {
				return fmt.Errorf("--path %q does not resolve to a directory (looked at %s)", path, abs)
			}
			if _, gitErr := os.Stat(filepath.Join(abs, ".git")); gitErr != nil {
				return fmt.Errorf("--path %q is not a git repository: no .git found at %s", path, abs)
			}

			if cmd.Flags().Changed("project") {
				if project <= 0 {
					return fmt.Errorf("--project must be a positive integer: %d would resolve to project 0 and silently misroute issues", project)
				}
			} else {
				resolved, rerr := resolveProjectForRepo(name)
				if rerr != nil {
					return fmt.Errorf("--project was omitted and no project could be resolved for %q: %w\n"+
						"Pass --project <n> explicitly, or provision a board for this repository first", name, rerr)
				}
				project = resolved
			}

			if err := m.addEntry(manifestEntry{
				Name:          name,
				Path:          path,
				Role:          role,
				ProjectNumber: project,
			}); err != nil {
				return err
			}
			if err := m.writeAtomic(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Added %s (path %s, project %d) to %s\n", name, path, project, m.path)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Repository name (must be unique)")
	cmd.Flags().StringVar(&path, "path", "", "Path to the repository, relative to the workspace root")
	cmd.Flags().StringVar(&role, "role", "", fmt.Sprintf("Repository role (%s)", strings.Join(validRoles, "|")))
	cmd.Flags().IntVar(&project, "project", 0, "Project board number (resolved automatically when omitted)")
	return cmd
}

// resolveProjectForRepo funnels through the single authoritative repo→project
// resolver rather than matching project names, per the single-resolver contract
// in docs/MULTI_REPO_WORKSPACE.md. ResolveRepoProjectNumber already refuses to
// hand back a non-positive number.
func resolveProjectForRepo(repo string) (int, error) {
	wd, err := os.Getwd()
	if err != nil {
		return 0, err
	}
	cfg, cfgErr := config.Load(wd)
	if cfgErr != nil || cfg == nil {
		return 0, fmt.Errorf("no config loaded for repo→project resolution: %w", cfgErr)
	}
	owner := cfg.Owner
	repoPart := repo
	if idx := strings.Index(repo, "/"); idx >= 0 {
		owner, repoPart = repo[:idx], repo[idx+1:]
	}
	n, err := config.ResolveRepoProjectNumber(cfg, config.RepoProjectQuery{
		Owner:    owner,
		Repo:     repoPart,
		StartDir: wd,
	})
	if err != nil {
		return 0, err
	}
	if n <= 0 {
		return 0, fmt.Errorf("resolver returned no project for %q", repo)
	}
	return n, nil
}

func repoRemoveCmd() *cobra.Command {
	var name string
	var force bool
	cmd := &cobra.Command{
		Use:   "remove --name <name> [--force]",
		Short: "Remove a repository from the workspace manifest",
		Long: `remove deletes a repositories[] entry.

It refuses when the name is still referenced by routing.default_repository or
by a routing.patterns[].preferred_repo, because removing it would leave routing
pointing at a repository the manifest no longer declares. --force overrides.

A comment block directly above the removed entry is retained rather than
deleted: yaml attributes a comment to the node below it, so an entry can own a
comment that documents the whole list.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			m, err := resolveManifest()
			if err != nil {
				return err
			}
			if _, ok := m.find(name); !ok {
				return fmt.Errorf("repository %q is not in the manifest", name)
			}

			var refs []string
			if m.routingDefault == name {
				refs = append(refs, "routing.default_repository")
			}
			for _, patternID := range m.routingPreferred[name] {
				refs = append(refs, fmt.Sprintf("routing.patterns[%s].preferred_repo", patternID))
			}
			if len(refs) > 0 && !force {
				return fmt.Errorf("repository %q is still referenced by %s\n"+
					"Removing it would leave routing pointing at a repository the manifest does not declare.\n"+
					"Update the routing section first, or pass --force to remove the entry anyway",
					name, strings.Join(refs, ", "))
			}

			keptComment, err := m.removeEntry(name)
			if err != nil {
				return err
			}
			if err := m.writeAtomic(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s from %s\n", name, m.path)
			if keptComment {
				fmt.Fprintf(cmd.OutOrStdout(),
					"NOTE: a comment block above %s was retained — yaml attaches comments to the entry\n"+
						"      below them, so it may document the list rather than that entry. Review it.\n", name)
			}
			if len(refs) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(),
					"WARNING: --force removed an entry still referenced by %s. Update routing.\n",
					strings.Join(refs, ", "))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Repository name to remove")
	cmd.Flags().BoolVar(&force, "force", false, "Remove even when routing still references the repository")
	return cmd
}
