package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/ipc"
	"github.com/nightgauge/nightgauge/internal/platform"
	"github.com/spf13/cobra"
)

// Exit codes of `nightgauge inquiry scorecard` (#2911). Each names one reason
// the scorecard could not be read, so a skill can say which.
const (
	inquiryExitPlatformError     = 1
	inquiryExitDaemonUnreachable = 3
	inquiryExitNotConfigured     = 4
	inquiryExitNotSignedIn       = 5
)

// inquiryScorecardTimeout bounds the whole read through the daemon.
const inquiryScorecardTimeout = 30 * time.Second

// fetchInquiryScorecardFn reads the scorecard through the daemon. Tests swap
// it. A dial failure is returned as errInquiryDaemonUnreachable.
var fetchInquiryScorecardFn = func(ctx context.Context, root string, p ipc.PlatformInquiryScorecardParams) (*platform.InquiryScorecard, error) {
	client, err := ipc.DialDaemon(ctx, root, daemonDialTimeout)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInquiryDaemonUnreachable, err)
	}
	defer client.Close()
	var sc platform.InquiryScorecard
	if err := client.Call(ctx, "platform.getInquiryScorecard", p, &sc); err != nil {
		return nil, err
	}
	return &sc, nil
}

var errInquiryDaemonUnreachable = fmt.Errorf("no nightgauge daemon is reachable")

func inquiryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inquiry",
		Short: "Read the platform's inquiry measurements",
	}
	cmd.AddCommand(inquiryScorecardCmd())
	return cmd
}

func inquiryScorecardCmd() *cobra.Command {
	var (
		window    string
		floor     float64
		minSample int
		asJSON    bool
		workdir   string
	)
	cmd := &cobra.Command{
		Use:   "scorecard",
		Short: "Show the platform's inquiry scorecard",
		Long: `Read the inquiry scorecard (GET /v1/inquiry/scorecard) from the platform.

The route takes a signed-in session, which only the running daemon holds, so
the read goes through ` + "`nightgauge serve`" + `. Every number is the platform's;
this command computes none of them.

Exit codes: 0 read (or no answers in the window), 1 platform error,
3 daemon unreachable, 4 platform not configured, 5 not signed in.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := resolveAttentionWorkdir(workdir)
			if err != nil {
				return err
			}
			p := ipc.PlatformInquiryScorecardParams{Window: window}
			if cmd.Flags().Changed("floor") {
				p.Floor = &floor
			}
			if cmd.Flags().Changed("min-sample") {
				p.MinSample = &minSample
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), inquiryScorecardTimeout)
			defer cancel()
			sc, err := fetchInquiryScorecardFn(ctx, root, p)
			if err != nil {
				code, msg := classifyInquiryError(err)
				fmt.Fprintln(cmd.ErrOrStderr(), msg)
				return verdictExit{code: code}
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(sc)
			}
			renderInquiryScorecard(out, sc)
			return nil
		},
	}
	cmd.Flags().StringVar(&window, "window", "", "window as a day count, e.g. 30d (platform default 30d)")
	cmd.Flags().Float64Var(&floor, "floor", 0, "impact ratio under which a class is weak, 0..1")
	cmd.Flags().IntVar(&minSample, "min-sample", 0, "fewest answers a class needs to be judged weak")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the platform's scorecard object unchanged")
	cmd.Flags().StringVar(&workdir, "workdir", "", "workspace whose daemon to ask (default: cwd)")
	return cmd
}

func classifyInquiryError(err error) (int, string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, errInquiryDaemonUnreachable.Error()):
		return inquiryExitDaemonUnreachable, "inquiry scorecard: no nightgauge daemon is reachable; start `nightgauge serve` or open the workspace in VS Code"
	case strings.Contains(msg, ipc.ErrMsgPlatformNotConfigured):
		return inquiryExitNotConfigured, "inquiry scorecard: no platform is configured for this workspace"
	case strings.Contains(msg, ipc.ErrMsgPlatformNotSignedIn):
		return inquiryExitNotSignedIn, "inquiry scorecard: not signed in to the platform; sign in from the VS Code extension"
	default:
		return inquiryExitPlatformError, "inquiry scorecard: " + msg
	}
}

// renderInquiryScorecard prints the scorecard as the platform sent it. It
// formats numbers and does no arithmetic on them.
func renderInquiryScorecard(w io.Writer, sc *platform.InquiryScorecard) {
	if sc.Empty() {
		fmt.Fprintf(w, "No inquiry answers in window %s.\n", sc.Window)
		return
	}
	fmt.Fprintf(w, "Inquiry scorecard — window %s (%s → %s), floor %g, min sample %d\n\n",
		sc.Window, sc.WindowStart, sc.WindowEnd, sc.Floor, sc.MinSample)
	o := sc.Overall
	fmt.Fprintf(w, "Overall: impact ratio %g — %d answers, %d impacted, %d unattributed, %d none\n",
		o.ImpactRatio, o.Answers, o.Impacted, o.Unattributed, o.None)
	if len(sc.ByClass) > 0 {
		fmt.Fprintf(w, "\n%-20s %8s %8s %12s %6s %8s\n", "Class", "Answers", "Impacted", "Unattributed", "None", "Ratio")
		for _, c := range sc.ByClass {
			fmt.Fprintf(w, "%-20s %8d %8d %12d %6d %8g\n", c.Class, c.Answers, c.Impacted, c.Unattributed, c.None, c.ImpactRatio)
		}
	}
	if sc.MedianTimeToAnswerSeconds != nil {
		fmt.Fprintf(w, "\nMedian time to answer: %gs\n", *sc.MedianTimeToAnswerSeconds)
	} else {
		fmt.Fprintln(w, "\nMedian time to answer: n/a")
	}
	s := sc.Sets
	fmt.Fprintf(w, "Sets: %d total, %d answered, %d waived, %d scored; waive rate %g, skip rate %g, question skip rate %g\n",
		s.Total, s.Answered, s.Waived, s.Scored, s.WaiveRate, s.SkipRate, s.QuestionSkipRate)
	fmt.Fprintf(w, "Kills: %d, spend avoided $%.2f\n", sc.Kills.Count, sc.Kills.SpendAvoidedUSD)
	fmt.Fprintf(w, "Unattributed targets: %d\n", sc.UnattributedTargets)
	h := sc.HistoryComparison
	fmt.Fprintf(w, "Cold start: %d sets, %d answers, ratio %g; history rich: %d sets, %d answers, ratio %g\n",
		h.ColdStart.Sets, h.ColdStart.Answers, h.ColdStart.ImpactRatio,
		h.HistoryRich.Sets, h.HistoryRich.Answers, h.HistoryRich.ImpactRatio)
	if len(sc.WeakClasses) > 0 {
		fmt.Fprintln(w, "\nWeak classes:")
		for _, c := range sc.WeakClasses {
			fmt.Fprintf(w, "  %s: ratio %g under floor %g over %d answers — %s\n",
				c.Class, c.ImpactRatio, c.Floor, c.Answers, c.Recommendation)
		}
	}
}
