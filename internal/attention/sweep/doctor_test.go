package sweep

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/attention"
	"github.com/nightgauge/nightgauge/internal/doctor"
)

const doctorTestRepo = "acme/web"

func doctorFinding(check, code string, sev doctor.Severity, remedies ...doctor.Remedy) doctor.Finding {
	if remedies == nil {
		remedies = []doctor.Remedy{}
	}
	return doctor.Finding{
		Code:        code,
		Check:       check,
		Severity:    sev,
		Title:       check + " is unhealthy",
		Cause:       "the " + check + " check found a problem",
		Evidence:    map[string]string{"object": check},
		Docs:        doctor.DocsAnchor(code),
		Fingerprint: doctor.Fingerprint(code, check),
		Remedies:    remedies,
	}
}

// The fixture: one blocker with an auto remedy, one warning with a manual
// remedy, one housekeeping and one info finding.
var (
	fxBlocker = doctorFinding("serve_lease", "NGD022", doctor.SeverityBlocker, doctor.Remedy{
		ID: "release", Kind: doctor.RemedyAuto, Summary: "Release the stale lease",
		Preview: "remove the lease file", Verb: "lease.release", Verify: "serve_lease",
	})
	fxWarning = doctorFinding("github_api_budget", "NGD008", doctor.SeverityWarning, doctor.Remedy{
		ID: "investigate", Kind: doctor.RemedyManual, Summary: "Find the caller that spent the budget",
		Steps: []string{"Run `nightgauge api-usage --by op`"},
		Links: []string{"https://docs.github.com/en/graphql", "https://evil.example/phish"},
	})
	fxHousekeeping = doctorFinding("worktree_leaks", "NGD017", doctor.SeverityHousekeeping)
	fxInfo         = doctorFinding("survival_backlog", "NGD026", doctor.SeverityInfo)
)

func doctorResults(findings ...doctor.Finding) []doctor.CheckResult {
	out := make([]doctor.CheckResult, 0, len(findings))
	for _, f := range findings {
		out = append(out, doctor.CheckResult{ID: f.Check, Status: doctor.StatusFailed, Findings: []doctor.Finding{f}})
	}
	return out
}

// scriptedDoctor answers each sweep from the next scripted scan.
type scriptedDoctor struct {
	scans []func() ([]doctor.CheckResult, error)
	n     int
}

func (s *scriptedDoctor) scan(context.Context, string) ([]doctor.CheckResult, error) {
	f := s.scans[s.n]
	s.n++
	return f()
}

func doctorSweeper(t *testing.T, scans ...func() ([]doctor.CheckResult, error)) (*Sweeper, *attention.Store) {
	t.Helper()
	script := &scriptedDoctor{scans: scans}
	// No reuse: each sweep here scripts its own scan.
	p := &Doctor{Scan: script.scan, PrimaryRepo: func(string) string { return "" }, cache: newDoctorScanCache(0)}
	return coverageSweeper(t, p)
}

func openDoctorCards(t *testing.T, store *attention.Store) map[string]attention.DecisionRequest {
	t.Helper()
	all, err := store.List(attention.ListFilter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := map[string]attention.DecisionRequest{}
	for _, r := range all {
		if r.Producer == ProducerDoctor && !r.Lifecycle.State.IsTerminal() {
			out[r.IdempotencyKey] = r
		}
	}
	return out
}

func sweepDoctor(t *testing.T, sw *Sweeper) WorkspaceResult {
	t.Helper()
	res, err := sw.SweepWorkspace(context.Background(), []string{doctorTestRepo})
	if err != nil {
		t.Fatalf("SweepWorkspace: %v", err)
	}
	return res
}

func TestDoctorProducer_RaisesOneCardPerBlockerOrWarning(t *testing.T) {
	sw, store := doctorSweeper(t, func() ([]doctor.CheckResult, error) {
		return doctorResults(fxBlocker, fxWarning, fxHousekeeping, fxInfo), nil
	})
	if res := sweepDoctor(t, sw); res.Created != 2 {
		t.Fatalf("created = %d, want 2 (%+v)", res.Created, res)
	}
	cards := openDoctorCards(t, store)
	if len(cards) != 2 {
		t.Fatalf("want 2 open doctor cards, got %d: %+v", len(cards), cards)
	}

	blocker, ok := cards[attention.DoctorCardKey(fxBlocker.Check, fxBlocker.Fingerprint)]
	if !ok {
		t.Fatalf("no card keyed by the blocker's fingerprint; have %v", cards)
	}
	if blocker.Severity != attention.SeverityBlockingFleet || blocker.Context.Repo != doctorTestRepo {
		t.Errorf("blocker card severity=%q repo=%q", blocker.Severity, blocker.Context.Repo)
	}
	if len(blocker.Options) != 2 {
		t.Fatalf("blocker options = %+v, want apply-release and recheck", blocker.Options)
	}
	apply := blocker.Options[0]
	if apply.Verb != attention.VerbDoctorApplyRemedy || apply.ID != "apply-release" ||
		len(apply.Args) != 2 || apply.Args["fingerprint"] != fxBlocker.Fingerprint || apply.Args["remedyId"] != "release" {
		t.Errorf("apply option = %+v, want doctor.applyRemedy with only (fingerprint, remedyId)", apply)
	}
	if recheck := blocker.Options[1]; recheck.Verb != attention.VerbDoctorRecheck || len(recheck.Args) != 1 {
		t.Errorf("recheck option = %+v", recheck)
	}

	warning, ok := cards[attention.DoctorCardKey(fxWarning.Check, fxWarning.Fingerprint)]
	if !ok {
		t.Fatalf("no card keyed by the warning's fingerprint; have %v", cards)
	}
	if warning.Severity != attention.SeverityFYI {
		t.Errorf("warning card severity = %q, want fyi", warning.Severity)
	}
	// A manual remedy is body text, never an option.
	if len(warning.Options) != 1 || warning.Options[0].Verb != attention.VerbDoctorRecheck {
		t.Errorf("warning options = %+v, want only recheck", warning.Options)
	}
	for _, want := range []string{"Find the caller", "nightgauge api-usage --by op", "https://docs.github.com/en/graphql",
		"https://evil.example/phish (not a link: host not allowlisted)", "docs/DOCTOR.md#ngd008"} {
		if !strings.Contains(warning.Body, want) {
			t.Errorf("warning body lacks %q:\n%s", want, warning.Body)
		}
	}
	if warning.Context.URL != "https://docs.github.com/en/graphql" {
		t.Errorf("card URL = %q, want the allowlisted link only", warning.Context.URL)
	}
}

func TestDoctorProducer_ClearedFindingResolvesItsCard(t *testing.T) {
	sw, store := doctorSweeper(t,
		func() ([]doctor.CheckResult, error) { return doctorResults(fxBlocker, fxWarning, fxHousekeeping), nil },
		func() ([]doctor.CheckResult, error) {
			return append(doctorResults(fxWarning, fxHousekeeping),
				doctor.CheckResult{ID: fxBlocker.Check, Status: doctor.StatusPassed}), nil
		},
	)
	sweepDoctor(t, sw)
	blockerKey := attention.DoctorCardKey(fxBlocker.Check, fxBlocker.Fingerprint)
	blockerID := openDoctorCards(t, store)[blockerKey].ID
	sweepDoctor(t, sw)
	if got, found, err := store.Get(blockerID); err != nil || !found || got.Lifecycle.State != attention.StateAutoResolved {
		t.Fatalf("blocker card after the clearing sweep = %+v, %v, %v; want auto_resolved", got, found, err)
	}
	cards := openDoctorCards(t, store)
	if _, ok := cards[attention.DoctorCardKey(fxBlocker.Check, fxBlocker.Fingerprint)]; ok {
		t.Error("the cleared blocker's card is still open")
	}
	if _, ok := cards[attention.DoctorCardKey(fxWarning.Check, fxWarning.Fingerprint)]; !ok {
		t.Error("the warning's card was retracted while the warning still holds")
	}
}

func TestDoctorProducer_ErrorLeavesCardsUntouched(t *testing.T) {
	sw, store := doctorSweeper(t,
		func() ([]doctor.CheckResult, error) { return doctorResults(fxBlocker, fxWarning), nil },
		func() ([]doctor.CheckResult, error) { return nil, errors.New("config unreadable") },
	)
	sweepDoctor(t, sw)
	res := sweepDoctor(t, sw)
	if res.Failed[ProducerDoctor] == "" || res.AutoResolved != 0 {
		t.Fatalf("second sweep = %+v, want the producer failed and nothing resolved", res)
	}
	if cards := openDoctorCards(t, store); len(cards) != 2 {
		t.Fatalf("want both cards left open, got %d", len(cards))
	}
}

// A check that timed out or was skipped observed nothing: its card stays even
// though its fingerprint is absent from the scan.
func TestDoctorProducer_IncompleteCheckKeepsItsCard(t *testing.T) {
	timeout := doctorFinding(fxBlocker.Check, "NGD000", doctor.SeverityWarning)
	timeout.Fingerprint = doctor.Fingerprint("NGD000", fxBlocker.Check, "timeout")
	sw, store := doctorSweeper(t,
		func() ([]doctor.CheckResult, error) { return doctorResults(fxBlocker, fxWarning), nil },
		func() ([]doctor.CheckResult, error) {
			return append(doctorResults(fxWarning), doctor.CheckResult{
				ID: fxBlocker.Check, Status: doctor.StatusTimeout, Findings: []doctor.Finding{timeout},
			}), nil
		},
	)
	sweepDoctor(t, sw)
	sweepDoctor(t, sw)
	cards := openDoctorCards(t, store)
	for _, key := range []string{
		attention.DoctorCardKey(fxBlocker.Check, fxBlocker.Fingerprint),
		attention.DoctorCardKey(fxWarning.Check, fxWarning.Fingerprint),
		attention.DoctorCardKey(timeout.Check, timeout.Fingerprint),
	} {
		if _, ok := cards[key]; !ok {
			t.Errorf("card %s is not open; have %v", key, cards)
		}
	}
}

// A sweep cut short by its deadline is an error, not an observation.
func TestDoctorProducer_CancelledScanIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &Doctor{
		Scan:        func(context.Context, string) ([]doctor.CheckResult, error) { return nil, nil },
		PrimaryRepo: func(string) string { return doctorTestRepo },
	}
	if _, err := p.Evaluate(ctx, WorkspaceInput{WorkspaceRoot: t.TempDir()}); err == nil {
		t.Fatal("a cancelled scan was reported as a clean one")
	}
}

func TestDoctorProducer_RedactsEvidence(t *testing.T) {
	f := fxWarning
	f.Evidence = map[string]string{"token": "ghp_" + strings.Repeat("a", 36)}
	card, ok := doctorCard(f, doctorTestRepo)
	if !ok {
		t.Fatal("no card")
	}
	if strings.Contains(card.Body, "ghp_"+strings.Repeat("a", 36)) {
		t.Fatalf("card body carries a raw token:\n%s", card.Body)
	}
}

func TestDoctorProducer_RegisteredInTheDefaultWorkspaceRegistry(t *testing.T) {
	for _, p := range Default.WorkspaceProducers() {
		if p.Name() != ProducerDoctor {
			continue
		}
		d, ok := p.(*Doctor)
		if !ok || d.Scan != nil || d.PrimaryRepo != nil {
			t.Fatalf("registered %q is %T %+v, want *Doctor with no test seams", ProducerDoctor, p, p)
		}
		return
	}
	t.Fatalf("%q is not in the default workspace registry", ProducerDoctor)
}

// The API-budget producer is gone: its condition is the doctor's.
func TestDoctorProducer_ReplacesTheAPIBudgetProducer(t *testing.T) {
	for _, p := range Default.WorkspaceProducers() {
		if p.Name() == "api-budget" {
			t.Fatal("the api-budget producer is still registered; github_api_budget is the doctor's")
		}
	}
}

func TestAllowedDoctorLink(t *testing.T) {
	for link, want := range map[string]bool{
		"https://github.com/nightgauge/nightgauge": true,
		"https://docs.github.com/en/graphql":       true,
		"https://nightgauge.dev/docs":              true,
		"http://github.com/x":                      false,
		"https://github.com.evil.example/x":        false,
		"https://user@github.com/x":                false,
		"https://github.com:8443/x":                false,
		"javascript:alert(1)":                      false,
	} {
		if got := allowedDoctorLink(link); got != want {
			t.Errorf("allowedDoctorLink(%q) = %v, want %v", link, got, want)
		}
	}
}

// --- the verbs through the remedy engine -------------------------------------

// leaseWorld is a one-check doctor whose finding an auto remedy can clear.
type leaseWorld struct{ stale bool }

func (w *leaseWorld) fixer(string) *doctor.Fixer {
	reg := doctor.NewRegistry()
	reg.MustRegister(doctor.Check{ID: fxBlocker.Check, Code: fxBlocker.Code,
		Run: func(context.Context, *doctor.Env) []doctor.Finding {
			if !w.stale {
				return nil
			}
			return []doctor.Finding{fxBlocker}
		}})
	verbs := doctor.NewVerbRegistry()
	if err := verbs.Register("lease.release", doctor.VerbFuncs{
		PreviewFunc:      func(context.Context, doctor.Finding) (string, error) { return "remove the lease file", nil },
		PreconditionFunc: func(context.Context, doctor.Finding) error { return nil },
		ApplyFunc:        func(context.Context, doctor.Finding) error { w.stale = false; return nil },
	}); err != nil {
		panic(err)
	}
	return &doctor.Fixer{Registry: reg, Verbs: verbs, Env: &doctor.Env{}}
}

func blockerCardRequest(t *testing.T) *attention.DecisionRequest {
	t.Helper()
	card, ok := doctorCard(fxBlocker, doctorTestRepo)
	if !ok {
		t.Fatal("no card")
	}
	card.ID = "dr_doctor"
	card.Producer = ProducerDoctor
	return &card
}

func TestDoctorVerbs_ApplyRemedyFixesAndVerifies(t *testing.T) {
	w := &leaseWorld{stale: true}
	runner := DoctorRemedies{WorkspaceRoot: t.TempDir(), NewFixer: w.fixer}
	req := blockerCardRequest(t)

	if err := attention.ExecuteDoctorRecheck(context.Background(), runner, req, req.Options[1]); !errors.Is(err, attention.ErrDoctorFindingStillPresent) {
		t.Fatalf("recheck before the fix = %v, want still present", err)
	}
	if err := attention.ExecuteDoctorApplyRemedy(context.Background(), runner, req, req.Options[0]); err != nil {
		t.Fatalf("applyRemedy: %v", err)
	}
	if w.stale {
		t.Fatal("the remedy's verb never ran")
	}
	if err := attention.ExecuteDoctorRecheck(context.Background(), runner, req, req.Options[1]); err != nil {
		t.Fatalf("recheck after the fix: %v", err)
	}
	// The finding is gone, so a second apply is stale: the card is moot.
	if err := attention.ExecuteDoctorApplyRemedy(context.Background(), runner, req, req.Options[0]); err != nil {
		t.Fatalf("applyRemedy on a stale fingerprint: %v", err)
	}
}

func TestDoctorVerbs_RemedyThatDoesNotVerifyKeepsTheCard(t *testing.T) {
	w := &leaseWorld{stale: true}
	runner := DoctorRemedies{WorkspaceRoot: t.TempDir(), NewFixer: func(root string) *doctor.Fixer {
		fx := w.fixer(root)
		verbs := doctor.NewVerbRegistry()
		_ = verbs.Register("lease.release", doctor.VerbFuncs{
			PreviewFunc:      func(context.Context, doctor.Finding) (string, error) { return "", nil },
			PreconditionFunc: func(context.Context, doctor.Finding) error { return nil },
			ApplyFunc:        func(context.Context, doctor.Finding) error { return nil }, // changes nothing
		})
		fx.Verbs = verbs
		return fx
	}}
	req := blockerCardRequest(t)
	err := attention.ExecuteDoctorApplyRemedy(context.Background(), runner, req, req.Options[0])
	if !errors.Is(err, attention.ErrDoctorFindingStillPresent) {
		t.Fatalf("applyRemedy = %v, want still present", err)
	}
}

// --- the rescan interval -------------------------------------------------------

type fakeDoctorClock struct{ t time.Time }

func (c *fakeDoctorClock) now() time.Time { return c.t }

func cachedDoctor(scan func(context.Context, string) ([]doctor.CheckResult, error)) (*Doctor, *doctorScanCache, *fakeDoctorClock) {
	clock := &fakeDoctorClock{t: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	cache := newDoctorScanCache(DoctorRescanInterval)
	cache.now = clock.now
	return &Doctor{Scan: scan, PrimaryRepo: func(string) string { return doctorTestRepo }, cache: cache}, cache, clock
}

func TestDoctorProducer_ReusesTheScanWithinTheRescanInterval(t *testing.T) {
	scans := 0
	p, _, clock := cachedDoctor(func(context.Context, string) ([]doctor.CheckResult, error) {
		scans++
		return doctorResults(fxBlocker), nil
	})
	in := WorkspaceInput{WorkspaceRoot: t.TempDir()}
	for i := 0; i < 3; i++ {
		reqs, err := p.Evaluate(context.Background(), in)
		if err != nil || len(reqs) != 1 {
			t.Fatalf("sweep %d = %d cards, %v; want the cached blocker", i, len(reqs), err)
		}
		clock.t = clock.t.Add(DoctorRescanInterval / 3)
	}
	if scans != 1 {
		t.Fatalf("scans within the interval = %d, want 1", scans)
	}
	clock.t = clock.t.Add(DoctorRescanInterval)
	if _, err := p.Evaluate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("scans after the interval = %d, want 2", scans)
	}
	// Another workspace root has a scan of its own.
	if _, err := p.Evaluate(context.Background(), WorkspaceInput{WorkspaceRoot: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if scans != 3 {
		t.Fatalf("a second root reused the first root's scan (scans = %d)", scans)
	}
}

func TestDoctorProducer_FailedOrCancelledScanIsNotCached(t *testing.T) {
	scans := 0
	var fail error
	p, _, _ := cachedDoctor(func(context.Context, string) ([]doctor.CheckResult, error) {
		scans++
		return doctorResults(fxBlocker), fail
	})
	in := WorkspaceInput{WorkspaceRoot: t.TempDir()}
	fail = errors.New("config unreadable")
	if _, err := p.Evaluate(context.Background(), in); err == nil {
		t.Fatal("a failed scan was reported as an observation")
	}
	fail = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Evaluate(ctx, in); err == nil {
		t.Fatal("a cancelled scan was reported as an observation")
	}
	if _, err := p.Evaluate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if scans != 3 {
		t.Fatalf("scans = %d, want 3: neither the failed nor the cancelled scan may be reused", scans)
	}
}

type doctorVerbExecutor struct{ runner attention.DoctorRemedyRunner }

func (e doctorVerbExecutor) ExecuteVerb(ctx context.Context, req *attention.DecisionRequest, opt attention.Option) error {
	if opt.Verb == attention.VerbDoctorRecheck {
		return attention.ExecuteDoctorRecheck(ctx, e.runner, req, opt)
	}
	return attention.ExecuteDoctorApplyRemedy(ctx, e.runner, req, opt)
}

// The operator fixes the condition by hand and re-checks: the card resolves,
// and the next sweep, still inside the rescan interval, scans again rather
// than raising the card anew from the scan taken before the fix.
func TestDoctorProducer_RecheckThatClearsAFindingIsNotReRaised(t *testing.T) {
	w := &leaseWorld{stale: true}
	scans := 0
	p, cache, clock := cachedDoctor(func(ctx context.Context, root string) ([]doctor.CheckResult, error) {
		scans++
		return w.fixer(root).Scan(ctx), nil
	})
	sw, store := coverageSweeper(t, p)
	sweepDoctor(t, sw)
	cards := openDoctorCards(t, store)
	card, ok := cards[attention.DoctorCardKey(fxBlocker.Check, fxBlocker.Fingerprint)]
	if !ok || len(cards) != 1 {
		t.Fatalf("first sweep cards = %v, want the blocker", cards)
	}

	w.stale = false // fixed by hand
	runner := DoctorRemedies{WorkspaceRoot: sw.WorkspaceRoot, NewFixer: w.fixer, cache: cache}
	if _, err := store.Resolve(context.Background(), card.ID, "recheck", "operator@test", "", "",
		doctorVerbExecutor{runner: runner}); err != nil {
		t.Fatalf("resolve via recheck: %v", err)
	}

	clock.t = clock.t.Add(time.Minute) // well inside the interval
	sweepDoctor(t, sw)
	if scans != 2 {
		t.Fatalf("scans = %d, want 2: the recheck must drop the cached scan", scans)
	}
	if open := openDoctorCards(t, store); len(open) != 0 {
		t.Fatalf("the cleared finding was raised again: %v", open)
	}
}

func TestSwapDoctorScanForTest_ResetsTheCache(t *testing.T) {
	root := t.TempDir()
	calls := 0
	restore := SwapDoctorScanForTest(func(context.Context, string) ([]doctor.CheckResult, error) {
		calls++
		return nil, nil
	})
	defer restore()
	p := &Doctor{PrimaryRepo: func(string) string { return doctorTestRepo }}
	in := WorkspaceInput{WorkspaceRoot: root}
	for i := 0; i < 2; i++ {
		if _, err := p.Evaluate(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("scans = %d, want 1 through the process-wide cache", calls)
	}
	ResetDoctorScanCacheForTest()
	if _, err := p.Evaluate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("scans after a reset = %d, want 2", calls)
	}
}

// Concurrent sweeps of one root (the CLI and the daemon in one process) share
// one scan.
func TestDoctorProducer_ConcurrentSweepsShareOneScan(t *testing.T) {
	var scans atomic.Int32
	p, _, _ := cachedDoctor(func(context.Context, string) ([]doctor.CheckResult, error) {
		scans.Add(1)
		time.Sleep(20 * time.Millisecond)
		return doctorResults(fxBlocker), nil
	})
	in := WorkspaceInput{WorkspaceRoot: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if reqs, err := p.Evaluate(context.Background(), in); err != nil || len(reqs) != 1 {
				t.Errorf("concurrent sweep = %d cards, %v", len(reqs), err)
			}
		}()
	}
	wg.Wait()
	if n := scans.Load(); n != 1 {
		t.Fatalf("scans = %d, want 1", n)
	}
}
