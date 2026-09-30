package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/clonelock"
	"github.com/spf13/cobra"
)

// layoutReport is the JSON `nightgauge layout` prints: every location the
// binary resolves for one repository root (ADR-024 § 1, § 7, "One path
// source"). The extension, skills and docs obtain the per-clone and
// per-checkout paths here instead of hard-coding them. Machine roots are resolved without being
// created; an unresolvable one is reported as "".
type layoutReport struct {
	SchemaVersion int    `json:"schema_version"`
	Root          string `json:"root"`
	GitCommonDir  string `json:"git_common_dir"`
	GitDir        string `json:"git_dir"`
	Clone         string `json:"clone"`
	Pipeline      string `json:"pipeline"`
	Plans         string `json:"plans"`
	Retros        string `json:"retros"`
	Logs          string `json:"logs"`
	Checkout      string `json:"checkout"`
	State         string `json:"state"`
	Cache         string `json:"cache"`
	Runtime       string `json:"runtime"`
}

// layoutRoot is --workdir (default: the working directory), made absolute.
func layoutRoot(workdir string) (string, error) {
	if workdir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve the working directory: %w", err)
		}
		workdir = wd
	}
	return filepath.Abs(workdir)
}

// layoutSchemaVersion is the shape of layoutReport: 2 added git_dir and
// checkout (the per-checkout root, #2037).
const layoutSchemaVersion = 2

// layoutCheckoutClass names CHECKOUT in `layout path/write/append`: the
// per-checkout root, whose entries are the unkeyed singletons and runtime
// files of one checkout (layout.CheckoutEntries).
const layoutCheckoutClass = "checkout"

func resolveLayoutReport(root string) (layoutReport, error) {
	rep := layoutReport{SchemaVersion: layoutSchemaVersion, Root: root}
	clone, err := layout.CloneDir(root)
	if err != nil {
		return rep, err
	}
	rep.Clone = clone
	rep.GitCommonDir = filepath.Dir(clone)
	for class, dst := range map[string]*string{
		layout.ClassPipeline: &rep.Pipeline,
		layout.ClassPlans:    &rep.Plans,
		layout.ClassRetros:   &rep.Retros,
		layout.ClassLogs:     &rep.Logs,
	} {
		dir, err := layout.ClassDir(root, class)
		if err != nil {
			return rep, err
		}
		*dst = dir
	}
	checkout, err := layout.CheckoutDir(root)
	if err != nil {
		return rep, err
	}
	rep.Checkout = checkout
	rep.GitDir = filepath.Dir(checkout)
	rep.State, _ = layout.StateHomePath()
	rep.Cache, _ = layout.CacheHomePath()
	rep.Runtime, _ = layout.RuntimeDir()
	return rep, nil
}

// layoutCmd is `nightgauge layout`: print the resolved data layout as JSON,
// and read or write per-clone files through the resolver.
func layoutCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:   "layout",
		Short: "Print where Nightgauge keeps this repository's data, as JSON",
		Long: `Prints every location Nightgauge resolves for the repository at --workdir
(default: the working directory) as one JSON object (ADR-024 § 7):

  clone     <git-common-dir>/nightgauge, shared by every worktree of the clone
  pipeline  run state, stage contexts, history, traces   (clone/pipeline)
  plans     issue-keyed implementation plans             (clone/plans)
  retros    issue-keyed retrospectives                   (clone/retros)
  logs      per-clone logs                               (clone/logs)
  checkout  <git-dir>/nightgauge-worktree: this checkout's run control
            (current-run.json, run-state.json, batch-state.json,
            queue-state.json, serve.lock, go-backend.log) and runtime state
            (attention/, autonomous/, health/, focus.yaml, ...)
  state, cache, runtime   the per-user machine roots

Per-clone data lives in the git directory, so it is never committed and a
linked worktree sees the main clone's data. Each linked worktree has its own
checkout directory inside its own git dir. Outside a git repository the
command fails with "not a git repository".

Agents and scripts never write under the git directory by path: use
'nightgauge layout write' (or 'append') to store a file, and
'nightgauge layout path' to find one to read.`,
		Example: `  nightgauge layout
  nightgauge layout path pipeline issue-42.json
  cat plan.md | nightgauge layout write plans 42-add-widget.md`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := layoutRoot(workdir)
			if err != nil {
				return err
			}
			rep, err := resolveLayoutReport(root)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(rep)
		},
	}
	cmd.PersistentFlags().StringVar(&workdir, "workdir", "", "Repository root or any directory inside it (default: cwd)")
	cmd.AddCommand(layoutPathCmd(&workdir), layoutWriteCmd(&workdir, false), layoutWriteCmd(&workdir, true))
	return cmd
}

var layoutClassHelp = strings.Join(append(append([]string{}, layout.Classes...), layoutCheckoutClass), ", ")

// layoutDirOf resolves a `layout path/write/append` class: a per-clone class
// or "checkout".
func layoutDirOf(root, class string) (string, error) {
	if class == layoutCheckoutClass {
		return layout.CheckoutDir(root)
	}
	return layout.ClassDir(root, class)
}

func layoutPathCmd(workdir *string) *cobra.Command {
	return &cobra.Command{
		Use:   "path <class> [name]",
		Short: "Print the absolute path of a per-clone class directory or a file in it",
		Long: `Prints the absolute path of a per-clone class directory, or of this
checkout's directory (` + layoutClassHelp + `), or of the file name inside
it. Nothing is created beyond the clone or checkout directory.
Read files at the printed path; write them with 'nightgauge layout write'.`,
		Example: `  jq . "$(nightgauge layout path pipeline issue-42.json)"
  ls "$(nightgauge layout path plans)"
  jq . "$(nightgauge layout path checkout run-state.json)"`,
		Args:         cobra.RangeArgs(1, 2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := layoutRoot(*workdir)
			if err != nil {
				return err
			}
			var p string
			switch {
			case len(args) == 2 && args[0] == layoutCheckoutClass:
				p, err = layout.CheckoutPath(root, args[1])
			case len(args) == 2:
				p, err = layout.ClassFilePath(root, args[0], args[1])
			default:
				p, err = layoutDirOf(root, args[0])
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
			return err
		},
	}
}

func layoutWriteCmd(workdir *string, appendMode bool) *cobra.Command {
	var from string
	use, short, verb := "write", "Write stdin (or --from) to a file in a per-clone class directory", "Replaces"
	if appendMode {
		use, short, verb = "append", "Append stdin (or --from) to a file in a per-clone class directory", "Appends to"
	}
	cmd := &cobra.Command{
		Use:   use + " <class> <name>",
		Short: short,
		Long: verb + ` <name> inside the per-clone class directory or this checkout's
directory (` + layoutClassHelp + `)
with the content of stdin, or of the file --from names. name may contain
subdirectories ("history/2026-09-29.jsonl"), never ".." or an absolute path;
the write is confined to the class directory and refuses symlinks out of it.
` + map[bool]string{
			false: "The file is written to a temporary file and renamed into place, so a reader never sees a partial file.",
			true:  "The file and its parents are created when absent.",
		}[appendMode] + `
Prints the absolute path written.`,
		Example: map[bool]string{
			false: `  jq -n '{issue_number: 42}' | nightgauge layout write pipeline issue-42.json
  nightgauge layout write plans 42-add-widget.md --from /tmp/plan.md`,
			true: `  echo '{"event":"x"}' | nightgauge layout append pipeline history/events.jsonl`,
		}[appendMode],
		Args:         cobra.ExactArgs(2),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := layoutRoot(*workdir)
			if err != nil {
				return err
			}
			in := cmd.InOrStdin()
			if from != "" {
				f, err := os.Open(from)
				if err != nil {
					return err
				}
				defer f.Close()
				in = f
			}
			dir, err := layoutDirOf(root, args[0])
			if err != nil {
				return err
			}
			var p string
			if appendMode {
				// A per-clone file is shared by every checkout of the
				// clone: the append holds CLONE/.lock (ADR-024 § 7).
				if args[0] != layoutCheckoutClass {
					if release, lockErr := clonelock.Acquire(filepath.Dir(dir)); lockErr == nil {
						defer release()
					}
					p, err = layout.AppendClassFile(root, args[0], args[1], in)
				} else {
					p, err = layout.AppendCheckoutFile(root, args[1], in)
				}
			} else if args[0] == layoutCheckoutClass {
				p, err = layout.WriteCheckoutFile(root, args[1], in)
			} else {
				p, err = layout.WriteClassFile(root, args[0], args[1], in)
			}
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), p)
			return err
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "Read the content from this file instead of stdin")
	return cmd
}
