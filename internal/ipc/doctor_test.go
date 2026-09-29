package ipc

// Tests for the doctor.* IPC methods (internal/ipc/doctor.go). They drive the
// real method table over a fake check registry and fake verbs, so every
// assertion is about what the daemon does with the engine's answers: which
// verb ran, which notifications went out, what the caller was told.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// lockedBuffer is the daemon's notification writer under test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// progress returns every doctor.progress event written so far, in order.
func (b *lockedBuffer) progress(t *testing.T) []doctor.CheckProgress {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []doctor.CheckProgress
	for _, line := range strings.Split(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var ev struct {
			Event string               `json:"event"`
			Data  doctor.CheckProgress `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("notification is not JSON: %q: %v", line, err)
		}
		if ev.Event == doctorProgressEvent {
			out = append(out, ev.Data)
		}
	}
	return out
}

// doctorWorld is the state the fake checks report on and the fake verbs change.
type doctorWorld struct {
	mu           sync.Mutex
	betaPresent  bool
	gammaPresent bool
	applies      map[string]int
	adapters     []string
}

func (w *doctorWorld) present(which string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if which == "beta" {
		return w.betaPresent
	}
	return w.gammaPresent
}

func (w *doctorWorld) set(which string, v bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if which == "beta" {
		w.betaPresent = v
	} else {
		w.gammaPresent = v
	}
}

func (w *doctorWorld) applied(verb string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.applies[verb]
}

var (
	betaFP  = doctor.Fingerprint("NGD902", "beta-object")
	gammaFP = doctor.Fingerprint("NGD903", "gamma-object")
)

// doctorTestRegistry registers, in order: alpha (passes, but finishes last
// among the first three), beta (a blocker with an auto remedy), gamma
// (housekeeping with a confirm remedy), delta (depends on beta, so skipped)
// and slow (overruns its deadline).
func doctorTestRegistry(w *doctorWorld) *doctor.Registry {
	reg := doctor.NewRegistry()
	reg.MustRegister(doctor.Check{ID: "alpha", Title: "Alpha", Code: "NGD901", Run: func(ctx context.Context, _ *doctor.Env) []doctor.Finding {
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
		}
		return nil
	}})
	reg.MustRegister(doctor.Check{ID: "beta", Title: "Beta", Code: "NGD902", Run: func(context.Context, *doctor.Env) []doctor.Finding {
		if !w.present("beta") {
			return nil
		}
		return []doctor.Finding{{
			Code: "NGD902", Check: "beta", Severity: doctor.SeverityBlocker, Title: "beta is broken",
			Evidence: map[string]string{"object": "beta-object"}, Fingerprint: betaFP,
			Remedies: []doctor.Remedy{{ID: "fix", Kind: doctor.RemedyAuto, Summary: "Fix beta", Preview: "fix beta", Verb: "test.fix", Verify: "beta"}},
		}}
	}})
	reg.MustRegister(doctor.Check{ID: "gamma", Title: "Gamma", Code: "NGD903", Run: func(context.Context, *doctor.Env) []doctor.Finding {
		if !w.present("gamma") {
			return nil
		}
		return []doctor.Finding{{
			Code: "NGD903", Check: "gamma", Severity: doctor.SeverityHousekeeping, Title: "gamma is untidy",
			Evidence: map[string]string{"object": "gamma-object"}, Fingerprint: gammaFP,
			Remedies: []doctor.Remedy{{ID: "delete", Kind: doctor.RemedyConfirm, Summary: "Delete gamma", Preview: "delete gamma", Verb: "test.delete", Verify: "gamma"}},
		}}
	}})
	reg.MustRegister(doctor.Check{ID: "delta", Title: "Delta", Code: "NGD904", DependsOn: []string{"beta"}, Run: func(context.Context, *doctor.Env) []doctor.Finding {
		return nil
	}})
	reg.MustRegister(doctor.Check{ID: "slow", Title: "Slow", Code: "NGD905", Timeout: 20 * time.Millisecond, Run: func(ctx context.Context, _ *doctor.Env) []doctor.Finding {
		<-ctx.Done()
		return nil
	}})
	return reg
}

func doctorTestVerbs(w *doctorWorld) *doctor.VerbRegistry {
	vr := doctor.NewVerbRegistry()
	verb := func(name, which string) doctor.RemedyVerb {
		return doctor.VerbFuncs{
			PreviewFunc: func(context.Context, doctor.Finding) (string, error) { return "would " + name, nil },
			PreconditionFunc: func(context.Context, doctor.Finding) error {
				if !w.present(which) {
					return errors.New("already gone")
				}
				return nil
			},
			ApplyFunc: func(context.Context, doctor.Finding) error {
				w.mu.Lock()
				w.applies[name]++
				w.mu.Unlock()
				w.set(which, false)
				return nil
			},
		}
	}
	if err := vr.Register("test.fix", verb("test.fix", "beta")); err != nil {
		panic(err)
	}
	if err := vr.Register("test.delete", verb("test.delete", "gamma")); err != nil {
		panic(err)
	}
	return vr
}

func newDoctorTestServer(t *testing.T) (*Server, *doctorWorld, *lockedBuffer) {
	t.Helper()
	w := &doctorWorld{betaPresent: true, gammaPresent: true, applies: map[string]int{}}
	out := &lockedBuffer{}
	s := &Server{
		writer:         out,
		workspaceRoot:  t.TempDir(),
		activeRuntimes: make(map[string]*runEntry),
		methods:        make(map[string]Handler),
	}
	s.registerMethods()
	s.newDoctorFixer = func(root string, adapters []string) (*doctor.Fixer, error) {
		w.mu.Lock()
		w.adapters = adapters
		w.mu.Unlock()
		return &doctor.Fixer{
			Registry: doctorTestRegistry(w),
			Verbs:    doctorTestVerbs(w),
			Env:      &doctor.Env{Cwd: root, Now: time.Now(), Adapters: adapters},
		}, nil
	}
	return s, w, out
}

// callDoctor sends params through the registered method table, exactly as a
// socket caller would, and returns the result as JSON.
func callDoctor(t *testing.T, s *Server, method string, params interface{}) ([]byte, error) {
	t.Helper()
	h, ok := s.methods[method]
	if !ok {
		t.Fatalf("%s is not registered", method)
	}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	res, err := h(context.Background(), raw)
	if err != nil {
		return nil, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("%s result does not encode: %v", method, err)
	}
	return out, nil
}

func mustCallDoctor(t *testing.T, s *Server, method string, params interface{}, into interface{}) {
	t.Helper()
	out, err := callDoctor(t, s, method, params)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	if err := json.Unmarshal(out, into); err != nil {
		t.Fatalf("%s: decode %s: %v", method, out, err)
	}
}

func TestDoctorIPCRunReturnsJSONv2(t *testing.T) {
	s, _, _ := newDoctorTestServer(t)

	var res map[string]json.RawMessage
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &res)

	for _, key := range []string{"v", "findings", "summary", "healthy", "exit_code", "failed_checks", "errors", "warnings", "install_instructions"} {
		if _, ok := res[key]; !ok {
			t.Errorf("JSON v2 key %q missing from doctor.run result", key)
		}
	}
	var v2 doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &v2)
	if v2.V != 2 {
		t.Fatalf("v = %d, want 2", v2.V)
	}
	want := doctor.Summary{Blocker: 1, Warning: 1, Housekeeping: 1, Info: 1}
	if v2.Summary != want {
		t.Errorf("summary = %+v, want %+v (beta blocker, slow NGD000, gamma, delta skipped)", v2.Summary, want)
	}
	if v2.ExitCode != 2 || v2.Healthy {
		t.Errorf("exit_code=%d healthy=%v, want 2 and false", v2.ExitCode, v2.Healthy)
	}
	if len(v2.FailedChecks) != 1 || v2.FailedChecks[0] != "beta" {
		t.Errorf("failed_checks = %v, want [beta]", v2.FailedChecks)
	}
}

func TestDoctorIPCRunFiltersFindingsAndRederivesFields(t *testing.T) {
	s, w, _ := newDoctorTestServer(t)

	var hk doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{Severity: []string{"housekeeping"}}, &hk)
	if len(hk.Findings) != 1 || hk.Findings[0].Fingerprint != gammaFP {
		t.Fatalf("severity=housekeeping returned %+v, want only gamma", hk.Findings)
	}
	if hk.ExitCode != 0 || len(hk.FailedChecks) != 0 {
		t.Errorf("derived fields not recomputed from the selection: exit=%d failed=%v", hk.ExitCode, hk.FailedChecks)
	}

	var only doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{Only: []string{"NGD902"}, Adapters: []string{"all"}}, &only)
	if len(only.Findings) != 1 || only.Findings[0].Check != "beta" {
		t.Fatalf("only=NGD902 returned %+v, want only beta", only.Findings)
	}
	w.mu.Lock()
	got := strings.Join(w.adapters, ",")
	w.mu.Unlock()
	if got != strings.Join(doctor.AllAdapterNames(), ",") {
		t.Errorf("adapters=all reached the engine as %q", got)
	}
}

func TestDoctorIPCRunStreamsProgressInCheckOrder(t *testing.T) {
	s, _, out := newDoctorTestServer(t)

	var res doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &res)

	var got []string
	for i, ev := range out.progress(t) {
		got = append(got, ev.Check+":"+string(ev.Phase))
		if ev.Total != 5 {
			t.Errorf("event %d total = %d, want 5", i, ev.Total)
		}
	}
	// alpha finishes after beta and gamma, and delta never starts: the stream
	// is still in registration order, each check's start before its outcome.
	want := []string{
		"alpha:started", "alpha:finished",
		"beta:started", "beta:finished",
		"gamma:started", "gamma:finished",
		"delta:skipped",
		"slow:started", "slow:timeout",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("progress = %v\nwant       %v", got, want)
	}
}

func TestDoctorIPCApplyRemedyStaleFingerprintAppliesNothing(t *testing.T) {
	s, w, _ := newDoctorTestServer(t)
	var run doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &run)

	// Resolved out of band after the scan: the re-scan no longer reports it.
	w.set("beta", false)
	var res DoctorApplyRemedyResult
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: betaFP, RemedyID: "fix"}, &res)
	if res.Outcome != doctor.OutcomeStale {
		t.Fatalf("outcome = %q, want stale: %+v", res.Outcome, res)
	}
	if res.Finding != nil {
		t.Errorf("a stale result carries no finding, got %+v", res.Finding)
	}

	// A well-formed fingerprint the current scan never produced.
	unknown := doctor.Fingerprint("NGD902", "some-other-object")
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: unknown, RemedyID: "fix", Confirm: true}, &res)
	if res.Outcome != doctor.OutcomeStale {
		t.Fatalf("unknown fingerprint: outcome = %q, want stale", res.Outcome)
	}
	if n := w.applied("test.fix"); n != 0 {
		t.Fatalf("Apply ran %d times for stale fingerprints, want 0", n)
	}
}

func TestDoctorIPCConfirmRemedyNeedsConfirm(t *testing.T) {
	s, w, _ := newDoctorTestServer(t)
	var run doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &run)

	var res DoctorApplyRemedyResult
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: gammaFP, RemedyID: "delete"}, &res)
	if res.Action != doctor.ActionAwaitingConsent || res.Outcome != doctor.OutcomeSkipped {
		t.Fatalf("confirm remedy without confirm: action=%q outcome=%q, want awaiting-consent/skipped", res.Action, res.Outcome)
	}
	if w.applied("test.delete") != 0 || !w.present("gamma") {
		t.Fatal("a confirm remedy ran without confirm: true")
	}

	// A dry run previews through the verb and changes nothing, even with consent.
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: gammaFP, RemedyID: "delete", Confirm: true, DryRun: true}, &res)
	if res.Action != doctor.ActionPreviewed || res.Preview != "would test.delete" || res.Outcome != "" {
		t.Fatalf("dry run = %+v, want previewed with the verb's preview", res)
	}
	if w.applied("test.delete") != 0 {
		t.Fatal("dry run applied the remedy")
	}

	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: gammaFP, RemedyID: "delete", Confirm: true}, &res)
	if res.Outcome != doctor.OutcomeFixed || w.applied("test.delete") != 1 {
		t.Fatalf("confirmed remedy: outcome=%q applies=%d, want fixed and 1", res.Outcome, w.applied("test.delete"))
	}
	if res.Finding == nil || res.Finding.Fingerprint != gammaFP {
		t.Errorf("result finding = %+v, want gamma's", res.Finding)
	}
}

func TestDoctorIPCAutoRemedyVerifiedAndRecheck(t *testing.T) {
	s, _, _ := newDoctorTestServer(t)
	var run doctor.DoctorResult
	mustCallDoctor(t, s, "doctor.run", DoctorRunParams{}, &run)

	var before DoctorRecheckResult
	mustCallDoctor(t, s, "doctor.recheck", DoctorRecheckParams{Check: "beta"}, &before)
	if before.Status != doctor.StatusFailed || len(before.Findings) != 1 {
		t.Fatalf("recheck before fix = %+v", before)
	}

	var res DoctorApplyRemedyResult
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: betaFP, RemedyID: "fix"}, &res)
	if res.Outcome != doctor.OutcomeFixed {
		t.Fatalf("auto remedy outcome = %q (%s), want fixed", res.Outcome, res.Detail)
	}

	var after DoctorRecheckResult
	mustCallDoctor(t, s, "doctor.recheck", DoctorRecheckParams{Code: "ngd902"}, &after)
	if after.Check != "beta" || after.Status != doctor.StatusPassed || len(after.Findings) != 0 {
		t.Fatalf("recheck by code after fix = %+v, want beta passed", after)
	}

	// The fingerprint left the current scan with the fix, so it is stale now.
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: betaFP, RemedyID: "fix"}, &res)
	if res.Outcome != doctor.OutcomeStale {
		t.Fatalf("second apply outcome = %q, want stale", res.Outcome)
	}
}

func TestDoctorIPCScansWhenNoCurrentScan(t *testing.T) {
	s, w, out := newDoctorTestServer(t)

	var res DoctorApplyRemedyResult
	mustCallDoctor(t, s, "doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: gammaFP, RemedyID: "delete"}, &res)
	if res.Action != doctor.ActionAwaitingConsent {
		t.Fatalf("apply before any doctor.run = %+v, want awaiting-consent against a fresh scan", res)
	}
	if len(out.progress(t)) == 0 {
		t.Error("the establishing scan streamed no progress")
	}
	if w.applied("test.delete") != 0 {
		t.Fatal("applied without consent")
	}
}

func TestDoctorIPCRejectsMalformedParams(t *testing.T) {
	s, w, _ := newDoctorTestServer(t)
	cases := []struct {
		method string
		params interface{}
	}{
		{"doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: "../../etc/passwd", RemedyID: "fix"}},
		{"doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: betaFP, RemedyID: "fix; rm -rf /"}},
		{"doctor.applyRemedy", DoctorApplyRemedyParams{Fingerprint: betaFP}},
		{"doctor.recheck", DoctorRecheckParams{}},
		{"doctor.recheck", DoctorRecheckParams{Code: "NGD902", Check: "beta"}},
		{"doctor.recheck", DoctorRecheckParams{Check: "no_such_check"}},
		{"doctor.recheck", DoctorRecheckParams{Code: "NGD999"}},
		{"doctor.run", DoctorRunParams{Severity: []string{"critical"}}},
		{"doctor.run", DoctorRunParams{Only: []string{"../x"}}},
		{"doctor.run", DoctorRunParams{Adapters: []string{"claude --dangerous"}}},
		{"doctor.history", DoctorHistoryParams{Limit: -1}},
	}
	for _, c := range cases {
		if _, err := callDoctor(t, s, c.method, c.params); err == nil {
			t.Errorf("%s %+v: want an error", c.method, c.params)
		}
	}
	if w.applied("test.fix")+w.applied("test.delete") != 0 {
		t.Fatal("a malformed request applied a remedy")
	}
}

func TestDoctorIPCHistory(t *testing.T) {
	s, _, _ := newDoctorTestServer(t)
	path := filepath.Join(t.TempDir(), "doctor", "fix-log.jsonl")
	s.doctorFixLogPath = func() (string, error) { return path, nil }

	var empty DoctorHistoryResult
	mustCallDoctor(t, s, "doctor.history", DoctorHistoryParams{}, &empty)
	if empty.Entries == nil || len(empty.Entries) != 0 {
		t.Fatalf("missing log = %+v, want an empty list", empty)
	}

	log := &doctor.FixLog{Path: path}
	for _, fp := range []string{"a", "b", "c"} {
		if err := log.Append(doctor.FixLogEntry{Time: time.Now().UTC(), Code: "NGD017", Check: "worktree_leaks", Fingerprint: fp, Remedy: "remove", Outcome: doctor.OutcomeFixed}); err != nil {
			t.Fatal(err)
		}
	}
	var res DoctorHistoryResult
	mustCallDoctor(t, s, "doctor.history", DoctorHistoryParams{Limit: 2}, &res)
	if len(res.Entries) != 2 || res.Entries[0].Fingerprint != "b" || res.Entries[1].Fingerprint != "c" {
		t.Fatalf("limit=2 = %+v, want the newest two, oldest first", res.Entries)
	}
}
