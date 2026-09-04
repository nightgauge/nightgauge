package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge"
	"github.com/nightgauge/nightgauge/internal/knowledge/okf"
	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
	"github.com/spf13/cobra"
)

// exportResult is the --json shape of `knowledge export`.
type exportResult struct {
	Source        string   `json:"source"`
	Target        string   `json:"target"`
	FilesCopied   int      `json:"files_copied"`
	LinksResolved int      `json:"links_resolved"`
	Warnings      []string `json:"warnings"`
	Valid         bool     `json:"valid"`
}

func knowledgeExportCmd() *cobra.Command {
	var (
		okfMode    bool
		workdir    string
		outputJSON bool
	)

	cmd := &cobra.Command{
		Use:          "export --okf <dir>",
		Short:        "Export the knowledge base as a portable Open Knowledge Format bundle",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		Long: `Copy the knowledge base to <dir> with every wiki-link resolved to a
bundle-absolute markdown link.

Wiki-links stay the authoring syntax — they are terse and they survive a file
move. They are also readable by no consumer we did not write, so an Open
Knowledge Format reader sees nodes and no edges. The export resolves them, so
the bundle can be handed to a customer, another agent framework, or a graph
viewer with no translation layer and no Nightgauge dependency.

What is exported:

  * every non-reserved .md entry, with its frontmatter block untouched and its
    body's wiki-links resolved
  * index.md and log.md, copied verbatim — they are navigation, and their
    links are already bundle-absolute

A link that cannot be resolved degrades to its display text with a warning on
stderr; it is never dropped silently, and the exported bundle contains no "[["
sequence. A link resolving outside the bundle is rejected the same way — the
authoring resolver joins link text onto a directory with no containment check,
which in a bundle meant to be shared is a way to smuggle a pointer out of the
tree.

The export directory must not be inside the knowledge root: exporting a bundle
into itself would make the walk consume its own output.

The result is checked for conformance before the command exits, so a bundle
that would fail an external consumer's own check fails here first.`,
		Example: `  nightgauge knowledge export --okf /tmp/okf-out
  nightgauge knowledge export --okf /tmp/okf-out --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !okfMode {
				return fmt.Errorf("--okf is required; it names the only export format")
			}
			if workdir == "" {
				var err error
				workdir, err = os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
			}

			bundleRoot := okf.KnowledgeRoot(workdir)
			if _, err := os.Stat(bundleRoot); err != nil {
				return fmt.Errorf("no knowledge base at %s", bundleRoot)
			}

			target, err := filepath.Abs(args[0])
			if err != nil {
				return err
			}
			if _, err := okf.ContainedPath(target, bundleRoot); err == nil {
				return fmt.Errorf("%s is inside the knowledge base; exporting a bundle into itself would make the walk consume its own output", args[0])
			}

			start := time.Now()
			res, err := exportBundle(bundleRoot, target, workdir)
			if err != nil {
				return err
			}

			conf, confErr := knowledge.ValidateConformance(target)
			if confErr != nil {
				return confErr
			}
			res.Valid = conf.Valid
			res.Source = bundleRoot
			res.Target = target

			emitKnowledgeTelemetry(workdir, telemetry.Event{
				Type:       telemetry.EventIndex,
				Scope:      "export",
				Path:       target,
				DurationMs: time.Since(start).Milliseconds(),
				Status:     "success",
			})

			if outputJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(res); err != nil {
					return err
				}
				if !conf.Valid {
					return fmt.Errorf("exported bundle has %d conformance violation(s)", len(conf.Violations))
				}
				return nil
			}

			for _, w := range res.Warnings {
				fmt.Fprintln(os.Stderr, "warning: "+w)
			}
			fmt.Printf("Exported %d entr%s to %s (%d link(s) resolved)\n",
				res.FilesCopied, pluralEntries(res.FilesCopied), target, res.LinksResolved)
			if !conf.Valid {
				for _, v := range conf.Violations {
					fmt.Fprintf(os.Stderr, "  %s: %s\n", v.Path, v.Reason)
				}
				return fmt.Errorf("exported bundle has %d conformance violation(s)", len(conf.Violations))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&okfMode, "okf", false, "Export in Open Knowledge Format (required)")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")

	return cmd
}

func pluralEntries(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// exportBundle walks the knowledge root and writes a resolved copy of every
// markdown file to target.
func exportBundle(bundleRoot, target, workdir string) (*exportResult, error) {
	res := &exportResult{Warnings: []string{}}

	err := filepath.WalkDir(bundleRoot, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		name := d.Name()
		if d.IsDir() {
			// Dot-directories hold derived state (the recall cache), not
			// bundle content.
			if p != bundleRoot && strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			return nil
		}

		rel, relErr := filepath.Rel(bundleRoot, p)
		if relErr != nil {
			return relErr
		}
		out := filepath.Join(target, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}

		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		content := string(data)

		// Navigation files are copied verbatim: their links are already
		// bundle-absolute, and they carry no authored wiki-links.
		if okf.IsReservedEntry(name) {
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return err
			}
			res.FilesCopied++
			return nil
		}

		// Resolve the BODY only. Frontmatter is a machine contract, and
		// rewriting inside it would change field values rather than prose.
		fmText, body := okf.SplitFrontmatter(content)
		resolved, warnings := knowledge.ResolveToMarkdown(body, p, workdir)
		res.LinksResolved += len(knowledge.ExtractWikiLinks(body)) - len(warnings)
		for _, w := range warnings {
			res.Warnings = append(res.Warnings, w.String())
		}

		final := resolved
		if fmText != "" {
			// Re-attach the ORIGINAL block text rather than re-rendering it,
			// so the export is byte-identical in the half it did not touch.
			final = "---\n" + fmText + "---\n\n" + resolved
		}
		if err := os.WriteFile(out, []byte(final), 0o644); err != nil {
			return err
		}
		res.FilesCopied++
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	return res, nil
}
