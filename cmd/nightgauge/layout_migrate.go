package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// The automatic layout migration at CLI start (ADR-024 § 15, #2040). The
// first command run on a clone without a current layout-version marker moves
// its per-clone data, and every checkout's per-checkout data, to the new
// layout with the code `nightgauge doctor --fix` runs. It never blocks or
// fails the command (ADR-024 § 15 amendment of 2026-09-29): a conflict, a run
// in flight, a busy worktree, a held lock or a live daemon leaves the data in
// place, prints one line naming `nightgauge doctor --fix`, and `nightgauge
// doctor` reports it. Only `doctor --fix` exits 3 (conflict) or 4 (blocked).

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

// autoMigrateLayoutAtCLIStart runs the machine-state migration (#2041: this
// user's state from ~/.nightgauge to the machine-state directory, wherever
// the command runs) and the migration for the command's workspace (the same
// root log retention uses). Each prints one line to stderr only when it moved
// data or had to leave some behind, which the retry window bounds to once an
// hour.
func autoMigrateLayoutAtCLIStart(cmd *cobra.Command) {
	if rep, ran := doctor.AutoMigrateMachineState(context.Background()); ran {
		reportAutoMigration(cmd, "machine state", rep)
	}
	root := explicitWorkspaceRoot(cmd)
	if root == "" {
		root, _ = os.Getwd()
	}
	rep, ran := doctor.AutoMigrateLayout(context.Background(), absPathOrEmpty(root))
	if !ran {
		return
	}
	reportAutoMigration(cmd, "data", rep)
}

// reportAutoMigration prints the one stderr line an automatic migration run
// earns: what moved, or what is still at an old location. what names the
// migrated data ("data", "machine state").
func reportAutoMigration(cmd *cobra.Command, what string, rep doctor.LayoutReport) {
	switch {
	case rep.Version > 0 && rep.Changed():
		fmt.Fprintf(cmd.ErrOrStderr(), "nightgauge: moved Nightgauge %s to the new layout (%s)\n", what, rep.Summary())
	case rep.Version == 0 && (rep.Changed() || len(rep.Conflicts) > 0 || rep.Blocked != "" ||
		rep.FilesHeld != "" || len(rep.Skipped) > 0 || len(rep.Refused) > 0 || len(rep.Errors) > 0):
		fmt.Fprintf(cmd.ErrOrStderr(), "nightgauge: Nightgauge %s is still at an old location (%s); "+
			"run `nightgauge doctor --fix`\n", what, rep.Summary())
	}
}
