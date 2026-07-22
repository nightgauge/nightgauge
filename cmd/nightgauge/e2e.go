package main

import (
	"fmt"
	"os"
	"strings"

	e2epkg "github.com/nightgauge/nightgauge/internal/e2e"
	"github.com/spf13/cobra"
)

// e2eCmd is the top-level "e2e" command family.
//
// Subcommands:
//   - detect: Scan the project for E2E test frameworks and return results.
//   - run:    Execute the E2E test suite using the detected (or specified) framework.
func e2eCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "e2e",
		Short: "E2E test detection and execution",
		Long:  `Detect E2E test frameworks (Playwright, Cypress, Vitest, Jest, Go) and execute test suites. Centralizes E2E detection logic from skill shell cascades into the deterministic Go layer. Closes audit appendix row B8.`,
	}
	cmd.AddCommand(e2eDetectCmd())
	cmd.AddCommand(e2eRunCmd())
	return cmd
}

// e2eDetectCmd implements `nightgauge e2e detect [--json] [--workdir <dir>]`.
func e2eDetectCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
	)

	cmd := &cobra.Command{
		Use:          "detect",
		Short:        "Detect E2E test frameworks in the project",
		SilenceUsage: true,
		Example: `  nightgauge e2e detect
  nightgauge e2e detect --json
  nightgauge e2e detect --workdir /path/to/project --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			result, err := e2epkg.DetectE2E(cmd.Context(), workdir)
			if err != nil {
				return fmt.Errorf("e2e detect: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if !result.Detected {
				fmt.Println("E2E: no frameworks detected")
				return nil
			}
			fmt.Printf("E2E frameworks: %s\n", strings.Join(result.Frameworks, ", "))
			if len(result.ConfigFiles) > 0 {
				fmt.Printf("Config files: %s\n", strings.Join(result.ConfigFiles, ", "))
			}
			if len(result.TestDirs) > 0 {
				fmt.Printf("Test dirs: %s\n", strings.Join(result.TestDirs, ", "))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	return cmd
}

// e2eRunCmd implements `nightgauge e2e run [--json] [--workdir <dir>] [--framework <name>]`.
func e2eRunCmd() *cobra.Command {
	var (
		outputJSON bool
		workdir    string
		framework  string
	)

	cmd := &cobra.Command{
		Use:          "run",
		Short:        "Execute E2E test suite",
		SilenceUsage: true,
		Example: `  nightgauge e2e run
  nightgauge e2e run --json
  nightgauge e2e run --framework playwright --json
  nightgauge e2e run --workdir /path/to/project --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if workdir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("get working directory: %w", err)
				}
				workdir = wd
			}

			result, err := e2epkg.RunE2E(cmd.Context(), workdir, framework)
			if err != nil {
				return fmt.Errorf("e2e run: %w", err)
			}

			if outputJSON {
				return printJSON(result)
			}

			if !result.Ran {
				fmt.Println("E2E: skipped (no framework detected)")
				return nil
			}
			fmt.Printf("E2E: %s (%s — %s)\n", result.Status, result.Framework, strings.Join(result.Commands, ", "))
			if result.Output != "" {
				fmt.Print(result.Output)
			}
			if result.Status == "failed" {
				return fmt.Errorf("e2e tests failed")
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	cmd.Flags().StringVar(&workdir, "workdir", "", "Working directory (default: cwd)")
	cmd.Flags().StringVar(&framework, "framework", "", "E2E framework to use (playwright|cypress|vitest|jest|go); auto-detected if omitted")
	return cmd
}
