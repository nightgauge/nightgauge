package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/cadence"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/layout/layouttest"
)

// #2090: a stopped autonomous loop is restarted through the daemon's own start
// method, with confirm consent, a preview that changes nothing, a precondition
// re-read at apply time and verification by re-running scheduled_automations.

// fakeStarter stands in for the daemon's autonomous.start. Its Start does what
// the real scheduler's first cycle does to the evidence: record a scan.
type fakeStarter struct {
	root     string
	reachErr atomic.Value // error
	started  atomic.Int32
	noScan   bool
}

func (s *fakeStarter) Reachable(context.Context, string) error {
	if err, ok := s.reachErr.Load().(error); ok && err != nil {
		return err
	}
	return nil
}

func (s *fakeStarter) Start(_ context.Context, root string) error {
	s.started.Add(1)
	if !s.noScan {
		writeAutonomousState(nil, root, time.Now())
	}
	return nil
}

func useStarter(t *testing.T, s AutonomousStarter) {
	t.Helper()
	prev := RegisterAutonomousStarter(s)
	t.Cleanup(func() { RegisterAutonomousStarter(prev) })
	wait := restartWait
	restartWait = 2 * time.Second
	t.Cleanup(func() { restartWait = wait })
}

// writeAutonomousState writes the scheduler's state file the way the
// scheduler persists it (status and lastScanAt, RFC 3339).
func writeAutonomousState(t *testing.T, root string, lastScan time.Time) {
	path, err := layout.CheckoutPath(root, "autonomous/state.json")
	if err != nil {
		if t != nil {
			t.Fatal(err)
		}
		return
	}
	data, _ := json.Marshal(map[string]string{"status": "stopped", "lastScanAt": lastScan.UTC().Format(time.RFC3339)})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		err = os.WriteFile(path, data, 0o600)
		if err != nil && t != nil {
			t.Fatal(err)
		}
	} else if t != nil {
		t.Fatal(err)
	}
}

func stateBytes(t *testing.T, root string) string {
	t.Helper()
	b, err := os.ReadFile(layouttest.CheckoutPath(t, root, "autonomous/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stoppedLoop is a workspace whose autonomous loop last scanned 17 days ago.
func stoppedLoop(t *testing.T) (string, *Fixer) {
	t.Helper()
	isolateMachineState(t)
	root := layouttest.Repo(t)
	writeAutonomousState(t, root, time.Now().AddDate(0, 0, -17))
	env := &Env{Cwd: root, Now: time.Now()}
	return root, &Fixer{
		Registry: registryOf(t, "scheduled_automations"), Verbs: BuiltinVerbs(env), Env: env,
		Log: &FixLog{Path: filepath.Join(t.TempDir(), "fix-log.jsonl")},
	}
}

func restartResult(t *testing.T, rep FixReport) FixResult {
	t.Helper()
	return resultFor(t, rep, codeAutomationStopped, "automation", "autonomous-loop")
}

func TestAutomationRestart_VerbIsRegistered(t *testing.T) {
	if _, ok := BuiltinVerbs(&Env{}).Lookup(verbAutomationRestart); !ok {
		t.Fatalf("%s is declared by scheduled_automations but not registered", verbAutomationRestart)
	}
}

func TestAutomationRestart_PreviewWritesNothing(t *testing.T) {
	root, fx := stoppedLoop(t)
	s := &fakeStarter{root: root}
	useStarter(t, s)
	before := stateBytes(t, root)

	rep := fx.Run(context.Background(), FixOptions{DryRun: true, Yes: true})
	res := restartResult(t, rep)
	if res.Action != ActionPreviewed || res.Remedy == nil || res.Remedy.Kind != RemedyConfirm {
		t.Fatalf("dry run: %+v, want a previewed confirm remedy", res)
	}
	if !strings.Contains(res.Preview, "daemon") || !strings.Contains(res.Preview, "autonomous-loop") {
		t.Errorf("preview does not say what it would do: %q", res.Preview)
	}
	if n := s.started.Load(); n != 0 {
		t.Errorf("--dry-run started the scheduler %d time(s)", n)
	}
	if after := stateBytes(t, root); after != before {
		t.Errorf("--dry-run changed the state file:\n%s\n%s", before, after)
	}
	if entries, _, _ := ReadFixLog(fx.Log.Path); len(entries) != 0 {
		t.Errorf("--dry-run wrote %d fix log entries", len(entries))
	}
}

func TestAutomationRestart_NeedsConsent(t *testing.T) {
	root, fx := stoppedLoop(t)
	s := &fakeStarter{root: root}
	useStarter(t, s)
	res := restartResult(t, fx.Run(context.Background(), FixOptions{}))
	if res.Action != ActionAwaitingConsent || s.started.Load() != 0 {
		t.Fatalf("without --yes: %s, started %d; want awaiting-consent and no start", res.Action, s.started.Load())
	}
}

func TestAutomationRestart_ApplyRestartsThroughTheEntryPointAndVerifies(t *testing.T) {
	root, fx := stoppedLoop(t)
	s := &fakeStarter{root: root}
	useStarter(t, s)

	rep := fx.Run(context.Background(), FixOptions{Yes: true})
	res := restartResult(t, rep)
	if res.Outcome != OutcomeFixed {
		t.Fatalf("outcome %s (%s), want fixed", res.Outcome, res.Detail)
	}
	if !strings.Contains(res.Detail, "verified: scheduled_automations") {
		t.Errorf("outcome not decided by the re-run check: %q", res.Detail)
	}
	if n := s.started.Load(); n != 1 {
		t.Errorf("entry point called %d times, want 1", n)
	}
	if rep.ExitCode != 0 || rep.Counts.Blocked != 0 {
		t.Errorf("exit %d, counts %+v; want 0 and nothing blocked", rep.ExitCode, rep.Counts)
	}
}

// A start the loop never follows with a scan is still-present: Apply's own
// return is never taken as success.
func TestAutomationRestart_NoScanIsStillPresent(t *testing.T) {
	root, fx := stoppedLoop(t)
	s := &fakeStarter{root: root, noScan: true}
	useStarter(t, s)
	restartWait = 300 * time.Millisecond
	res := restartResult(t, fx.Run(context.Background(), FixOptions{Yes: true}))
	if res.Outcome != OutcomeStillPresent || s.started.Load() != 1 {
		t.Fatalf("outcome %s (%s), started %d; want still-present after one start", res.Outcome, res.Detail, s.started.Load())
	}
}

func TestAutomationRestart_PreconditionChangeRefuses(t *testing.T) {
	cases := map[string]func(root string, s *fakeStarter){
		"the loop scanned since": func(root string, _ *fakeStarter) {
			writeAutonomousState(nil, root, time.Now().AddDate(0, 0, -10))
		},
		"the loop is fresh again": func(root string, _ *fakeStarter) {
			writeAutonomousState(nil, root, time.Now())
		},
		"the daemon went away": func(_ string, s *fakeStarter) {
			s.reachErr.Store(errors.New("dial unix: connection refused"))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			root, fx := stoppedLoop(t)
			s := &fakeStarter{root: root}
			useStarter(t, s)
			var f Finding
			for _, x := range fx.Scan(context.Background())[0].Findings {
				if x.Code == codeAutomationStopped {
					f = x
				}
			}
			if f.Fingerprint == "" {
				t.Fatal("no stopped finding in the scan")
			}
			change(root, s)
			res := fx.apply(context.Background(), f, f.Remedies[0], mustVerb(t, fx), true, "")
			if res.Outcome != OutcomeBlocked {
				t.Fatalf("outcome %s (%s), want blocked", res.Outcome, res.Detail)
			}
			if n := s.started.Load(); n != 0 {
				t.Errorf("a refused precondition still started the scheduler %d time(s)", n)
			}
		})
	}
}

func mustVerb(t *testing.T, fx *Fixer) RemedyVerb {
	t.Helper()
	v, ok := fx.Verbs.Lookup(verbAutomationRestart)
	if !ok {
		t.Fatal("automation.restart is not registered")
	}
	return v
}

// With no daemon to reach, the restart is manual start steps, so --fix --yes
// reports it manual instead of a blocked remedy (exit 4).
func TestAutomationRestart_UnreachableEntryPointIsManual(t *testing.T) {
	for name, s := range map[string]AutonomousStarter{
		"no entry point wired": nil,
		"no daemon listening":  unreachable{},
	} {
		t.Run(name, func(t *testing.T) {
			_, fx := stoppedLoop(t)
			useStarter(t, s)
			rep := fx.Run(context.Background(), FixOptions{Yes: true})
			res := restartResult(t, rep)
			if res.Action != ActionManual || res.Remedy == nil || res.Remedy.Kind != RemedyManual {
				t.Fatalf("result %+v, want the manual start steps", res)
			}
			if !strings.Contains(strings.Join(res.Remedy.Steps, "\n"), "nightgauge autonomous start") {
				t.Errorf("steps do not name the entry point: %v", res.Remedy.Steps)
			}
			if rep.ExitCode == 4 || rep.Counts.Blocked != 0 {
				t.Errorf("exit %d, counts %+v; an unreachable daemon must not read as a blocked remedy", rep.ExitCode, rep.Counts)
			}
		})
	}
}

type unreachable struct{}

func (unreachable) Reachable(context.Context, string) error {
	return errors.New("dial unix: no such file or directory")
}
func (unreachable) Start(context.Context, string) error { return errors.New("unreachable") }

// A stopped workflow's normal entry point is GitHub's scheduler, which doctor
// cannot invoke: its restart remedy is manual and names the schedule.
func TestAutomationRestart_WorkflowRestartIsManual(t *testing.T) {
	r := restartRemedy(cadence.Automation{ID: "nightly", Kind: cadence.EvidenceWorkflowRun, Workflow: "nightly.yml"}, nil)
	if r.Kind != RemedyManual || r.Verb != "" {
		t.Fatalf("workflow restart = %+v, want manual", r)
	}
	if !strings.Contains(strings.Join(r.Steps, "\n"), "gh workflow enable nightly.yml") {
		t.Errorf("steps = %v", r.Steps)
	}
}
