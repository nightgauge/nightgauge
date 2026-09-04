package stages

import "testing"

// TestPRCreatePhases_CountAdvancesAndSkipsAreExplicit is AC3's pr-create half:
// the same defect, the same shape of fix.
func TestPRCreatePhases_CountAdvancesAndSkipsAreExplicit(t *testing.T) {
	prc := &fakePRClient{createdPR: &CreatedPR{Number: 99, URL: "https://example.invalid/99", NodeID: "PR_99"}}
	r := newTestRunner(richSnap(), prc, &fakeGit{})

	rec := &recordingReporter{}
	res, err := r.Run(reporterCtx(rec), 42, "owner/repo", t.TempDir())
	if err != nil || res.Path != CreatePathCreated {
		t.Fatalf("setup: Path=%q err=%v, want created", res.Path, err)
	}

	for _, want := range []string{"load-context", "preflight-checks", "create-pr", "verify-pr-created", "write-context"} {
		if !rec.has(want, "running") || !rec.has(want, "complete") {
			t.Errorf("phase %q did not run to completion; events = %+v", want, rec.events)
		}
	}

	skipped := map[string]bool{}
	for _, n := range rec.namesWithStatus("skipped") {
		skipped[n] = true
	}
	for _, name := range prCreatePhases.order {
		offPath := prCreatePhases.roles[name] == phaseOffPath
		if offPath && !skipped[name] {
			t.Errorf("LLM-only phase %q was not recorded skipped", name)
		}
		if !offPath && skipped[name] {
			t.Errorf("phase %q is on the deterministic path and must not be recorded skipped", name)
		}
	}
}

// TestPRCreatePhases_PuntRecordsNoSkips — same contract as pr-merge: a punt
// hands the stage to the skill, so the runner may not pre-empt the skill's
// record of phases the skill is about to run.
func TestPRCreatePhases_PuntRecordsNoSkips(t *testing.T) {
	snap := richSnap()
	snap.Branch = "" // DecideCreate punts: nothing to open a PR from.
	r := newTestRunner(snap, &fakePRClient{}, &fakeGit{})

	rec := &recordingReporter{}
	res, err := r.Run(reporterCtx(rec), 42, "owner/repo", t.TempDir())
	if err != nil || res.Path != CreatePathPunt {
		t.Fatalf("setup: Path=%q err=%v, want punt", res.Path, err)
	}
	if got := rec.namesWithStatus("skipped"); len(got) != 0 {
		t.Errorf("punt recorded %v as skipped — the LLM path is about to run those phases", got)
	}
	if !rec.has("load-context", "complete") {
		t.Error("load-context completed before the punt and must stay recorded complete")
	}
	if !rec.has("preflight-checks", "failed") {
		t.Errorf("the in-flight phase was left open on the punt; events = %+v", rec.events)
	}
}
