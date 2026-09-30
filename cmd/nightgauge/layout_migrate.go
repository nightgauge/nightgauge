package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// The automatic per-clone layout migration at CLI start (ADR-024 § 15,
// #2040). The first command run on a clone without the layout-version marker
// moves its data to the new layout, with the code `nightgauge doctor --fix`
// runs. It never blocks or fails the command: a conflict, a run in flight or a
// live daemon leaves the data in place, and `nightgauge doctor` reports it.

// layoutAutoMigrateSkip lists the top-level commands that never trigger the
// migration: doctor reports and fixes it itself, `layout` prints paths and
// must not change them, hooks run inside other tools and must stay quiet and
// fast, and help, version and completion are side-effect free.
var layoutAutoMigrateSkip = map[string]bool{
	"doctor": true, "layout": true, "version": true, "help": true, "completion": true,
	cobra.ShellCompRequestCmd: true, cobra.ShellCompNoDescRequestCmd: true,
	"hook": true, "pre-push": true,
}

// layoutAutoMigrateApplies reports whether cmd may run the migration first.
func layoutAutoMigrateApplies(cmd *cobra.Command) bool {
	path := strings.Fields(cmd.CommandPath())
	if len(path) < 2 || layoutAutoMigrateSkip[path[1]] {
		return false
	}
	// A dry run promises to change nothing.
	if f := cmd.Flags().Lookup("dry-run"); f != nil && cmd.Flags().Changed("dry-run") {
		return false
	}
	return true
}

// autoMigrateLayoutAtCLIStart runs the migration for the command's workspace
// (the same root log retention uses). It prints one line to stderr only when
// it moved data or had to leave some behind, which the retry window bounds to
// once an hour.
func autoMigrateLayoutAtCLIStart(cmd *cobra.Command) {
	root := explicitWorkspaceRoot(cmd)
	if root == "" {
		root, _ = os.Getwd()
	}
	rep, ran := doctor.AutoMigrateLayout(context.Background(), absPathOrEmpty(root))
	if !ran {
		return
	}
	switch {
	case rep.Version > 0 && rep.Changed():
		fmt.Fprintf(cmd.ErrOrStderr(), "nightgauge: moved per-clone data to the new layout (%s)\n", rep.Summary())
	case rep.Version == 0 && (rep.Changed() || len(rep.Conflicts) > 0 || rep.Blocked != "" ||
		rep.FilesHeld != "" || len(rep.Skipped) > 0 || len(rep.Refused) > 0 || len(rep.Errors) > 0):
		fmt.Fprintf(cmd.ErrOrStderr(), "nightgauge: per-clone data is still at an old location (%s); "+
			"run `nightgauge doctor --fix`\n", rep.Summary())
	}
}
