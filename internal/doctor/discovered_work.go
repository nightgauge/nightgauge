package doctor

// The `orphaned_discovered_work` check (#478, ADR 027).
//
// A discovered-work capture file is deleted only by the #480 materializer,
// after it has filed the records. A run that ended before materializing
// leaves its file behind, and an auto-sweep would discard the very follow-ups
// the substrate exists to keep. So this check reports, one finding per file,
// and never deletes.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/nightgauge/nightgauge/internal/cmd/discoveredwork"
	"github.com/nightgauge/nightgauge/internal/layout"
)

const codeOrphanedDiscoveredWork = "NGD050"

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: "orphaned_discovered_work", Title: "Orphaned discovered work", Group: "hygiene",
		Code: codeOrphanedDiscoveredWork,
		Run: func(ctx context.Context, env *Env) []Finding {
			fs, detail := orphanedDiscoveredWorkFindings(env.Cwd, env.Now)
			env.SetDetail("orphaned_discovered_work", detail)
			return fs
		},
	})
}

// orphanedDiscoveredWorkFindings resolves the clone's pipeline state directory
// from cwd and reports its orphaned capture files.
func orphanedDiscoveredWorkFindings(cwd string, now time.Time) ([]Finding, string) {
	const check, code = "orphaned_discovered_work", codeOrphanedDiscoveredWork
	dir, err := layout.PipelineStateDir(cwd)
	if err != nil {
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "orphaned discovered work",
			fmt.Sprintf("could not resolve the pipeline state directory: %v", err))}, "could not scan for discovered-work files"
	}
	return discoveredWorkOrphanFindings(dir, now)
}

// discoveredWorkOrphanFindings reports one warning per orphaned capture file
// in dir. A missing dir means no run has written state: nothing to report.
func discoveredWorkOrphanFindings(dir string, now time.Time) ([]Finding, string) {
	const check, code = "orphaned_discovered_work", codeOrphanedDiscoveredWork
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return nil, "no discovered-work files"
	}
	orphans, err := discoveredwork.Orphans(dir, now, discoveredwork.DefaultOrphanAge)
	if err != nil {
		return []Finding{unverifiableFinding(check, code, SeverityWarning, "orphaned discovered work", err.Error())},
			"could not scan for discovered-work files"
	}
	if len(orphans) == 0 {
		return nil, "no orphaned discovered-work files"
	}
	out := make([]Finding, 0, len(orphans))
	for _, o := range orphans {
		n := strconv.Itoa(o.IssueNumber)
		cause := "its run ended (no issue-" + n + ".json) before the materializer filed its records"
		if o.Reason == discoveredwork.ReasonTooOld {
			cause = fmt.Sprintf("it is older than %s and has not been materialized", discoveredwork.DefaultOrphanAge)
		}
		out = append(out, newFinding(check, code, SeverityWarning,
			fmt.Sprintf("discovered work for #%s was never materialized", n), cause,
			map[string]string{"path": o.Path, "issue": n, "reason": o.Reason,
				"modified": o.ModTime.Format(time.RFC3339)},
			[]string{o.Path},
			manualRemedy("materialize", "File the recorded follow-ups, then let the materializer remove the file", check,
				"Not offered as a fix: deleting the file would discard the follow-up work it records",
				"Inspect it: nightgauge discovered-work list --issue "+n,
				"Run the materializer from the main checkout (it is the only deleter): "+
					"nightgauge followup materialize "+n+" --workdir <main checkout>")))
	}
	return out, fmt.Sprintf("%d orphaned discovered-work file(s)", len(orphans))
}
