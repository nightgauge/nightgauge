package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/intelligence/approvalGate"
	"github.com/spf13/cobra"
)

// approvalGateCmd runs `nightgauge approval-gate <issue-number>` — the
// architecture-approval gate (#4098). A high-impact architectural decision
// (≥2 trade-off signals in the issue/ADR text, or routing risk_high) blocks
// feature-dev (exit 1) until a human grants approval out-of-band: the approval
// label on the issue, or an approval file. It is a deterministic gate (not an
// interactive prompt) so it is exempt from auto_accept_stages by construction.
//
// Exit codes:
//
//	0 — proceed (not high-impact, or already human-approved, or disabled)
//	1 — REQUIRES human approval (high-impact + unapproved)
//	2 — invalid arguments
func approvalGateCmd() *cobra.Command {
	var (
		workdir    string
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "approval-gate <issue-number>",
		Short: "Block a high-impact architectural decision until a human approves it (#4098)",
		Long: `High-impact architectural decisions stay human-owned. When the issue/ADR text
shows ≥2 distinct trade-off signals (consistency/availability, build/buy,
reuse/build, …) or the issue is high-risk (routing risk_high, #4093), this gate
requires a human to approve the decision before feature-dev proceeds.

Approval is granted out-of-band — the approval label on the issue (default
"approved:architecture") or an approval file — so the gate is exempt from
human_in_the_loop.auto_accept_stages. Disabled with
pipeline.architecture_approval.enabled: false.`,
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

			var pc *config.PipelineConfig
			if cfg, e := config.Load(work); e == nil && cfg != nil {
				pc = cfg.Pipeline
			}
			if !pc.ResolveArchitectureApprovalEnabled() {
				// Honor --json even on the disabled short-circuit. Callers like
				// the VSCode pre-check parse stdout as JSON on exit 0 — a plain
				// text line here made every disabled-gate run log a spurious
				// "binary error" (observed in bowlsheet dogfooding 2026-07-11).
				if outputJSON {
					if err := printJSON(approvalGate.ApprovalResult{
						Reasons: []string{"gate disabled (pipeline.architecture_approval.enabled=false)"},
					}); err != nil {
						fmt.Fprintf(os.Stderr, "warning: failed to encode JSON: %v\n", err)
					}
				} else {
					fmt.Println("architecture-approval gate disabled (pipeline.architecture_approval.enabled=false)")
				}
				return nil
			}
			label := pc.ResolveArchitectureApprovalLabel()

			facts := readApprovalFacts(work, issueNum)
			text := strings.Join(append([]string{facts.Summary}, append(facts.TechNotes, facts.DecisionsText)...), "\n")
			hits := approvalGate.CountTradeoffSignals(text)
			granted := containsFold(facts.Labels, label) || approvalFileGranted(work, issueNum)

			res := approvalGate.Evaluate(approvalGate.ApprovalInput{
				IssueNumber:              issueNum,
				TradeoffKeywordHits:      hits,
				RiskHigh:                 facts.RiskHigh,
				DependencyMajorBumpCount: facts.DependencyMajorBumps,
				IsProductionChange:       facts.IsProductionChange,
				ApprovalGranted:          granted,
			})

			if outputJSON {
				if err := printJSON(res); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to encode JSON: %v\n", err)
				}
			} else {
				printApprovalHuman(issueNum, res, label)
			}
			if res.RequiresApproval {
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&workdir, "workdir", "", "Workspace root (default: cwd)")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output result as JSON")
	return cmd
}

func printApprovalHuman(issue int, r approvalGate.ApprovalResult, label string) {
	if r.RequiresApproval {
		fmt.Printf("Architecture-approval gate #%d: APPROVAL REQUIRED\n", issue)
	} else if r.HighImpact {
		fmt.Printf("Architecture-approval gate #%d: high-impact, approved ✓\n", issue)
	} else {
		fmt.Printf("Architecture-approval gate #%d: not high-impact — proceed\n", issue)
	}
	for _, reason := range r.Reasons {
		fmt.Printf("  - %s\n", reason)
	}
	if r.RequiresApproval {
		fmt.Printf("  → A human must review the decision and add the %q label to the issue (or write .nightgauge/pipeline/approval-%d.json with {\"approved\": true}).\n", label, issue)
	}
}

// approvalFacts holds the approval-relevant fields read from issue-{N}.json + ADR.
type approvalFacts struct {
	RiskHigh      bool
	Labels        []string
	Summary       string
	TechNotes     []string
	DecisionsText string
	// DependencyMajorBumps + IsProductionChange come from feature-planning's
	// dependency_analysis block (#4135). Absent block → zero/false, so the
	// triggers never over-fire when feature-planning emitted nothing.
	DependencyMajorBumps int
	IsProductionChange   bool
}

func readApprovalFacts(workdir string, issueNum int) approvalFacts {
	facts := approvalFacts{}
	path := filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("issue-%d.json", issueNum))
	data, err := os.ReadFile(path)
	if err != nil {
		return facts
	}
	var raw struct {
		KnowledgePath string `json:"knowledge_path"`
		Routing       struct {
			RiskHigh bool `json:"risk_high"`
		} `json:"routing"`
		Requirements struct {
			Summary        string          `json:"summary"`
			TechnicalNotes json.RawMessage `json:"technical_notes"`
		} `json:"requirements"`
		// DependencyAnalysis is feature-planning's risk-tiering fact block
		// (#4135). A pointer so an absent block stays nil and the triggers
		// default off — never over-firing on missing data.
		DependencyAnalysis *struct {
			MajorBumpsCount int  `json:"major_bumps_count"`
			ProductionArea  bool `json:"production_area"`
		} `json:"dependency_analysis"`
		Labels json.RawMessage `json:"labels"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return facts
	}
	facts.RiskHigh = raw.Routing.RiskHigh
	facts.Summary = raw.Requirements.Summary
	facts.TechNotes = flexStringSlice(raw.Requirements.TechnicalNotes)
	facts.Labels = flexLabels(raw.Labels)
	if raw.DependencyAnalysis != nil {
		facts.DependencyMajorBumps = raw.DependencyAnalysis.MajorBumpsCount
		facts.IsProductionChange = raw.DependencyAnalysis.ProductionArea
	}

	// ADR text from the issue's knowledge directory, if present.
	if raw.KnowledgePath != "" {
		if b, e := os.ReadFile(filepath.Join(workdir, raw.KnowledgePath, "decisions.md")); e == nil {
			facts.DecisionsText = string(b)
		}
	}
	return facts
}

// approvalFileGranted reports whether an approval file grants approval.
func approvalFileGranted(workdir string, issueNum int) bool {
	b, err := os.ReadFile(filepath.Join(workdir, ".nightgauge", "pipeline", fmt.Sprintf("approval-%d.json", issueNum)))
	if err != nil {
		return false
	}
	var v struct {
		Approved bool `json:"approved"`
	}
	return json.Unmarshal(b, &v) == nil && v.Approved
}

// flexStringSlice coerces technical_notes (string | []string | object) to []string.
func flexStringSlice(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []string{s}
	}
	return nil
}

// flexLabels coerces labels ([]string | [{name}]) to []string.
func flexLabels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, s)
			continue
		}
		var obj struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(it, &obj) == nil && obj.Name != "" {
			out = append(out, obj.Name)
		}
	}
	return out
}

func containsFold(haystack []string, needle string) bool {
	for _, h := range haystack {
		if strings.EqualFold(strings.TrimSpace(h), needle) {
			return true
		}
	}
	return false
}
