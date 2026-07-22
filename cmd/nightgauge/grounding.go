package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/intelligence/groundingGate"
	"github.com/spf13/cobra"
)

// groundCmd runs `nightgauge ground <issue-number>` — the pre-feature-dev
// grounding gate (#4099). It deterministically confirms the agent is grounded
// before feature-dev burns tokens: on the issue's feature branch (not the
// base), with the issue context present, and flags an under-specified premise.
//
// Exit codes:
//
//	0 — grounded (proceed; may print a pull-human low-confidence warning)
//	1 — NOT grounded (re-ground: wrong/protected branch or missing context)
//	2 — invalid arguments
func groundCmd() *cobra.Command {
	var (
		workdir    string
		branchFlag string
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "ground <issue-number>",
		Short: "Pre-feature-dev grounding gate: am I on the right issue/branch with context? (#4099)",
		Long: `Confirms the agent is grounded before feature-dev acts. Fails (exit 1) when
the current branch is a protected/base branch or does not match the issue's
feature branch, or when the issue context is missing — the hallucinated-task /
lost-grounding signals from #3863. A grounded-but-under-specified premise (no
acceptance criteria) still passes but recommends pulling human context.

Disabled with pipeline.grounding_gate.enabled: false.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(_ *cobra.Command, args []string) error {
			issueNum, err := parseIssueNumberArg(args[0])
			if err != nil {
				return err
			}
			work := workdir
			if work == "" {
				if wd, e := os.Getwd(); e == nil {
					work = wd
				}
			}

			// Config gate (default on).
			if cfg, e := config.Load(work); e == nil && cfg != nil && !cfg.Pipeline.ResolveGroundingGateEnabled() {
				fmt.Println("grounding gate disabled (pipeline.grounding_gate.enabled=false)")
				return nil
			}

			cur := branchFlag
			if cur == "" {
				cur = currentGitBranchFor(work)
			}

			ctx := readIssueContextForGrounding(work, issueNum)
			protected := []string{"main", "master"}
			if ctx.BaseBranch != "" {
				protected = append(protected, ctx.BaseBranch)
			}

			res := groundingGate.Evaluate(groundingGate.GroundingInput{
				IssueNumber:       issueNum,
				CurrentBranch:     cur,
				ExpectedBranch:    ctx.Branch,
				ContextPresent:    ctx.Present,
				ACCount:           ctx.ACCount,
				ProtectedBranches: protected,
			})

			if outputJSON {
				if err := printJSON(res); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON: %v\n", err)
				}
			} else {
				printGroundingHuman(issueNum, res)
			}
			if !res.Grounded {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().StringVar(&branchFlag, "current-branch", "", "Override the detected current branch (for tests/CI)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

func printGroundingHuman(issue int, r groundingGate.GroundingResult) {
	verdict := "GROUNDED"
	if !r.Grounded {
		verdict = "NOT GROUNDED"
	}
	fmt.Printf("Grounding gate #%d: %s (confidence=%s, recommendation=%s)\n", issue, verdict, r.Confidence, r.Recommendation)
	for _, reason := range r.Reasons {
		fmt.Printf("  - %s\n", reason)
	}
}

// issueContextFacts holds the grounding-relevant fields read from issue-{N}.json.
type issueContextFacts struct {
	Present    bool
	Branch     string
	BaseBranch string
	ACCount    int
}

// readIssueContextForGrounding reads the grounding-relevant fields from
// .nightgauge/pipeline/issue-{N}.json. A missing/unparseable file yields
// Present=false (the gate treats that as ungrounded).
func readIssueContextForGrounding(workdir string, issueNum int) issueContextFacts {
	path := filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("issue-%d.json", issueNum))
	data, err := os.ReadFile(path)
	if err != nil {
		return issueContextFacts{Present: false}
	}
	var raw struct {
		Branch       string `json:"branch"`
		BaseBranch   string `json:"base_branch"`
		Requirements struct {
			AcceptanceCriteria []string `json:"acceptance_criteria"`
		} `json:"requirements"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return issueContextFacts{Present: false}
	}
	return issueContextFacts{
		Present:    true,
		Branch:     raw.Branch,
		BaseBranch: raw.BaseBranch,
		ACCount:    len(raw.Requirements.AcceptanceCriteria),
	}
}

// currentGitBranchFor returns the current branch via git, or "" on failure.
func currentGitBranchFor(workdir string) string {
	out, err := exec.Command("git", "-C", workdir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
