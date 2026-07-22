package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
)

// configCmd is the parent for `nightgauge config ...` operations.
func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration operations",
	}
	cmd.AddCommand(configShowCmd())
	cmd.AddCommand(configInitCmd())
	cmd.AddCommand(configValidateCmd())
	cmd.AddCommand(configMigrateCmd())
	return cmd
}

// configShowCmd renders the merged effective configuration in the canonical
// on-disk YAML schema. Replaces the brittle `grep | awk` and `yq` patterns
// scattered across consuming skills (audit row B11).
func configShowCmd() *cobra.Command {
	var (
		key         string
		outputJSON  bool
		raw         bool
		tierAudit   bool
		filterDrift bool
		strict      bool
	)

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration (or a single dotted key)",
		Long: `Render the merged effective Nightgauge configuration.

By default prints the full configuration as YAML. Use --key to print a single
dotted path (e.g. "project.number" or "autonomous"); --raw strips quoting from
scalar leaves so shell scripts can capture the value directly. Missing keys
exit non-zero with "key not found: <path>" on stderr.

Use --tier-audit to render a per-key table showing where each setting comes
from (machine, project, or local tier), its documented target tier, and whether
it is in the correct tier. Runtime-tier keys (e.g. pipeline.max_concurrent) are
excluded until #3313 Phase 3 wires runtime enforcement to the Go side.`,
		Example: `  nightgauge config show
  nightgauge config show --key project.number --raw
  nightgauge config show --key autonomous --json
  nightgauge config show --tier-audit
  nightgauge config show --tier-audit --filter-drift
  nightgauge config show --tier-audit --json
  nightgauge config show --tier-audit --strict`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			workdir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve working directory: %w", err)
			}

			if tierAudit {
				hasDrift, out, err := config.RenderTierAudit(workdir, filterDrift, outputJSON)
				if err != nil {
					return fmt.Errorf("tier audit: %w", err)
				}
				fmt.Fprint(cmd.OutOrStdout(), out)
				if strict && hasDrift {
					return fmt.Errorf("tier audit: DRIFT detected (use --tier-audit to inspect)")
				}
				return nil
			}

			cfg, err := config.Load(workdir)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			out, err := config.Render(cfg, key, outputJSON, raw)
			if err != nil {
				if errors.Is(err, config.ErrKeyNotFound) {
					// Stable error format for shell `||` fallbacks: "key not found: <path>"
					// goes to stderr; cobra prints RunE errors to ErrOrStderr by default.
					return err
				}
				return err
			}
			fmt.Fprint(cmd.OutOrStdout(), out)
			return nil
		},
	}

	cmd.Flags().StringVar(&key, "key", "", "Dotted path to a single value or sub-document (e.g. project.number)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON instead of YAML")
	cmd.Flags().BoolVar(&raw, "raw", false, "Strip quoting and trailing newline from scalar values (requires --key)")
	cmd.Flags().BoolVar(&tierAudit, "tier-audit", false, "Show per-key tier source and drift status")
	cmd.Flags().BoolVar(&filterDrift, "filter-drift", false, "When used with --tier-audit, show only DRIFT rows")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit non-zero when any DRIFT row is present (requires --tier-audit)")

	return cmd
}

// configInitCmd renders the canonical .nightgauge/config.yaml template
// — either with placeholder tokens (offline mode) or with project-board
// field/option IDs queried via GitHub (online mode). Replaces the inline
// heredoc templates scattered across `nightgauge-repo-init` and
// `smart-setup` skills (audit row B10).
func configInitCmd() *cobra.Command {
	var (
		owner         string
		ownerType     string
		repo          string
		projectNumber int
		outPath       string
		force         bool
		noFetch       bool
		outputJSON    bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Generate .nightgauge/config.yaml from the canonical template",
		Long: `Render the canonical Nightgauge configuration template.

Without --project the template is emitted with <PROJECT_NUMBER> /
<*_OPTION_ID> placeholders intact, suitable for offline scaffolding. With
--project N the verb queries GitHub once for the project ID and field/option
IDs and substitutes them into the template.

The verb refuses to overwrite an existing file unless --force is given. Use
--out - to write to stdout instead of a file.`,
		Example: `  nightgauge config init --owner nightgauge
  nightgauge config init --owner nightgauge --project 1 --repo nightgauge
  nightgauge config init --owner nightgauge --project 1 --force
  nightgauge config init --owner nightgauge --out -`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := config.InitOptions{
				Owner:         owner,
				OwnerType:     ownerType,
				Repo:          repo,
				ProjectNumber: projectNumber,
			}

			if projectNumber > 0 && !noFetch {
				snap, err := fetchProjectSnapshot(cmd.Context(), owner, ownerType, projectNumber)
				if err != nil {
					return fmt.Errorf("fetch project fields: %w", err)
				}
				applySnapshot(&opts, snap)
			}

			out, err := config.BuildTemplate(opts)
			if err != nil {
				return err
			}

			path, wrote, err := writeTemplate(cmd, out, outPath, force)
			if err != nil {
				return err
			}

			if outputJSON {
				return emitInitJSON(cmd, path, wrote)
			}
			if path != "" && wrote {
				fmt.Fprintf(cmd.ErrOrStderr(), "wrote %s\n", path)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&owner, "owner", "", "GitHub project owner (org or user login) — required")
	cmd.Flags().StringVar(&ownerType, "owner-type", "org", `Owner type: "org" or "user"`)
	cmd.Flags().StringVar(&repo, "repo", "", "Default repository name (emits <REPO_NAME> placeholder when empty)")
	cmd.Flags().IntVar(&projectNumber, "project", 0, "GitHub Project V2 number (omit for placeholder template)")
	cmd.Flags().StringVar(&outPath, "out", ".nightgauge/config.yaml", `Output path (use "-" for stdout)`)
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite an existing file at --out")
	cmd.Flags().BoolVar(&noFetch, "no-fetch", false, "Skip GitHub queries even when --project is set; emit placeholders for IDs")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit machine-readable JSON status to stdout after writing")

	_ = cmd.MarkFlagRequired("owner")

	return cmd
}

// configValidateCmd validates the .nightgauge/config.yaml file, reporting
// schema version, declared forges, and any validation errors with details.
func configValidateCmd() *cobra.Command {
	var (
		configPath string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate .nightgauge/config.yaml and report forge configuration",
		Long: `Validate the Nightgauge configuration file.

Reports:
  - Schema version (v1 or v2; v1 triggers migration warning)
  - All declared forges and their kinds
  - Validation errors with details

Exits non-zero when validation errors are found.`,
		Example: `  nightgauge config validate
  nightgauge config validate --config /path/to/config.yaml
  nightgauge config validate --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve relative config paths against CWD so the command works
			// regardless of the directory from which it is invoked.
			if !filepath.IsAbs(configPath) {
				workdir, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve working directory: %w", err)
				}
				configPath = filepath.Join(workdir, configPath)
			}
			cfg, err := config.LoadYAML(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			var repos map[string]*config.RepositoryConfig
			if cfg.Autonomous != nil {
				repos = cfg.Autonomous.Repositories
			}

			validationErr := config.ValidateForgeConfig(cfg.Forges, repos)

			if outputJSON {
				forgeKinds := make(map[string]string, len(cfg.Forges))
				for id, entry := range cfg.Forges {
					if entry != nil {
						forgeKinds[id] = entry.Kind
					}
				}
				var validationMsg *string
				if validationErr != nil {
					s := validationErr.Error()
					validationMsg = &s
				}
				payload := map[string]any{
					"schema_version":   cfg.SchemaVersion,
					"forges":           forgeKinds,
					"validation_error": validationMsg,
					"valid":            validationErr == nil,
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if encErr := enc.Encode(payload); encErr != nil {
					return encErr
				}
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "schema_version: %s\n", cfg.SchemaVersion)
				fmt.Fprintf(cmd.OutOrStdout(), "forges (%d):\n", len(cfg.Forges))
				for _, id := range forgeKeysSorted(cfg.Forges) {
					entry := cfg.Forges[id]
					fmt.Fprintf(cmd.OutOrStdout(), "  %s: kind=%s base_url=%s\n", id, entry.Kind, entry.BaseURL)
				}
				if validationErr != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "validation errors:\n  %s\n", validationErr)
				} else {
					fmt.Fprintf(cmd.OutOrStdout(), "validation: OK\n")
				}
			}

			if validationErr != nil {
				return fmt.Errorf("config validation failed: %w", validationErr)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config file")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit JSON report")
	return cmd
}

// configMigrateCmd migrates a v1 config.yaml to v2 schema using the
// yaml.v3 Node API to preserve comments and key ordering.
func configMigrateCmd() *cobra.Command {
	var (
		configPath string
		dryRun     bool
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Migrate config.yaml from v1 to v2 schema",
		Long: `Migrate the Nightgauge configuration file from v1 to v2 schema.

v1→v2 changes:
  - Adds schema_version: "2" at the top of the file
  - Inserts a default forges.github entry when no github forge exists

Comments, blank lines, and key ordering are preserved. The migration is
idempotent: running it on a v2 file is a no-op.`,
		Example: `  nightgauge config migrate
  nightgauge config migrate --dry-run
  nightgauge config migrate --config /path/to/config.yaml`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !filepath.IsAbs(configPath) {
				workdir, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve working directory: %w", err)
				}
				configPath = filepath.Join(workdir, configPath)
			}

			result, err := config.MigrateFile(configPath, dryRun)
			if err != nil {
				if errors.Is(err, config.ErrAlreadyMigrated) {
					if outputJSON {
						return encodeJSON(cmd, map[string]any{
							"path":    configPath,
							"changed": false,
							"message": "config is already at schema_version 2",
						})
					}
					fmt.Fprintf(cmd.OutOrStdout(), "already at schema_version 2: %s\n", configPath)
					return nil
				}
				return err
			}

			if outputJSON {
				payload := map[string]any{
					"path":        result.Path,
					"old_version": result.OldVersion,
					"new_version": result.NewVersion,
					"changed":     result.Changed,
					"dry_run":     dryRun,
				}
				if result.Diff != "" {
					payload["diff"] = result.Diff
				}
				return encodeJSON(cmd, payload)
			}

			if !result.Changed {
				fmt.Fprintf(cmd.OutOrStdout(), "already at schema_version 2: %s\n", configPath)
				return nil
			}

			if dryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "--- dry-run: %s ---\n", configPath)
				fmt.Fprint(cmd.OutOrStdout(), result.Diff)
				fmt.Fprintf(cmd.OutOrStdout(), "--- end dry-run (no changes written) ---\n")
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "migrated %s (v%s → v%s)\n", configPath, result.OldVersion, result.NewVersion)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&configPath, "config", ".nightgauge/config.yaml", "Path to config file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print diff without writing the file")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Emit machine-readable JSON result")
	return cmd
}

// encodeJSON writes v as indented JSON to cmd's stdout.
func encodeJSON(cmd *cobra.Command, v any) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// forgeKeysSorted returns sorted forge IDs for deterministic output.
func forgeKeysSorted(m map[string]*config.ForgeConfigEntry) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fetchProjectSnapshot queries GitHub once for the project ID and field
// metadata. The returned snapshot is later spliced into InitOptions.
func fetchProjectSnapshot(ctx context.Context, owner, ownerType string, projectNumber int) (*gh.FieldsSnapshot, error) {
	client, err := clientFromConfig()
	if err != nil {
		return nil, err
	}
	svc := gh.NewProjectService(client, owner, projectNumber, gh.ParseOwnerType(ownerType))
	if ctx == nil {
		ctx = context.Background()
	}
	return svc.SnapshotFields(ctx)
}

// applySnapshot copies project ID + canonical field IDs and option maps from
// snap into opts. Unknown fields on the project board are silently ignored —
// only Status, Priority, and Size are templated by `config init`.
func applySnapshot(opts *config.InitOptions, snap *gh.FieldsSnapshot) {
	if snap == nil {
		return
	}
	opts.ProjectID = snap.ProjectID
	if f, ok := snap.Fields["Status"]; ok {
		opts.StatusFieldID = f.ID
		opts.StatusOptions = lowercaseKeys(f.Options)
	}
	if f, ok := snap.Fields["Priority"]; ok {
		opts.PriorityFieldID = f.ID
		opts.PriorityOptions = lowercaseKeys(f.Options)
	}
	if f, ok := snap.Fields["Size"]; ok {
		opts.SizeFieldID = f.ID
		opts.SizeOptions = lowercaseKeys(f.Options)
	}
}

// lowercaseKeys returns a copy of in with each key lowercased. The Go parser
// expects lowercase kebab-case option names ("in-progress", "p0", "xs"), but
// GitHub returns the display names ("In progress", "P0", "XS").
func lowercaseKeys(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[normalizeOptionKey(k)] = v
	}
	return out
}

// normalizeOptionKey lowercases a GitHub option name and converts internal
// whitespace to hyphens (e.g. "In progress" → "in-progress").
func normalizeOptionKey(name string) string {
	out := make([]byte, 0, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c == ' ':
			out = append(out, '-')
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// writeTemplate persists out to outPath. When outPath == "-" the body is
// written to stdout and (path="", wrote=false) is returned. Otherwise the
// resolved path is returned along with wrote=true on success. An existing
// file with force=false returns an error.
func writeTemplate(cmd *cobra.Command, body, outPath string, force bool) (string, bool, error) {
	if outPath == "-" {
		fmt.Fprint(cmd.OutOrStdout(), body)
		return "", false, nil
	}

	if _, err := os.Stat(outPath); err == nil {
		if !force {
			return outPath, false, fmt.Errorf("config.yaml already exists at %q (use --force to overwrite)", outPath)
		}
	} else if !os.IsNotExist(err) {
		return outPath, false, fmt.Errorf("stat %s: %w", outPath, err)
	}

	if dir := filepath.Dir(outPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return outPath, false, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	if err := os.WriteFile(outPath, []byte(body), 0o644); err != nil {
		return outPath, false, fmt.Errorf("write %s: %w", outPath, err)
	}
	return outPath, true, nil
}

// emitInitJSON writes a stable {"path": ..., "wrote": ...} envelope to stdout
// so calling skills/scripts can branch on the outcome without parsing prose.
func emitInitJSON(cmd *cobra.Command, path string, wrote bool) error {
	payload := map[string]any{
		"path":  path,
		"wrote": wrote,
	}
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(payload)
}
