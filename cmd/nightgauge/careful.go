package main

import (
	"fmt"
	"os"

	"github.com/nightgauge/nightgauge/internal/careful"
	"github.com/spf13/cobra"
)

// carefulCmd toggles the opt-in destructive-operation guardrail. The
// careful-gate PreToolUse(Bash) hook blocks production-destructive commands
// while careful mode is on.
func carefulCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "careful",
		Short: "Toggle the opt-in destructive-operation guardrail (on | off | status)",
	}
	cmd.AddCommand(carefulOnCmd(), carefulOffCmd(), carefulStatusCmd())
	return cmd
}

func resolveRoot(workdir string) (string, error) {
	if workdir != "" {
		return workdir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getcwd: %w", err)
	}
	return wd, nil
}

func carefulOnCmd() *cobra.Command {
	var (
		workdir string
		ttl     int
		note    string
	)
	cmd := &cobra.Command{
		Use:          "on",
		Short:        "Enable careful mode (blocks prod-destructive commands until off or TTL expiry)",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveRoot(workdir)
			if err != nil {
				return err
			}
			if err := careful.Enable(root, ttl, note); err != nil {
				return err
			}
			fmt.Printf("careful mode ON — prod-destructive Bash commands are blocked (TTL %d min). Run `nightgauge careful off` when done.\n", ttlOrDefault(ttl))
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: cwd)")
	cmd.Flags().IntVar(&ttl, "ttl", careful.DefaultTTLMinutes, "Auto-expiry in minutes")
	cmd.Flags().StringVar(&note, "note", "", "Optional note (e.g. why careful mode is on)")
	return cmd
}

func carefulOffCmd() *cobra.Command {
	var workdir string
	cmd := &cobra.Command{
		Use:          "off",
		Short:        "Disable careful mode",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveRoot(workdir)
			if err != nil {
				return err
			}
			if err := careful.Disable(root); err != nil {
				return err
			}
			fmt.Println("careful mode OFF.")
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: cwd)")
	return cmd
}

func carefulStatusCmd() *cobra.Command {
	var (
		workdir    string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Show whether careful mode is active",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveRoot(workdir)
			if err != nil {
				return err
			}
			lock, active := careful.Read(root)
			if jsonOutput {
				return printJSON(map[string]any{"active": active, "lock": lock})
			}
			if active {
				fmt.Printf("careful mode: ON (since %s)\n", lock.Since)
			} else {
				fmt.Println("careful mode: OFF")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Project root (default: cwd)")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output JSON")
	return cmd
}

func ttlOrDefault(ttl int) int {
	if ttl <= 0 {
		return careful.DefaultTTLMinutes
	}
	return ttl
}
