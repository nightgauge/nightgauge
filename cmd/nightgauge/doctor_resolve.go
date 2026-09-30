package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// doctorResolveCmd carries out an operator's choice for a conflict the
// migration will not decide (#2308). Today that is the machine-id: two
// different ids at the old and the new location (NGD045).
func doctorResolveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Carry out your choice for a conflict doctor reports but will not decide",
	}
	cmd.AddCommand(doctorResolveMachineIDCmd(doctor.ResolveMachineIDConflict))
	return cmd
}

func doctorResolveMachineIDCmd(resolve func(string) (doctor.MachineIDResolution, error)) *cobra.Command {
	var keep string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "machine-id --keep state|legacy",
		Short: "Keep one of two differing machine ids (NGD045); the other is saved, never deleted",
		Long: `Resolve the machine-id conflict nightgauge doctor reports as NGD045: a
machine-id in the machine-state directory and a different one an older build
left in ~/.nightgauge.

  --keep state    keep the id in the machine-state directory, the one this
                  build uses
  --keep legacy   keep the id from ~/.nightgauge

The chosen id ends at <state>/machine-id byte for byte, mode 0600. The other id
is saved beside it as machine-id.replaced-<UTC time>, never deleted, and the
legacy file is removed so nightgauge doctor --fix can move the rest of the
machine state. There is no default: the platform knows this device by its id,
and a different id is a different device. Without a conflict nothing changes,
and no id is ever generated.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res, err := resolve(keep)
			if err != nil {
				return err
			}
			return renderMachineIDResolution(cmd.OutOrStdout(), res, jsonOut)
		},
	}
	cmd.Flags().StringVar(&keep, "keep", "", "Which id to keep: state (the one this build uses) or legacy (the one in ~/.nightgauge)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output the resolution as JSON")
	_ = cmd.MarkFlagRequired("keep")
	return cmd
}

func renderMachineIDResolution(w io.Writer, res doctor.MachineIDResolution, jsonOut bool) error {
	if jsonOut {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintf(w, "kept the %s machine-id %s at %s (mode 0600)\n", res.Kept, res.ID, res.Path)
	fmt.Fprintf(w, "saved the other id %s at %s\n", res.BackupID, res.Backup)
	fmt.Fprintf(w, "removed %s\n", res.Legacy)
	fmt.Fprintln(w, "next: run `nightgauge doctor --fix` to move the rest of the machine state")
	return nil
}
