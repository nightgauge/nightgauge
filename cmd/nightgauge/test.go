package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	tests "github.com/nightgauge/nightgauge/internal/cmd/tests"
	"github.com/spf13/cobra"
)

// testCmd is the top-level "test" command. It exposes deterministic verbs
// for test inventory and untested-file risk scoring. Implements audit
// appendix row B39 — absorbs the inline Glob + grep + git log shell from
// skills/nightgauge-test-scaffold/SKILL.md Phases 1 and 3.
func testCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Test inventory and risk-based prioritization",
		Long: `Deterministic verbs for the test-scaffold pipeline. Replaces the inline
Glob + grep + git log shell embedded in skills/nightgauge-test-scaffold/
SKILL.md Phases 1 + 3 (audit row B39). Output schemas are stable v1 — additive
evolution only.`,
	}
	cmd.AddCommand(testInventoryCmd())
	cmd.AddCommand(testRiskScoreCmd())
	return cmd
}

// testInventoryCmd implements `nightgauge test inventory`.
//
// Exit codes:
//
//	0 — inventory completed (zero source/test files is not an error)
//	2 — hard error (e.g. unresolvable workdir, walk failure)
func testInventoryCmd() *cobra.Command {
	var (
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "inventory",
		Short: "Walk the workdir and emit a source/test file inventory",
		Long: `Walk --workdir, classify every file matching the source-extension allowlist
(.ts .tsx .js .jsx .py .go .rs .java .kt) as source or test, build the
test→source mapping (basename strip per SKILL.md Phase 1.3), and list
source files with no matching test (Phase 1.4). Pure path classification —
no file-content reads.

Replaces test-scaffold SKILL.md Phase 1 Steps 1.1–1.4 (audit row B39).

Excluded directories (pruned at walk time): .git, node_modules, vendor,
dist, build, coverage.

Schema version 1 — field names (v, workdir, counts, source_files,
test_files, test_to_source_mapping, untested_files, warnings) are stable.

Exit codes:
  0  inventory completed
  2  hard error (e.g. unresolvable workdir, internal failure)`,
		Example: `  nightgauge test inventory --workdir . --json | jq '.counts'
  nightgauge test inventory --json | jq -r '.untested_files[]' > /tmp/u.txt`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := tests.RunInventory(cmd.Context(), tests.InventoryOptions{Workdir: workdir})
			if err != nil {
				fmt.Fprintln(os.Stderr, "test inventory:", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printInventoryHuman(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Directory to scan (default: current working directory)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

// testRiskScoreCmd implements `nightgauge test risk-score`.
//
// Exit codes:
//
//	0 — scoring completed
//	2 — hard error (bad flag, file-list read error)
func testRiskScoreCmd() *cobra.Command {
	var (
		filesPath  string
		readStdin  bool
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "risk-score",
		Short: "Score files by composite risk (criticality + complexity + change-freq + dep-depth)",
		Long: `Score each input file by combining four sub-scores from SKILL.md Phase 3:
business criticality (regex over content), code complexity (branching keyword
count), change frequency (git log --since="6 months ago"), and dependency
depth (basename-substring grep across the source allowlist). Composite is
min(100, sum) with priority bucket: critical (80-100), high (60-79),
medium (40-59), low (0-39).

Replaces test-scaffold SKILL.md Phase 3 Steps 3.1–3.5 (audit row B39).

Input: --files PATH reads newline-delimited paths (blank lines and lines
starting with '#' are ignored). --stdin reads the same format from stdin.
Paths may be absolute or workdir-relative.

Output is sorted by score descending, then by file path ascending, for
stable ordering.

Schema version 1 — field names (v, workdir, entries[].file, .business_criticality,
.complexity, .change_frequency, .dependency_depth, .score, .priority, warnings)
are stable.

Exit codes:
  0  scoring completed
  2  hard error (bad flag, input read error)`,
		Example: `  nightgauge test inventory --json | jq -r '.untested_files[]' > /tmp/u.txt && \
    nightgauge test risk-score --files /tmp/u.txt --workdir . --json | \
    jq '.entries[0:5]'
  cat /tmp/u.txt | nightgauge test risk-score --stdin --json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if filesPath == "" && !readStdin {
				return fmt.Errorf("--files PATH or --stdin is required")
			}
			if filesPath != "" && readStdin {
				return fmt.Errorf("--files and --stdin are mutually exclusive")
			}

			var src io.Reader
			if readStdin {
				src = os.Stdin
			} else {
				f, err := os.Open(filesPath)
				if err != nil {
					fmt.Fprintln(os.Stderr, "test risk-score:", err)
					os.Exit(2)
				}
				defer f.Close()
				src = f
			}

			files, err := readFilesList(src)
			if err != nil {
				fmt.Fprintln(os.Stderr, "test risk-score:", err)
				os.Exit(2)
			}

			result, err := tests.RunRiskScore(cmd.Context(), tests.RiskOptions{
				Workdir: workdir,
				Files:   files,
			})
			if err != nil {
				fmt.Fprintln(os.Stderr, "test risk-score:", err)
				os.Exit(2)
			}

			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
				return nil
			}
			printRiskScoreHuman(result)
			return nil
		},
	}
	cmd.Flags().StringVar(&filesPath, "files", "", "Path to a newline-delimited file list")
	cmd.Flags().BoolVar(&readStdin, "stdin", false, "Read newline-delimited file list from stdin")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root for git log + importer scans (default: cwd)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

// readFilesList parses a newline-delimited file list, ignoring blank lines
// and lines starting with `#`. Whitespace around each path is trimmed.
func readFilesList(r io.Reader) ([]string, error) {
	var out []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read files list: %w", err)
	}
	return out, nil
}

func printInventoryHuman(r *tests.InventoryResult) {
	fmt.Printf("nightgauge test inventory — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n\n", r.Workdir)
	fmt.Printf("  source files:   %d\n", r.Counts.SourceFiles)
	fmt.Printf("  test files:     %d\n", r.Counts.TestFiles)
	fmt.Printf("  untested files: %d\n", r.Counts.UntestedFiles)
	if len(r.UntestedFiles) > 0 {
		fmt.Println("\nFirst untested files:")
		limit := 10
		if len(r.UntestedFiles) < limit {
			limit = len(r.UntestedFiles)
		}
		for _, f := range r.UntestedFiles[:limit] {
			fmt.Printf("  • %s\n", f)
		}
		if len(r.UntestedFiles) > limit {
			fmt.Printf("  … (%d more)\n", len(r.UntestedFiles)-limit)
		}
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}

func printRiskScoreHuman(r *tests.RiskScoreResult) {
	fmt.Printf("nightgauge test risk-score — schema v%d\n", r.V)
	fmt.Printf("workdir: %s\n", r.Workdir)
	fmt.Printf("entries: %d\n\n", len(r.Entries))

	limit := 20
	if len(r.Entries) < limit {
		limit = len(r.Entries)
	}
	for _, e := range r.Entries[:limit] {
		fmt.Printf("  [%-8s] score=%3d  bc=%2d cx=%2d cf=%2d dd=%2d  %s\n",
			e.Priority, e.Score, e.BusinessCriticality, e.Complexity, e.ChangeFrequency, e.DependencyDepth, e.File)
	}
	if len(r.Entries) > limit {
		fmt.Printf("  … (%d more)\n", len(r.Entries)-limit)
	}
	for _, w := range r.Warnings {
		fmt.Printf("  ! %s\n", w)
	}
}
