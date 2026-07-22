package main

import (
	"fmt"
	"os"
	"strings"

	buildpkg "github.com/nightgauge/nightgauge/internal/build"
	"github.com/spf13/cobra"
)

// buildCmd is the top-level "build" command family.
//
// Subcommands:
//   - run: Detect build system and run the build.
func buildCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build detection and execution",
		Long:  `Detect the project build system (go.mod, package.json) and run the build. Centralizes build logic from skill shell cascades into the deterministic Go layer.`,
	}
	cmd.AddCommand(buildRunCmd())
	return cmd
}

// buildRunCmd implements `nightgauge build run [--json] [--workdir <dir>]`.
func buildRunCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Detect build system and run build",
		SilenceUsage: true,
		Example: `  nightgauge build run
  nightgauge build run --json
  nightgauge build run --workdir /path/to/project --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			result, err := buildpkg.RunBuild(cmd.Context(), workdir)
			if err != nil {
				return fmt.Errorf("build run: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if !result.Ran {
				fmt.Println("Build: skipped (no build system detected)")
				return nil
			}
			fmt.Printf("Build: %s (%s)\n", result.Status, strings.Join(result.Commands, ", "))
			if result.Output != "" {
				fmt.Print(result.Output)
			}
			if result.Status == "failed" {
				return fmt.Errorf("build failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}
