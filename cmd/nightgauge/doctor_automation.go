package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// doctorAutomationCmd records or clears an operator's decision that a
// scheduled automation is intentionally paused (#2090). A paused automation
// that has stopped is still reported by doctor, as info, until it is resumed.
func doctorAutomationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "automation",
		Short: "Record that a scheduled automation is intentionally paused, or resume it",
	}
	var reason string
	pause := &cobra.Command{
		Use:          "pause <automation-id>",
		Short:        "Mark a stopped automation as intentionally paused (reported as info)",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			if err := doctor.PauseAutomation(root, args[0], reason, time.Now()); err != nil {
				return err
			}
			recorded, err := doctor.AutomationPausePath(root)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "paused %s (recorded in %s)\n", args[0], recorded)
			return nil
		},
	}
	pause.Flags().StringVar(&reason, "reason", "", "Why the automation is paused")
	resume := &cobra.Command{
		Use:          "resume <automation-id>",
		Short:        "Clear a recorded pause so a stopped automation reports as a warning again",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := os.Getwd()
			if err != nil {
				return err
			}
			found, err := doctor.ResumeAutomation(root, args[0])
			if err != nil {
				return err
			}
			if !found {
				fmt.Fprintf(cmd.OutOrStdout(), "%s was not paused\n", args[0])
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "resumed %s\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(pause, resume)
	return cmd
}
