package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/trust"
)

// eligibilityRow is one issue's verdict in `autonomous eligibility`.
type eligibilityRow struct {
	Number      int    `json:"number"`
	Author      string `json:"author"`
	Eligible    bool   `json:"eligible"`
	Via         string `json:"via,omitempty"`
	AuthorLevel string `json:"author_level,omitempty"`
	Approver    string `json:"approver,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

type eligibilityReport struct {
	Repo       string           `json:"repo"`
	Total      int              `json:"total"`
	Eligible   int              `json:"eligible"`
	Ineligible int              `json:"ineligible"`
	Issues     []eligibilityRow `json:"issues"`
}

// openIssueLister is the read `--all-open` makes.
type openIssueLister interface {
	OpenIssues(ctx context.Context, owner, repo string) ([]gh.OpenIssue, error)
}

// buildEligibilityReport decides each issue with one gate, so the report
// costs one permission call per distinct author (#2130, #842).
func buildEligibilityReport(ctx context.Context, gate *trust.Gate, lister openIssueLister, owner, repo string, numbers []int, allOpen bool) (eligibilityReport, error) {
	rep := eligibilityReport{Repo: owner + "/" + repo, Issues: []eligibilityRow{}}
	var subjects []trust.Subject
	if allOpen {
		open, err := lister.OpenIssues(ctx, owner, repo)
		if err != nil {
			return rep, err
		}
		for _, oi := range open {
			subjects = append(subjects, trust.Subject{Owner: owner, Repo: repo, Number: oi.Number,
				Author: oi.Author, Labels: oi.Labels, LabelsComplete: true})
		}
	}
	for _, n := range numbers {
		subjects = append(subjects, trust.Subject{Owner: owner, Repo: repo, Number: n})
	}
	for _, s := range subjects {
		d := gate.Decide(ctx, s)
		rep.Issues = append(rep.Issues, eligibilityRow{Number: s.Number, Author: d.Author, Eligible: d.Eligible,
			Via: d.Via, AuthorLevel: string(d.AuthorLevel), Approver: d.Approver, Reason: d.Reason})
		if d.Eligible {
			rep.Eligible++
		} else {
			rep.Ineligible++
		}
	}
	rep.Total = len(rep.Issues)
	return rep, nil
}

func autonomousEligibilityCmd() *cobra.Command {
	var (
		owner      string
		repo       string
		allOpen    bool
		outputJSON bool
	)
	cmd := &cobra.Command{
		Use:   "eligibility [issue-number...]",
		Short: "Report whether the pipeline may work issues: who vouched for each, or why not",
		Long: `Runs the eligibility gate (#2130) without dispatching anything. An issue is
eligible when a user with write access (or higher) to the repository authored
it, or applied ` + "`pipeline:approved`" + ` after its last edit, or a bot listed in
autonomous.trusted_bots authored it. Exit status 0 means every issue reported
is eligible; 1 means at least one is not.`,
		Example: `  nightgauge autonomous eligibility --all-open --json
  nightgauge autonomous eligibility 2130 2131 --repo nightgauge/nightgauge`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var numbers []int
			for _, a := range args {
				n, err := strconv.Atoi(a)
				if err != nil || n <= 0 {
					return fmt.Errorf("invalid issue number: %s", a)
				}
				numbers = append(numbers, n)
			}
			if !allOpen && len(numbers) == 0 {
				return errors.New("name issue numbers or pass --all-open")
			}
			ownerPart, repoPart := splitRepo(owner, repo)
			client, err := clientFromConfigForOwner(ownerPart)
			if err != nil {
				return err
			}
			var bots []string
			var appOwners, logins []string
			if wd, err := os.Getwd(); err == nil {
				if cfg, err := config.Load(wd); err == nil && cfg != nil {
					appOwners = cfg.AppOwners()
					logins = cfg.ConfiguredGitHubLogins()
					if cfg.Autonomous != nil {
						bots = cfg.Autonomous.TrustedBots
					}
				}
			}
			forge := gh.NewTrustForge(client)
			gate := trust.NewGate(forge, bots).WithAppOwners(appOwners...).
				WithStageIdentities(func(ctx context.Context) ([]string, error) { return gh.StageIdentities(ctx, logins) })
			if !gate.LabelApprovalAvailable(ownerPart) {
				fmt.Fprintf(os.Stderr, "note: label approval (%s) is unavailable for %s: no GitHub App installation is "+
					"configured there, so only issues opened by users with write access are eligible\n", trust.ApprovedLabel, ownerPart)
			}
			rep, err := buildEligibilityReport(cmd.Context(), gate, forge, ownerPart, repoPart, numbers, allOpen)
			if err != nil {
				return err
			}
			if outputJSON {
				if err := printJSON(rep); err != nil {
					return err
				}
			} else {
				for _, r := range rep.Issues {
					if r.Eligible {
						fmt.Printf("#%d  eligible  (%s, @%s)\n", r.Number, r.Via, r.Author)
					} else {
						fmt.Printf("#%d  NOT eligible  %s\n", r.Number, r.Reason)
					}
				}
				fmt.Printf("%s: %d issue(s), %d eligible, %d not\n", rep.Repo, rep.Total, rep.Eligible, rep.Ineligible)
			}
			if rep.Ineligible > 0 {
				cmd.SilenceUsage = true
				return fmt.Errorf("%d of %d issue(s) in %s are not eligible", rep.Ineligible, rep.Total, rep.Repo)
			}
			return nil
		},
	}
	ownerFlag(cmd, &owner, "GitHub organization")
	requiredRepoNameFlag(cmd, &repo, "Repository (owner/name or name)")
	cmd.Flags().BoolVar(&allOpen, "all-open", false, "Report every open issue in the repository")
	cmd.Flags().BoolVar(&outputJSON, "json", false, "Output as JSON")
	return cmd
}
