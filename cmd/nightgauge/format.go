package main

import (
	"fmt"
	"os"

	formatpkg "github.com/nightgauge/nightgauge/internal/format"
	"github.com/spf13/cobra"
)

// formatCmd is the top-level "format" command family.
//
// Subcommands:
//   - run: Detect project formatter and run it.
func formatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "format",
		Short: "Format detection and execution",
		Long:  `Detect the project formatter (npm format, prettier, dprint, go fmt, dart format) and run it. Centralizes format logic from skill shell cascades into the deterministic Go layer.`,
	}
	cmd.AddCommand(formatRunCmd())
	return cmd
}

// formatRunCmd implements `nightgauge format run [--json] [--workdir <dir>]`.
func formatRunCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Detect project formatter and run it",
		SilenceUsage: true,
		Example: `  nightgauge format run
  nightgauge format run --json
  nightgauge format run --workdir /path/to/project`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			result, err := formatpkg.RunFormat(cmd.Context(), workdir)
			if err != nil {
				return fmt.Errorf("format run: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if !result.Ran {
				fmt.Println("Format: skipped (no formatter detected)")
				return nil
			}
			fmt.Printf("Format: ran (%s)\n", result.Formatter)
			if result.Output != "" {
				fmt.Print(result.Output)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}
