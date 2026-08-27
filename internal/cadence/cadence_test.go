package cadence

import (
	"errors"
	"testing"
	"time"
)

var base = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func daily() Automation {
	return Automation{ID: "daily-thing", Interval: 24 * time.Hour}
}

// TestEvaluate_DistinguishesNeverRanFromStale is acceptance criterion 4, and it
// is the one that decides whether this check is useful.
//
// "Never ran" and "ran and stopped" have different causes — a schedule that was
// never valid (a cron on a non-default branch) vs. a process that died — and
// different fixes. A single "stale" verdict for both sends the operator to look
// at the wrong half, which is a worse signal than none.
func TestEvaluate_DistinguishesNeverRanFromStale(t *testing.T) {
	never := Evaluate(daily(), Evidence{EverRan: false}, base, DefaultStaleMultiple)
	if never.Status != StatusNeverRan {
		t.Errorf("status = %q, want %q for an automation with no evidence at all",
			never.Status, StatusNeverRan)
	}

	stopped := Evaluate(daily(), Evidence{EverRan: true, Newest: base.AddDate(0, 0, -22)},
		base, DefaultStaleMultiple)
	if stopped.Status != StatusStale {
		t.Errorf("status = %q, want %q for an automation 22 days past a daily cadence",
			stopped.Status, StatusStale)
	}

	if never.Status == stopped.Status {
		t.Error("never-ran and stopped produced the same status — the operator cannot " +
			"tell an invalid schedule from a dead process")
	}
}

// TestEvaluate_FreshInsideTheThreshold is the control: an automation doing its
// job must not be reported, or the arm is muted before it catches a real stop.
func TestEvaluate_FreshInsideTheThreshold(t *testing.T) {
	// Two days into a daily cadence with a 3x multiple: late, but not alarming.
	v := Evaluate(daily(), Evidence{EverRan: true, Newest: base.AddDate(0, 0, -2)},
		base, DefaultStaleMultiple)
	if v.Status != StatusFresh {
		t.Errorf("status = %q, want %q — a single skipped run must not alarm", v.Status, StatusFresh)
	}
	if v.Stale() {
		t.Error("a fresh verdict reported itself as stale")
	}
}

// TestEvaluate_ThresholdBoundary pins where the line actually is, in both
// directions. Without the second half, a fix that reported everything stale
// would pass the first half.
func TestEvaluate_ThresholdBoundary(t *testing.T) {
	// Just inside 3 x 24h.
	inside := Evaluate(daily(), Evidence{EverRan: true, Newest: base.Add(-71 * time.Hour)},
		base, DefaultStaleMultiple)
	if inside.Status != StatusFresh {
		t.Errorf("71h into a 3x24h threshold = %q, want fresh", inside.Status)
	}
	// Just outside.
	outside := Evaluate(daily(), Evidence{EverRan: true, Newest: base.Add(-73 * time.Hour)},
		base, DefaultStaleMultiple)
	if outside.Status != StatusStale {
		t.Errorf("73h into a 3x24h threshold = %q, want stale", outside.Status)
	}
}

// TestEvaluate_ProbeErrorIsUnknownNotHealthy guards the fail-closed direction.
// An unreachable API is not evidence that a cron fired.
func TestEvaluate_ProbeErrorIsUnknownNotHealthy(t *testing.T) {
	v := Evaluate(daily(), Evidence{Err: errors.New("api down")}, base, DefaultStaleMultiple)
	if v.Status != StatusUnknown {
		t.Errorf("status = %q, want %q on a probe error", v.Status, StatusUnknown)
	}
	if !v.Stale() {
		t.Error("an unverifiable automation reported itself as not warranting a finding — " +
			"'I could not look' must never render as 'it is fine'")
	}
}

// TestEvaluate_ZeroMultipleFallsBackToTheDefault guards a caller that forgets to
// configure the threshold: it must get the conservative default, not a
// threshold of zero that reports everything stale.
func TestEvaluate_ZeroMultipleFallsBackToTheDefault(t *testing.T) {
	v := Evaluate(daily(), Evidence{EverRan: true, Newest: base.Add(-2 * time.Hour)}, base, 0)
	if v.Status != StatusFresh {
		t.Errorf("status = %q with multiple=0, want fresh via the default — a zero "+
			"threshold would report every automation stale", v.Status)
	}
}

// TestRegistry_IsWellFormed guards the registry's own invariants. An entry with
// no interval divides the threshold to zero; a duplicate id makes findings
// ambiguous; a missing remedy leaves the operator with a problem and no action.
func TestRegistry_IsWellFormed(t *testing.T) {
	reg := Registry()
	if len(reg) == 0 {
		t.Fatal("the registry is empty — the arm becomes a no-op that always reports healthy")
	}
	seen := map[string]bool{}
	for _, a := range reg {
		if a.ID == "" {
			t.Error("automation with an empty id — findings key on it")
		}
		if seen[a.ID] {
			t.Errorf("duplicate automation id %q", a.ID)
		}
		seen[a.ID] = true
		if a.Interval <= 0 {
			t.Errorf("%s has interval %v — a non-positive interval makes the staleness "+
				"threshold zero", a.ID, a.Interval)
		}
		if a.Description == "" {
			t.Errorf("%s has no description", a.ID)
		}
		if a.Remedy == "" {
			t.Errorf("%s has no remedy — a finding with no next action is noise", a.ID)
		}
		if a.Kind == EvidenceWorkflowRun && a.Workflow == "" {
			t.Errorf("%s is workflow-backed but names no workflow file", a.ID)
		}
	}
}

// TestRegistry_CoversThisRepositorysOwnAutomations pins the built-ins.
//
// Only THIS repository's automations are built in. A workspace's sibling repos
// register through config — the shipped product must not hardcode one
// workspace's repo slugs, and the public-core boundary independently forbids
// naming a private companion repo here.
func TestRegistry_CoversThisRepositorysOwnAutomations(t *testing.T) {
	for _, id := range []string{"autonomous-loop", "release-workflow"} {
		if _, ok := ByID(id); !ok {
			t.Errorf("%q is not registered — it was one of the automations found dark, "+
				"and an unregistered automation is one nobody will notice stopping", id)
		}
	}
	for _, a := range Registry() {
		if a.Repo != "" {
			t.Errorf("built-in automation %q names repo %q — built-ins must be repo-agnostic; "+
				"sibling repos belong in config", a.ID, a.Repo)
		}
	}
}

// TestMerge_AddsDeclaredAutomations covers the path a workspace actually uses
// to register a sibling repo's cron.
func TestMerge_AddsDeclaredAutomations(t *testing.T) {
	got, errs := Merge([]ConfigAutomation{{
		ID: "sibling-smoke", Interval: "24h", Repo: "acme/widget",
		Workflow: "smoke.yml", TriggerEvent: "schedule",
	}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	var found *Automation
	for i := range got {
		if got[i].ID == "sibling-smoke" {
			found = &got[i]
		}
	}
	if found == nil {
		t.Fatal("declared automation was not merged in")
	}
	if found.Interval != 24*time.Hour {
		t.Errorf("interval = %v, want 24h", found.Interval)
	}
	if found.Kind != EvidenceWorkflowRun {
		t.Errorf("kind = %q, want %q", found.Kind, EvidenceWorkflowRun)
	}
	if found.Remedy == "" {
		t.Error("a declared entry with no remedy should get a default, not an empty one")
	}
	if len(got) != len(Registry())+1 {
		t.Errorf("merged %d entries, want built-ins + 1", len(got))
	}
}

// TestMerge_AcceptsDayIntervals — a weekly cron is the common case and nobody
// writes it as 168h.
func TestMerge_AcceptsDayIntervals(t *testing.T) {
	got, errs := Merge([]ConfigAutomation{{
		ID: "weekly", Interval: "7d", Workflow: "w.yml",
	}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	for _, a := range got {
		if a.ID == "weekly" && a.Interval != 7*24*time.Hour {
			t.Errorf("interval = %v, want 168h", a.Interval)
		}
	}
}

// TestMerge_RejectsMalformedEntriesLoudly is the guard that matters most here.
//
// A silently-dropped entry is an automation the operator BELIEVES is watched
// and is not — this package's own failure, reproduced one level up.
func TestMerge_RejectsMalformedEntriesLoudly(t *testing.T) {
	cases := []struct {
		name  string
		entry ConfigAutomation
	}{
		{"no id", ConfigAutomation{Interval: "24h", Workflow: "w.yml"}},
		{"no workflow", ConfigAutomation{ID: "x", Interval: "24h"}},
		{"unparseable interval", ConfigAutomation{ID: "x", Interval: "soon", Workflow: "w.yml"}},
		{"zero interval", ConfigAutomation{ID: "x", Interval: "0h", Workflow: "w.yml"}},
		{"negative interval", ConfigAutomation{ID: "x", Interval: "-24h", Workflow: "w.yml"}},
	}
	for _, c := range cases {
		got, errs := Merge([]ConfigAutomation{c.entry})
		if len(errs) == 0 {
			t.Errorf("%s: accepted silently — the operator would believe it is watched", c.name)
		}
		if len(got) != len(Registry()) {
			t.Errorf("%s: a rejected entry still entered the registry", c.name)
		}
	}
}

// TestMerge_DeclaredEntryOverridesABuiltIn lets a workspace correct an interval
// without forking the registry.
func TestMerge_DeclaredEntryOverridesABuiltIn(t *testing.T) {
	got, errs := Merge([]ConfigAutomation{{
		ID: "release-workflow", Interval: "30d", Workflow: "release.yml",
	}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(got) != len(Registry()) {
		t.Errorf("override added an entry instead of replacing one: %d vs %d",
			len(got), len(Registry()))
	}
	for _, a := range got {
		if a.ID == "release-workflow" && a.Interval != 30*24*time.Hour {
			t.Errorf("override did not apply: interval = %v", a.Interval)
		}
	}
}

// TestRegistry_CronBackedAutomationsRequireAScheduleEvent is the guard for the
// blindness that nearly shipped.
//
// The first version of the workflow probe counted ANY run as evidence. Against
// reality that reported `org-security-audit` healthy — because it has plenty of
// runs, all of them `workflow_dispatch` or `pull_request`, while its weekly
// cron has never fired once. A check that a hand-dispatch can satisfy is
// structurally blind to the failure it exists to catch.
func TestRegistry_CronBackedAutomationsRequireAScheduleEvent(t *testing.T) {
	// A config-declared cron must be able to demand a schedule event, or a
	// workspace cannot express the distinction at all.
	got, errs := Merge([]ConfigAutomation{{
		ID: "weekly-audit", Interval: "7d", Workflow: "audit.yml", TriggerEvent: "schedule",
	}})
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	var declared *Automation
	for i := range got {
		if got[i].ID == "weekly-audit" {
			declared = &got[i]
		}
	}
	if declared == nil || declared.TriggerEvent != "schedule" {
		t.Error("a declared trigger_event did not survive the merge — a workspace could " +
			"not distinguish a firing cron from a manual dispatch")
	}

	// The tag-triggered built-in must NOT demand a schedule event, or it would
	// be permanently unsatisfiable.
	rel, ok := ByID("release-workflow")
	if !ok {
		t.Fatal("release-workflow is not registered")
	}
	if rel.TriggerEvent == "schedule" {
		t.Error("release-workflow is tag-triggered; demanding a schedule event would make " +
			"it permanently and misleadingly 'never ran'")
	}
}

// TestRoundAge_ReadsAsAnOperatorWouldWriteIt guards the message, which is the
// only part of this an operator ever sees. "536h0m0s" is technically the age of
// a 22-day stall and tells nobody anything.
func TestRoundAge_ReadsAsAnOperatorWouldWriteIt(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{22 * 24 * time.Hour, "22d"},
		{5 * time.Hour, "5h"},
		{90 * time.Second, "1m"},
		{3 * time.Second, "3s"},
	}
	for _, c := range cases {
		if got := roundAge(c.in); got != c.want {
			t.Errorf("roundAge(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
