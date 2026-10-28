package main

import (
	"fmt"
	"io"
	"os"

	"github.com/nightgauge/nightgauge/internal/cmd/modelwatch"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/spf13/cobra"
)

// modelWatchCmd is the top-level "model-watch" command: deterministic intake
// for vendor model releases, backing skills/nightgauge-model-watch (#83).
func modelWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model-watch",
		Short: "Turn a vendor model release into a registry patch, overlay proposals and defects",
		Long: `Deterministic verbs behind the nightgauge-model-watch skill. They propose
work and never write the model registry. Output schemas are stable v1 —
additive evolution only.`,
	}
	cmd.AddCommand(modelWatchClassifyCmd())
	return cmd
}

// modelWatchClassifyCmd implements `nightgauge model-watch classify`.
//
// Exit codes:
//
//	0 — classification completed (an empty result is success)
//	2 — bad input dir, manifest, source file or registry snapshot
func modelWatchClassifyCmd() *cobra.Command {
	var (
		input      string
		registry   string
		overlays   string
		jsonOutput bool
	)
	cmd := &cobra.Command{
		Use:   "classify",
		Short: "Classify fetched release pages into {patch, overlays, defects}",
		Long: `Reads --input <dir>: a manifest.json (provider, tiers, optional model_id and
display_name, sources[]{file,url,kind}) plus the fetched page text it names.
Compares the release against the baseline registry (--registry <file>, else the
embedded registry) and emits a proposed registry patch, one overlay proposal per
documented propensity axis, defects such as a thinking/effort 400, and effort
capabilities the band's prior holder lacked.

Vendor text is untrusted: the patch holds typed values only, and every quote is
truncated, stripped of backticks and screened for prompt injection in block
mode. A screened-out finding is listed under "dropped". There is no --apply.

Exit codes:
  0  classification completed
  2  bad input dir, manifest, source file or registry snapshot`,
		Example: `  nightgauge model-watch classify --input /tmp/model-watch/opus-5 --json
  nightgauge model-watch classify --input ./pages --registry snapshot.json`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if input == "" {
				return fmt.Errorf("--input is required (directory holding manifest.json)")
			}
			baseline := models.All()
			if registry != "" {
				b, err := modelwatch.LoadBaseline(registry)
				if err != nil {
					fmt.Fprintln(os.Stderr, "model-watch classify:", err)
					os.Exit(2)
				}
				baseline = b
			}
			res, err := modelwatch.Classify(input, baseline, modelwatch.Options{OverlayRoot: overlays})
			if err != nil {
				fmt.Fprintln(os.Stderr, "model-watch classify:", err)
				os.Exit(2)
			}
			if jsonOutput {
				return printJSON(res)
			}
			printModelWatchHuman(cmd.OutOrStdout(), &res)
			return nil
		},
	}
	cmd.Flags().StringVar(&input, "input", "", "Directory with manifest.json and fetched page text — required")
	cmd.Flags().StringVar(&registry, "registry", "", "Baseline registry snapshot ({\"models\": [...]}); default is the embedded registry")
	cmd.Flags().StringVar(&overlays, "overlays", "skills/_shared/_overlays", "Overlay directory used to choose create vs edit")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output result as JSON (parsed by skills)")
	return cmd
}

func printModelWatchHuman(w io.Writer, r *modelwatch.Result) {
	if r.Patch.Add == nil {
		fmt.Fprintf(w, "No new %s model in the input (model id %q).\n", r.Provider, r.ModelID)
		return
	}
	fmt.Fprintf(w, "Proposed registry add: %s (%s, tiers %v)\n", r.Patch.Add.ID, r.Provider, r.Patch.Add.Tiers)
	for _, d := range r.Patch.Demote {
		fmt.Fprintf(w, "  demote %s -> replacement %s\n", d.ID, d.Replacement)
	}
	for _, c := range r.Capabilities {
		fmt.Fprintf(w, "Capability %s: %s\n", c.Kind, c.Value)
	}
	for _, d := range r.Defects {
		fmt.Fprintf(w, "Defect %s: %s\n", d.Kind, d.Summary)
	}
	for _, o := range r.Overlays {
		fmt.Fprintf(w, "Overlay %s=%s: %s %s\n", o.Axis, o.Level, o.Action, o.Target)
	}
	for _, d := range r.Dropped {
		fmt.Fprintf(w, "Dropped (%s): %s\n", d.Source, d.Reason)
	}
}
