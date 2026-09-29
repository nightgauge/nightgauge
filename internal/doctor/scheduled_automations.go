package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// cadenceProbe resolves one automation's freshness evidence.
type cadenceProbe func(ctx context.Context, a cadence.Automation) cadence.Evidence

// autonomousStateEvidence reads the daemon's own lastScanAt.
//
// A missing state file is NOT an error and NOT "never ran" in the alarming
// sense — but it is still reported, because a workspace with no autonomous
// state has never started the loop, which is exactly the condition worth
// naming on a workspace that is supposed to be running unattended.
func autonomousStateEvidence(workspaceRoot string) cadenceProbe {
	return func(context.Context, cadence.Automation) cadence.Evidence {
		path := filepath.Join(workspaceRoot, ".nightgauge", "autonomous", "state.json")
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return cadence.Evidence{EverRan: false}
		}
		if err != nil {
			return cadence.Evidence{Err: err}
		}
		var st struct {
			Status     string `json:"status"`
			LastScanAt string `json:"lastScanAt"`
		}
		if err := json.Unmarshal(data, &st); err != nil {
			return cadence.Evidence{Err: err}
		}
		if strings.TrimSpace(st.LastScanAt) == "" {
			return cadence.Evidence{EverRan: false}
		}
		ts, err := time.Parse(time.RFC3339, st.LastScanAt)
		if err != nil {
			return cadence.Evidence{Err: fmt.Errorf("unparseable lastScanAt %q: %w", st.LastScanAt, err)}
		}
		return cadence.Evidence{Newest: ts, EverRan: true}
	}
}

// workflowRunEvidence reads the newest run of a GitHub Actions workflow.
//
// Zero runs is EverRan:false, which is the whole reason this arm exists: a
// workflow that has never fired has no failed run to report and is invisible to
// every other detector in the product.
func workflowRunEvidence(client *gh.Client, defaultOwner, defaultRepo string) cadenceProbe {
	return func(ctx context.Context, a cadence.Automation) cadence.Evidence {
		if client == nil {
			return cadence.Evidence{Err: fmt.Errorf("no authenticated GitHub client")}
		}
		owner, repo := defaultOwner, defaultRepo
		if a.Repo != "" {
			if o, r, ok := splitOwnerRepoSlug(a.Repo); ok {
				owner, repo = o, r
			}
		}
		if owner == "" || repo == "" {
			return cadence.Evidence{Err: fmt.Errorf("no repository resolved for %s", a.ID)}
		}
		// branch "" so a run on any branch counts. a.TriggerEvent narrows it to
		// the event that proves the SCHEDULE fired — without that, a hand
		// dispatch makes a dead cron look healthy.
		runs, err := gh.NewCIService(client).ListWorkflowRunsByEvent(
			ctx, owner, repo, a.Workflow, "", a.TriggerEvent, 1)
		if err != nil {
			return cadence.Evidence{Err: err}
		}
		if len(runs) == 0 {
			return cadence.Evidence{EverRan: false}
		}
		ts, err := time.Parse(time.RFC3339, runs[0].CreatedAt)
		if err != nil {
			return cadence.Evidence{Err: fmt.Errorf("unparseable created_at %q: %w", runs[0].CreatedAt, err)}
		}
		return cadence.Evidence{Newest: ts, EverRan: true}
	}
}

func splitOwnerRepoSlug(slug string) (string, string, bool) {
	idx := strings.Index(slug, "/")
	if idx <= 0 || idx == len(slug)-1 {
		return "", "", false
	}
	return slug[:idx], slug[idx+1:], true
}

// evaluateCadence runs every registered automation's probe and returns the
// verdicts, most-stale first.
func evaluateCadence(ctx context.Context, probes map[cadence.EvidenceKind]cadenceProbe, scope cadence.Scope, declared []cadence.ConfigAutomation, now time.Time) ([]cadence.Verdict, []error) {
	registry, cfgErrs := cadence.Merge(scope, declared)
	verdicts := make([]cadence.Verdict, 0, len(registry))
	for _, a := range registry {
		probe, ok := probes[a.Kind]
		if !ok {
			verdicts = append(verdicts, cadence.Evaluate(a,
				cadence.Evidence{Err: fmt.Errorf("no probe registered for evidence kind %q", a.Kind)},
				now, cadence.DefaultStaleMultiple))
			continue
		}
		verdicts = append(verdicts, cadence.Evaluate(a, probe(ctx, a), now, cadence.DefaultStaleMultiple))
	}
	// Never-ran first, then oldest — the operator reads the top of the list.
	sort.SliceStable(verdicts, func(i, j int) bool {
		rank := func(v cadence.Verdict) int {
			switch v.Status {
			case cadence.StatusNeverRan:
				return 0
			case cadence.StatusStale:
				return 1
			case cadence.StatusUnknown:
				return 2
			default:
				return 3
			}
		}
		if rank(verdicts[i]) != rank(verdicts[j]) {
			return rank(verdicts[i]) < rank(verdicts[j])
		}
		return verdicts[i].Age > verdicts[j].Age
	})
	return verdicts, cfgErrs
}

// Codes for scheduled_automations. Never-ran, stopped and unverifiable have
// different causes and different fixes, so each is its own code: a single
// "stale" verdict for all three sends the operator to look at the wrong half.
const (
	codeAutomationStopped      = "NGD029"
	codeAutomationNeverRan     = "NGD030"
	codeAutomationUnverifiable = "NGD031"
)

// scheduledAutomationFindings reports registered automations that stopped
// firing, never fired, or cannot be read (#996), one finding per automation.
//
// The general absence detector: the survival backlog and corpus calibration
// arms each notice one specific thing having stopped. This notices the CLASS —
// anything registered whose evidence has gone quiet.
//
// A stopped automation the operator recorded as intentionally paused (see
// automation_pause.go) is still reported, as info, until it is resumed.
//
// restartable reports whether the autonomous loop can be restarted from doctor
// now (nil error): only then is the confirm restart offered. It is asked at
// most once per scan, and only when the loop has stopped. A nil func means
// no entry point, and the manual start steps are offered instead.
func scheduledAutomationFindings(ctx context.Context, probes map[cadence.EvidenceKind]cadenceProbe, scope cadence.Scope, declared []cadence.ConfigAutomation, pauses map[string]AutomationPause, restartable func() error, now time.Time) ([]Finding, string) {
	const check = "scheduled_automations"
	verdicts, cfgErrs := evaluateCadence(ctx, probes, scope, declared, now)
	if len(verdicts) == 0 && len(cfgErrs) == 0 {
		return nil, "none registered (declare repo automations under automations.cadence)"
	}
	var restartErr error
	restartAsked := false
	restartRemedyFor := func(a cadence.Automation) Remedy {
		if a.Kind == cadence.EvidenceAutonomousState && !restartAsked {
			restartAsked = true
			restartErr = errNoAutonomousStarter
			if restartable != nil {
				restartErr = restartable()
			}
		}
		return restartRemedy(a, restartErr)
	}

	var out []Finding
	var never, stale, unknown, paused int
	for _, v := range verdicts {
		a := v.Automation
		ev := map[string]string{
			"automation":        a.ID,
			"description":       a.Description,
			"expected_interval": a.Interval.String(),
			"evidence_kind":     string(a.Kind),
		}
		if a.Workflow != "" {
			ev["workflow"] = a.Workflow
		}
		if a.Repo != "" {
			ev["repo"] = a.Repo
		}
		if !v.Newest.IsZero() {
			ev["last_ran"] = v.Newest.UTC().Format(time.RFC3339)
		}
		identity := []string{a.ID, a.Repo, a.Workflow}
		switch v.Status {
		case cadence.StatusNeverRan:
			never++
			out = append(out, newFinding(check, codeAutomationNeverRan, SeverityWarning,
				fmt.Sprintf("scheduled automation %s has never run", a.ID),
				"no evidence of a run has ever existed: usually a schedule that was never valid (a cron on a branch GitHub does not schedule from, or a trigger that never matched)",
				ev, identity,
				manualRemedy("schedule", "Make the schedule valid, then confirm one run", check,
					automationRemedySteps(a)...)))
		case cadence.StatusStale:
			ev["age"] = v.Age.Round(time.Minute).String()
			title := fmt.Sprintf("scheduled automation %s stopped: %s", a.ID, v.Detail)
			if p, ok := pauses[a.ID]; ok {
				paused++
				ev["paused_at"] = p.PausedAt.Format(time.RFC3339)
				if p.Reason != "" {
					ev["pause_reason"] = p.Reason
				}
				out = append(out, newFinding(check, codeAutomationStopped, SeverityInfo,
					fmt.Sprintf("scheduled automation %s is paused (stopped: %s)", a.ID, v.Detail),
					"the operator recorded this automation as intentionally paused; it is reported until it is resumed",
					ev, identity,
					restartRemedyFor(a),
					manualRemedy("resume", "Resume watching it as a live automation", check,
						fmt.Sprintf("Run `nightgauge doctor automation resume %s`", a.ID))))
				continue
			}
			stale++
			out = append(out, newFinding(check, codeAutomationStopped, SeverityWarning, title,
				"it ran before and has since stopped producing evidence: usually a process that died or a credential that expired",
				ev, identity,
				restartRemedyFor(a),
				manualRemedy("pause", "Mark it as intentionally paused", check,
					fmt.Sprintf("Run `nightgauge doctor automation pause %s --reason \"<why>\"`", a.ID),
					"The finding stays visible as info until `nightgauge doctor automation resume` is run")))
		case cadence.StatusUnknown:
			unknown++
			out = append(out, newFinding(check, codeAutomationUnverifiable, SeverityWarning,
				fmt.Sprintf("scheduled automation %s is unverifiable: %s", a.ID, v.Detail),
				"its status could not be read, and an automation whose freshness is unknown is not a healthy one",
				ev, identity,
				manualRemedy("probe", "Make the automation's evidence readable", check,
					"Check GitHub authentication (`nightgauge doctor` github checks) for workflow evidence",
					"Check that .nightgauge/autonomous/state.json is readable for the autonomous loop")))
		}
	}
	// A malformed entry is reported, never dropped. An operator who declared an
	// automation believes it is watched; silently skipping it reproduces this
	// package's own failure one level up.
	for _, e := range cfgErrs {
		unknown++
		out = append(out, newFinding(check, codeAutomationUnverifiable, SeverityWarning,
			"declared automation is invalid: "+e.Error(),
			"a malformed automations.cadence entry cannot be evaluated, so the automation it names is not watched",
			map[string]string{"error": e.Error()}, []string{"config", e.Error()},
			manualRemedy("fix-config", "Correct the automations.cadence entry in .nightgauge/config.yaml", check)))
	}
	detail := fmt.Sprintf("%d never ran, %d stopped, %d paused, %d unverifiable (of %d registered)",
		never, stale, paused, unknown, len(verdicts))
	if len(out) == 0 {
		detail = fmt.Sprintf("all %d registered automation(s) are firing on schedule", len(verdicts))
	}
	return out, detail
}

// restartRemedy restarts a stopped automation through the entry point that
// normally starts it (#2090). For the autonomous loop that is the daemon's
// scheduler start, offered as a confirm remedy on the automation.restart verb
// when restartErr is nil, and as manual start steps otherwise. A workflow is
// normally started by GitHub's own scheduler, which doctor cannot invoke: a
// workflow_dispatch is a different trigger, and it would make a dead cron look
// alive to the probe that filters on the schedule event. Its restart is manual.
func restartRemedy(a cadence.Automation, restartErr error) Remedy {
	const check = "scheduled_automations"
	if a.Kind == cadence.EvidenceAutonomousState {
		if restartErr == nil {
			return Remedy{ID: "restart", Kind: RemedyConfirm, Verb: verbAutomationRestart, Verify: check,
				Summary: "Restart the autonomous scheduler through the running daemon",
				Preview: fmt.Sprintf("ask the daemon serving this workspace to start its autonomous scheduler "+
					"(the entry point `nightgauge autonomous start` and the extension's Start use), "+
					"then wait up to %s for %s to record a scan", restartWait, a.ID)}
		}
		return manualRemedy("restart", "Start the autonomous scheduler", check, manualStartSteps(restartErr)...)
	}
	steps := []string{
		fmt.Sprintf("Check whether GitHub disabled the schedule: `gh workflow view %s`; re-enable it with `gh workflow enable %s`", a.Workflow, a.Workflow),
	}
	return manualRemedy("restart", fmt.Sprintf("Restore the schedule of workflow %s", a.Workflow), check,
		append(steps, automationRemedySteps(a)...)...)
}

func automationRemedySteps(a cadence.Automation) []string {
	steps := []string{}
	if a.Remedy != "" {
		steps = append(steps, a.Remedy)
	}
	if a.Workflow != "" {
		steps = append(steps, fmt.Sprintf("Check the trigger of %s: a cron fires only from the default branch", a.Workflow))
	}
	return steps
}
