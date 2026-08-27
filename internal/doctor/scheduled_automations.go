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
func evaluateCadence(ctx context.Context, probes map[cadence.EvidenceKind]cadenceProbe, declared []cadence.ConfigAutomation, now time.Time) ([]cadence.Verdict, []error) {
	registry, cfgErrs := cadence.Merge(declared)
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

// checkScheduledAutomations reports registered automations that have stopped
// firing, or never fired at all (#996).
//
// The third absence detector in this package, and the general one: the survival
// backlog and corpus calibration arms each notice one specific thing having
// stopped. This notices the CLASS — anything registered whose evidence has gone
// quiet — which is what makes registering a new scheduled workflow the only
// work required to have its silence noticed.
func checkScheduledAutomations(ctx context.Context, probes map[cadence.EvidenceKind]cadenceProbe, declared []cadence.ConfigAutomation, now time.Time) (CheckItem, string) {
	verdicts, cfgErrs := evaluateCadence(ctx, probes, declared, now)

	var never, stale, unknown []string
	for _, v := range verdicts {
		line := fmt.Sprintf("%s (%s)", v.Automation.ID, v.Detail)
		switch v.Status {
		case cadence.StatusNeverRan:
			never = append(never, line)
		case cadence.StatusStale:
			stale = append(stale, line)
		case cadence.StatusUnknown:
			unknown = append(unknown, line)
		}
	}
	// A malformed entry is reported, never dropped. An operator who declared an
	// automation believes it is watched; silently skipping it reproduces this
	// package's own failure one level up.
	for _, e := range cfgErrs {
		unknown = append(unknown, e.Error())
	}

	if len(never) == 0 && len(stale) == 0 && len(unknown) == 0 {
		return CheckItem{
			OK:     true,
			Detail: fmt.Sprintf("all %d registered automation(s) are firing on schedule", len(verdicts)),
		}, ""
	}

	// NEVER RAN and STALE are reported separately and in that order. They have
	// different causes — a schedule that was never valid vs. one that stopped —
	// and different fixes, so a single "stale" verdict for both sends the
	// operator to look at the wrong half.
	var parts []string
	if len(never) > 0 {
		parts = append(parts, "NEVER RAN: "+strings.Join(never, "; "))
	}
	if len(stale) > 0 {
		parts = append(parts, "STOPPED: "+strings.Join(stale, "; "))
	}
	if len(unknown) > 0 {
		parts = append(parts, "UNVERIFIABLE: "+strings.Join(unknown, "; "))
	}

	msg := "scheduled-automation-stale: " + strings.Join(parts, " | ") +
		" — an automation nobody notices stopping is the failure unattended operation cannot survive"
	return CheckItem{
		OK: false,
		Detail: fmt.Sprintf("%d never ran, %d stopped, %d unverifiable (of %d registered)",
			len(never), len(stale), len(unknown), len(verdicts)),
		Error: msg,
	}, msg
}
