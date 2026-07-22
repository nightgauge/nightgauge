package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/preflight"
	"github.com/spf13/cobra"
)

// preflightDependencyGuardCmd wraps preflight.RunDependencyGuardCheck (#4095):
// the slopsquatting / hallucinated-dependency gate.
//
// It is intentionally NOT a deterministic StageGate (those forbid network — see
// docs/STAGE_GATES.md): registry-existence is a network check, so it runs as a
// CLI/skill preflight in pr-create / pr-merge, the same class as
// version-downgrade and scope-drift.
//
// Exit codes:
//
//	0 — no blocking findings (clean, or only network-inconclusive warnings)
//	1 — a missing (hallucinated) package or a typosquat was found (BLOCKING)
//	2 — IO error
func preflightDependencyGuardCmd() *cobra.Command {
	var (
		root       string
		baseline   string
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "dependency-guard",
		Short: "Block hallucinated / typosquatted newly-added dependencies",
		Long: `Diffs package.json / go.mod / requirements.txt against a baseline ref,
extracts the newly-added dependencies, and fails when one does not exist on its
registry (a hallucinated dependency) or is within one edit of a popular package
(a possible slopsquat). CVE scanners (npm audit / pip-audit / govulncheck) only
cover packages that really exist — this closes the existence/typosquat gap.

A registry lookup that cannot be completed (network/rate-limit) is reported as a
non-blocking warning, so a flaky registry never blocks a merge.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result, err := preflight.RunDependencyGuardCheck(cmd.Context(), preflight.DependencyGuardOptions{
				Root:     root,
				Baseline: baseline,
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "preflight dependency-guard: %v\n", err)
				os.Exit(2)
			}
			if jsonOutput {
				if err := printJSON(result); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
				}
			} else {
				printPreflightDependencyGuardHuman(result)
			}
			if result.HasBlocking() {
				os.Exit(1)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&root, "root", "", "Repository root (default: current working directory)")
	cmd.Flags().StringVar(&baseline, "baseline", "main", "Baseline git ref to diff added dependencies against")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printPreflightDependencyGuardHuman(r *preflight.DependencyGuardResult) {
	fmt.Printf("nightgauge preflight dependency-guard — schema v%d\n", r.V)
	fmt.Printf("  baseline: %s | newly-added deps: %d\n", r.Baseline, r.AddedCount)
	if len(r.Findings) == 0 && len(r.Inconclusive) == 0 {
		fmt.Println("  ✓ no missing or typosquatted dependencies")
	}
	for _, f := range r.Findings {
		fmt.Fprintf(os.Stderr, "  ✗ [%s] %s (%s): %s\n", f.Kind, f.Name, f.Ecosystem, f.Detail)
	}
	for _, f := range r.Inconclusive {
		fmt.Printf("  ⚠ %s (%s): %s\n", f.Name, f.Ecosystem, f.Detail)
	}
}
