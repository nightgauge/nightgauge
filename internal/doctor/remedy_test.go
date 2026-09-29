package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// #2093. The remedy engine's contract: the owning check, re-run after apply,
// decides the outcome — never Apply's own return value.

const fakeCheck = "fake_hygiene"

// fakeWorld is a toy condition a fake check reports and a fake verb repairs.
type fakeWorld struct {
	present  atomic.Bool // the condition the check detects
	repairs  atomic.Bool // whether Apply actually repairs it
	changed  atomic.Bool // set after the scan: the precondition's claim no longer holds
	applied  atomic.Int32
	evidence string // extra evidence value, to seed the redactor
}

func fakeFinding(kind RemedyKind, verb string, ev map[string]string) Finding {
	return newFinding(fakeCheck, "NGD017", SeverityHousekeeping, "fake leak", "a fake leak for the engine test",
		ev, []string{"object-1"},
		Remedy{ID: "repair", Kind: kind, Verb: verb, Verify: fakeCheck,
			Summary: "Repair the fake leak", Preview: "repair object-1"})
}

// fakeFixer builds a Fixer over one fake check whose remedy has kind and verb,
// with verbs registered by register.
func fakeFixer(t *testing.T, w *fakeWorld, kind RemedyKind, verb string, register func(*VerbRegistry)) *Fixer {
	t.Helper()
	reg := NewRegistry()
	reg.MustRegister(Check{ID: fakeCheck, Title: "Fake", Group: "hygiene", Code: "NGD017",
		Run: func(ctx context.Context, env *Env) []Finding {
			if !w.present.Load() {
				return nil
			}
			ev := map[string]string{"object": "object-1"}
			if w.evidence != "" {
				ev["note"] = w.evidence
			}
			return []Finding{fakeFinding(kind, verb, ev)}
		}})
	verbs := NewVerbRegistry()
	if register != nil {
		register(verbs)
	}
	return &Fixer{
		Registry: reg, Verbs: verbs,
		Env: &Env{Cwd: t.TempDir(), Now: time.Now()},
		Log: &FixLog{Path: filepath.Join(t.TempDir(), "doctor", "fix-log.jsonl")},
	}
}

func fakeVerb(w *fakeWorld) VerbFuncs {
	return VerbFuncs{
		PreviewFunc: declaredPreview("fake.repair"),
		PreconditionFunc: func(ctx context.Context, f Finding) error {
			if w.changed.Load() {
				return errors.New("object-1 changed since the scan")
			}
			return nil
		},
		ApplyFunc: func(ctx context.Context, f Finding) error {
			w.applied.Add(1)
			if w.repairs.Load() {
				w.present.Store(false)
			}
			return nil
		},
	}
}

func registerFake(w *fakeWorld) func(*VerbRegistry) {
	return func(r *VerbRegistry) {
		if err := r.Register("fake.repair", fakeVerb(w)); err != nil {
			panic(err)
		}
	}
}

func onlyResult(t *testing.T, rep FixReport) FixResult {
	t.Helper()
	if len(rep.Results) != 1 {
		t.Fatalf("want one result, got %d: %+v", len(rep.Results), rep.Results)
	}
	return rep.Results[0]
}

func TestRemedyEngine_AutoRemedyIsAppliedAndVerifiedFixed(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))

	rep := fx.Run(context.Background(), FixOptions{})
	r := onlyResult(t, rep)
	if r.Action != ActionApplied || r.Outcome != OutcomeFixed {
		t.Fatalf("result = %s/%s (%s), want applied/fixed", r.Action, r.Outcome, r.Detail)
	}
	if w.applied.Load() != 1 {
		t.Errorf("Apply ran %d times, want 1", w.applied.Load())
	}
	if rep.ExitCode != 0 || rep.Counts.Fixed != 1 {
		t.Errorf("exit %d, counts %+v", rep.ExitCode, rep.Counts)
	}
	if len(rep.Doctor.Findings) != 0 {
		t.Errorf("post-fix state still carries %d finding(s)", len(rep.Doctor.Findings))
	}
}

// TestRemedyEngine_ApplySuccessIsNotTrusted: Apply returns nil but the check
// still finds the problem. An engine that trusted Apply would say fixed.
func TestRemedyEngine_ApplySuccessIsNotTrusted(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true) // repairs stays false: Apply "succeeds" and changes nothing
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))

	r := onlyResult(t, fx.Run(context.Background(), FixOptions{}))
	if r.Outcome != OutcomeStillPresent {
		t.Fatalf("outcome = %s, want still-present: Apply's nil error must not be reported as success", r.Outcome)
	}
	if r.Evidence["object"] != "object-1" {
		t.Errorf("still-present result carries no re-run evidence: %+v", r.Evidence)
	}
}

// TestRemedyEngine_PreconditionChangedAfterScanIsBlocked: the world changes
// between the scan and apply; the remedy is blocked and Apply never runs.
func TestRemedyEngine_PreconditionChangedAfterScanIsBlocked(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	// The scan runs, then the world changes before apply.
	fx.Scan(context.Background())
	w.changed.Store(true)
	r := fx.ApplyFingerprint(context.Background(), fakeCheck, fakeFinding(RemedyAuto, "", nil).Fingerprint, "repair", false)
	if r.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %s (%s), want blocked", r.Outcome, r.Detail)
	}
	if w.applied.Load() != 0 {
		t.Fatal("Apply ran although the precondition failed")
	}

	// Through a whole pass the exit code is 4.
	w2 := &fakeWorld{}
	w2.present.Store(true)
	fx2 := fakeFixer(t, w2, RemedyAuto, "fake.repair", registerFake(w2))
	fx2.Registry.checks[0].Run = func(ctx context.Context, env *Env) []Finding {
		defer w2.changed.Store(true) // the world moves right after the scan reads it
		return []Finding{fakeFinding(RemedyAuto, "fake.repair", map[string]string{"object": "object-1"})}
	}
	rep := fx2.Run(context.Background(), FixOptions{})
	if r := onlyResult(t, rep); r.Outcome != OutcomeBlocked || w2.applied.Load() != 0 {
		t.Fatalf("outcome = %s, applied %d; want blocked with no Apply", r.Outcome, w2.applied.Load())
	}
	if rep.ExitCode != 4 {
		t.Errorf("exit code = %d, want 4", rep.ExitCode)
	}
}

func TestRemedyEngine_UnregisteredVerbFailsClosed(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "no.such.verb", nil)

	rep := fx.Run(context.Background(), FixOptions{})
	r := onlyResult(t, rep)
	if r.Outcome != OutcomeBlocked || !strings.Contains(r.Detail, "not registered") {
		t.Fatalf("result = %s (%s), want blocked naming the unregistered verb", r.Outcome, r.Detail)
	}
	if rep.ExitCode != 4 {
		t.Errorf("exit code = %d, want 4", rep.ExitCode)
	}
	dry := onlyResult(t, fx.Run(context.Background(), FixOptions{DryRun: true}))
	if !strings.Contains(dry.Detail, "not registered") {
		t.Errorf("dry run does not report the unregistered verb: %q", dry.Detail)
	}
}

func TestRemedyEngine_DryRunChangesNothing(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))

	r := onlyResult(t, fx.Run(context.Background(), FixOptions{DryRun: true}))
	if r.Action != ActionPreviewed || r.Preview != "repair object-1" {
		t.Fatalf("result = %s preview %q, want previewed with the declared preview", r.Action, r.Preview)
	}
	if w.applied.Load() != 0 || !w.present.Load() {
		t.Fatal("--dry-run applied the remedy")
	}
	if entries, _, _ := ReadFixLog(fx.Log.Path); len(entries) != 0 {
		t.Errorf("--dry-run wrote %d fix-log entries", len(entries))
	}
}

func TestRemedyEngine_ConfirmNeedsConsent(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyConfirm, "fake.repair", registerFake(w))

	r := onlyResult(t, fx.Run(context.Background(), FixOptions{}))
	if r.Action != ActionAwaitingConsent || w.applied.Load() != 0 {
		t.Fatalf("confirm remedy without --yes: action %s, applied %d", r.Action, w.applied.Load())
	}
	asked := 0
	r = onlyResult(t, fx.Run(context.Background(), FixOptions{Consent: func(Finding, Remedy) bool { asked++; return false }}))
	if asked != 1 || r.Action != ActionAwaitingConsent {
		t.Fatalf("declined consent: asked %d, action %s", asked, r.Action)
	}
	r = onlyResult(t, fx.Run(context.Background(), FixOptions{Yes: true}))
	if r.Outcome != OutcomeFixed {
		t.Fatalf("confirm remedy with --yes: outcome %s (%s)", r.Outcome, r.Detail)
	}
}

func TestRemedyEngine_ManualRemedyIsListedWithSteps(t *testing.T) {
	reg := NewRegistry()
	reg.MustRegister(Check{ID: fakeCheck, Code: "NGD018", Run: func(ctx context.Context, env *Env) []Finding {
		return []Finding{newFinding(fakeCheck, "NGD018", SeverityHousekeeping, "unmerged", "unique commits", nil,
			[]string{"b"}, manualRemedy("review", "Land it", fakeCheck, "Not offered as a fix", "Inspect it"))}
	}})
	fx := &Fixer{Registry: reg, Verbs: NewVerbRegistry(), Env: &Env{Now: time.Now()}}
	r := onlyResult(t, fx.Run(context.Background(), FixOptions{Yes: true}))
	if r.Action != ActionManual || r.Remedy == nil || len(r.Remedy.Steps) != 2 {
		t.Fatalf("manual result = %+v", r)
	}
}

func TestRemedyEngine_FiltersByCodeCheckAndSeverity(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	for _, opts := range []FixOptions{
		{Only: []string{"NGD999"}},
		{Only: []string{"other_check"}},
		{Severities: []Severity{SeverityBlocker, SeverityWarning}},
	} {
		if rep := fx.Run(context.Background(), opts); len(rep.Results) != 0 || w.applied.Load() != 0 {
			t.Fatalf("filter %+v selected %d result(s)", opts, len(rep.Results))
		}
	}
	for _, opts := range []FixOptions{{Only: []string{"ngd017"}, DryRun: true}, {Only: []string{fakeCheck}, DryRun: true},
		{Severities: []Severity{SeverityHousekeeping}, DryRun: true}} {
		if rep := fx.Run(context.Background(), opts); len(rep.Results) != 1 {
			t.Fatalf("filter %+v selected %d result(s), want 1", opts, len(rep.Results))
		}
	}
	if _, err := ParseSeverity("urgent"); err == nil {
		t.Error("ParseSeverity accepted an unknown severity")
	}
}

func TestRemedyEngine_IdempotentOnACleanWorkspace(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	w.repairs.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	if rep := fx.Run(context.Background(), FixOptions{}); rep.Counts.Fixed != 1 {
		t.Fatalf("first pass fixed %d", rep.Counts.Fixed)
	}
	rep := fx.Run(context.Background(), FixOptions{})
	if len(rep.Results) != 0 || rep.ExitCode != 0 || w.applied.Load() != 1 {
		t.Fatalf("second pass: %d result(s), exit %d, Apply ran %d times in total", len(rep.Results), rep.ExitCode, w.applied.Load())
	}
}

func TestRemedyEngine_ConflictExitsThree(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.move", func(r *VerbRegistry) {
		_ = r.Register("fake.move", VerbFuncs{
			PreviewFunc:      declaredPreview("fake.move"),
			PreconditionFunc: func(context.Context, Finding) error { return nil },
			ApplyFunc: func(context.Context, Finding) error {
				return errors.Join(ErrRemedyConflict, errors.New("/a and /b both exist"))
			},
		})
	})
	rep := fx.Run(context.Background(), FixOptions{})
	if r := onlyResult(t, rep); r.Outcome != OutcomeConflict || rep.ExitCode != 3 {
		t.Fatalf("outcome %s exit %d, want conflict/3", r.Outcome, rep.ExitCode)
	}
}

func TestRemedyEngine_UnverifiableRecheckIsNotFixed(t *testing.T) {
	w := &fakeWorld{}
	w.present.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	fx.Registry.checks[0].Timeout = 50 * time.Millisecond
	run := fx.Registry.checks[0].Run
	fx.Registry.checks[0].Run = func(ctx context.Context, env *Env) []Finding {
		if w.applied.Load() > 0 { // the verification re-run hangs
			<-ctx.Done()
			return nil
		}
		return run(ctx, env)
	}
	r := onlyResult(t, fx.Run(context.Background(), FixOptions{}))
	if r.Outcome != OutcomeStillPresent || !strings.Contains(r.Detail, "cannot verify") {
		t.Fatalf("outcome %s (%s), want still-present: a re-run that did not complete verifies nothing", r.Outcome, r.Detail)
	}
}

func TestRemedyEngine_ApplyFingerprintStale(t *testing.T) {
	w := &fakeWorld{}
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", registerFake(w))
	r := fx.ApplyFingerprint(context.Background(), fakeCheck, "0123456789abcdef", "repair", true)
	if r.Outcome != OutcomeStale {
		t.Fatalf("outcome = %s, want stale for a fingerprint the check no longer reports", r.Outcome)
	}
}

// TestRemedyEngine_FixLogRecordsAndRedacts: every attempted remedy is logged
// with time, code, fingerprint, verb and outcome; no evidence value reaches
// the log, and a token-shaped detail is masked.
func TestRemedyEngine_FixLogRecordsAndRedacts(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C3d4", 5)
	w := &fakeWorld{evidence: token}
	w.present.Store(true)
	fx := fakeFixer(t, w, RemedyAuto, "fake.repair", func(r *VerbRegistry) {
		_ = r.Register("fake.repair", VerbFuncs{
			PreviewFunc:      declaredPreview("fake.repair"),
			PreconditionFunc: func(context.Context, Finding) error { return nil },
			ApplyFunc: func(context.Context, Finding) error {
				w.present.Store(false)
				return errors.New("warning: used " + token)
			},
		})
	})
	rep := fx.Run(context.Background(), FixOptions{})
	if r := onlyResult(t, rep); r.Outcome != OutcomeFixed || strings.Contains(r.Detail, token) {
		t.Fatalf("result = %s, detail %q", r.Outcome, r.Detail)
	}
	raw, err := os.ReadFile(fx.Log.Path)
	if err != nil {
		t.Fatalf("fix log not written: %v", err)
	}
	if strings.Contains(string(raw), token) {
		t.Fatalf("the fix log carries a raw token: %s", raw)
	}
	entries, malformed, err := ReadFixLog(fx.Log.Path)
	if err != nil || malformed != 0 || len(entries) != 1 {
		t.Fatalf("entries %d malformed %d err %v", len(entries), malformed, err)
	}
	e := entries[0]
	if e.Code != "NGD017" || e.Verb != "fake.repair" || e.Outcome != OutcomeFixed || e.Fingerprint == "" || e.Time.IsZero() {
		t.Errorf("entry = %+v", e)
	}
	if info, err := os.Stat(fx.Log.Path); err == nil && info.Mode().Perm() != 0o600 {
		t.Errorf("fix log mode %o, want 0600", info.Mode().Perm())
	}
	if info, err := os.Stat(filepath.Dir(fx.Log.Path)); err == nil && info.Mode().Perm() != 0o700 {
		t.Errorf("fix log dir mode %o, want 0700", info.Mode().Perm())
	}
}

func TestFixLog_RefusesASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.jsonl")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "doctor", "fix-log.jsonl")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := (&FixLog{Path: link}).Append(FixLogEntry{Code: "NGD017"}); err == nil {
		t.Fatal("the fix log wrote through a symlink")
	}
}

// TestRemedyEngine_BuiltinVerbsCoverTheHygieneRemedies: every hygiene remedy
// #2089 declares resolves to a registered verb.
func TestRemedyEngine_BuiltinVerbsCoverTheHygieneRemedies(t *testing.T) {
	reg := BuiltinVerbs(&Env{})
	for _, v := range []string{verbWorktreeSweep, verbStashSweep, verbWipPrune, verbBranchDelete,
		verbProcessTerminate, verbServeLeaseReclaim, verbComposeCleanup, verbOutcomeInit, verbSurvivalSweep} {
		if _, ok := reg.Lookup(v); !ok {
			t.Errorf("verb %s is declared by a check but not registered", v)
		}
	}
}
