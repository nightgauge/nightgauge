package main

// `nightgauge capabilities validate|matrix` — the CLI surface over
// capabilities.yaml, the one hand-authored layer of the workspace knowledge
// graph (ADR-005 Decision 2).
//
// `validate` is the gate: it refuses an unknown status/disposition/surface and,
// against the tree, a doc that does not exist or an `owns` glob that matches
// nothing. The empty-glob check is the anti-rot mechanism — a capability whose
// implementation was deleted or relocated fails here rather than persisting as
// a claim about a product that no longer exists.
//
// `matrix` renders capability × surface. docs/CAPABILITIES_MAP.md is that
// render, committed; `--write` regenerates it.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/capabilities"
	"github.com/spf13/cobra"
)

// registryFilename is the registry's fixed name at the repository root. It is
// authored, reviewed and public-facing, which is why it lives at the root
// rather than under .nightgauge/ with the generated and gitignored state.
const registryFilename = "capabilities.yaml"

func capabilitiesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Capability registry — validate and render the surface matrix",
	}
	cmd.AddCommand(capabilitiesValidateCmd(), capabilitiesMatrixCmd())
	return cmd
}

func capabilitiesValidateCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate capabilities.yaml against its schema and the tree",
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := capabilities.Load(filepath.Join(root, registryFilename))
			if err != nil {
				return err
			}
			violations, err := reg.ValidateAgainstTree(root)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			for _, v := range violations {
				fmt.Fprintf(cmd.ErrOrStderr(), "violation: %s\n", v)
			}
			if len(violations) > 0 {
				return fmt.Errorf("%d capability violation(s)", len(violations))
			}
			fmt.Fprintf(out, "%d capabilities valid — every doc exists, every owns glob matches\n",
				len(reg.Capabilities))
			if holes := reg.SurfacesWithoutCapability(); len(holes) > 0 {
				fmt.Fprintf(out, "surfaces no capability claims: %v\n", holes)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "repository root")
	return cmd
}

func capabilitiesMatrixCmd() *cobra.Command {
	var root, format string
	var write bool
	cmd := &cobra.Command{
		Use:   "matrix",
		Short: "Render the capability × surface matrix",
		RunE: func(cmd *cobra.Command, _ []string) error {
			reg, err := capabilities.Load(filepath.Join(root, registryFilename))
			if err != nil {
				return err
			}
			if format == "json" {
				b, err := reg.RenderJSON()
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(b)
				return err
			}
			md := reg.RenderMarkdown()
			if write {
				p := filepath.Join(root, "docs", "CAPABILITIES_MAP.md")
				if err := os.WriteFile(p, []byte(md), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", p)
				return nil
			}
			fmt.Fprint(cmd.OutOrStdout(), md)
			return nil
		},
	}
	cmd.Flags().StringVar(&root, "root", ".", "repository root")
	cmd.Flags().StringVar(&format, "format", "markdown", "markdown|json")
	cmd.Flags().BoolVar(&write, "write", false, "write docs/CAPABILITIES_MAP.md instead of stdout")
	return cmd
}
