package cadence

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Registry is every automation whose silence should be noticed.
//
// THIS IS THE POINT OF THE ISSUE, AND IT IS DATA ON PURPOSE. The four entries
// below were each found dark by a human looking directly at them; none had an
// issue and none raised anything. Prose in a runbook cannot be evaluated, so a
// registry that is a doc is a registry that goes stale silently — the same
// failure one level up.
//
// ADDING A SCHEDULED WORKFLOW WITHOUT REGISTERING IT IS THE DEFECT THIS EXISTS
// TO PREVENT. A cron nobody registered is a cron nobody will notice stopping.
//
// ONLY THIS REPOSITORY'S OWN AUTOMATIONS ARE BUILT IN. A workspace's other
// repos — including private ones — register through
// `automations.cadence` in config.yaml and merge via Merge(). Hardcoding one
// workspace's repo slugs into a shipped product would be wrong even without the
// public-core boundary that forbids naming a private companion repo here.
func Registry() []Automation {
	return []Automation{
		{
			ID:          "autonomous-loop",
			Description: "the autonomous scheduler's scan loop",
			// Continuous. The scan interval is seconds, but the freshness
			// evidence is a persisted state field, so an hour is the honest
			// granularity — and 3x an hour still catches a stop the same day.
			Interval: time.Hour,
			Kind:     EvidenceAutonomousState,
			Remedy:   "start autonomous mode from the VSCode extension, or `nightgauge serve`",
		},
		{
			ID:          "release-workflow",
			Description: "the release workflow that publishes a tagged build",
			// Tag-triggered rather than cron. Registered anyway with a
			// deliberately long interval: this one has NEVER run, and
			// "never ran" is the verdict that matters here — it is reported
			// regardless of interval, because no elapsed time makes zero runs
			// acceptable for a release path nobody has ever exercised.
			Interval: 90 * 24 * time.Hour,
			Kind:     EvidenceWorkflowRun,
			Workflow: "release.yml",
			// No TriggerEvent: this one is tag-triggered, and the verdict that
			// matters is that it has never run AT ALL.
			Remedy: "cut a stable tag; the Marketplace publish is a separate manual dispatch (marketplace-publish.yml) gated on VSCE_PAT",
		},
	}
}

// ByID returns the registered automation with the given id.
func ByID(id string) (Automation, bool) {
	for _, a := range Registry() {
		if a.ID == id {
			return a, true
		}
	}
	return Automation{}, false
}

// ConfigAutomation is one operator-declared entry, as it appears under
// `automations.cadence` in config.yaml.
//
// Separate from Automation so the YAML shape can use a human interval string
// ("24h", "7d") rather than a Go duration, and so a malformed entry is rejected
// with a reason instead of silently becoming a zero-interval automation that
// reports everything stale.
type ConfigAutomation struct {
	ID           string `yaml:"id" json:"id"`
	Description  string `yaml:"description" json:"description"`
	Interval     string `yaml:"interval" json:"interval"`
	Repo         string `yaml:"repo,omitempty" json:"repo,omitempty"`
	Workflow     string `yaml:"workflow" json:"workflow"`
	TriggerEvent string `yaml:"trigger_event,omitempty" json:"triggerEvent,omitempty"`
	Remedy       string `yaml:"remedy,omitempty" json:"remedy,omitempty"`
}

// Merge returns the built-in registry plus the operator's declared entries.
//
// Invalid entries are RETURNED AS ERRORS, not skipped. A silently-dropped entry
// is an automation the operator believes is watched and is not — the precise
// failure this package exists to remove, reproduced one level up.
//
// An operator entry may override a built-in of the same id, so a workspace can
// correct an interval without forking the registry.
func Merge(declared []ConfigAutomation) ([]Automation, []error) {
	out := Registry()
	var errs []error

	for _, d := range declared {
		if strings.TrimSpace(d.ID) == "" {
			errs = append(errs, fmt.Errorf("cadence entry with no id (workflow %q) — findings key on the id", d.Workflow))
			continue
		}
		interval, err := time.ParseDuration(normalizeInterval(d.Interval))
		if err != nil || interval <= 0 {
			errs = append(errs, fmt.Errorf("cadence entry %q has interval %q: must be a positive duration like \"24h\" or \"7d\"", d.ID, d.Interval))
			continue
		}
		if strings.TrimSpace(d.Workflow) == "" {
			errs = append(errs, fmt.Errorf("cadence entry %q names no workflow file", d.ID))
			continue
		}
		a := Automation{
			ID:           d.ID,
			Description:  d.Description,
			Interval:     interval,
			Kind:         EvidenceWorkflowRun,
			Repo:         d.Repo,
			Workflow:     d.Workflow,
			TriggerEvent: d.TriggerEvent,
			Remedy:       d.Remedy,
		}
		if a.Description == "" {
			a.Description = d.Workflow
		}
		if a.Remedy == "" {
			a.Remedy = "check the workflow's trigger and that its cron is on the default branch"
		}

		replaced := false
		for i := range out {
			if out[i].ID == a.ID {
				out[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, a)
		}
	}
	return out, errs
}

// normalizeInterval accepts "7d" alongside Go's duration syntax, because a
// weekly cron is the common case and `168h` is not how anyone writes it.
func normalizeInterval(v string) string {
	v = strings.TrimSpace(v)
	if days, ok := strings.CutSuffix(v, "d"); ok {
		if n, err := strconv.Atoi(days); err == nil {
			return strconv.Itoa(n*24) + "h"
		}
	}
	return v
}
