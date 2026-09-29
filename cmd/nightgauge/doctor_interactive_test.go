package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// #2095. The guided repair session is driven with scripted keystrokes on a
// plain reader; no real terminal is involved.

// fixtureObject is one condition a fixture check reports and a fixture verb
// repairs.
type fixtureObject struct {
	code     string
	severity doctor.Severity
	kind     doctor.RemedyKind
	title    string
	steps    []string
	links    []string
	// fixedAfterRuns, for a manual remedy, clears the condition once the
	// check has run this many times (the scan is run 1).
	fixedAfterRuns int32

	present atomic.Bool
	applied atomic.Int32
	runs    atomic.Int32
	// onApply runs inside Apply, before the condition is cleared.
	onApply func()
}

func (o *fixtureObject) check() string { return "fixture_" + strings.ToLower(o.code) }

func (o *fixtureObject) finding() doctor.Finding {
	rem := doctor.Remedy{
		ID: "repair", Kind: o.kind, Summary: "Repair " + o.code,
		Preview: "declared preview for " + o.code, Verify: o.check(),
		Steps: o.steps, Links: o.links,
	}
	if o.kind != doctor.RemedyManual {
		rem.Verb = "fixture.repair"
	}
	return doctor.Finding{
		Code: o.code, Check: o.check(), Severity: o.severity, Title: o.title,
		Cause:       "a fixture condition on " + o.code,
		Evidence:    map[string]string{"object": o.code},
		Docs:        doctor.DocsAnchor(o.code),
		Fingerprint: doctor.Fingerprint(o.code, "object"),
		Remedies:    []doctor.Remedy{rem},
	}
}

// fixtureFixer is a Fixer over one check per object and a verb that clears
// the object's condition. Its live preview differs from the declared one, so
// a test can tell which the session showed.
func fixtureFixer(t *testing.T, objs ...*fixtureObject) *doctor.Fixer {
	t.Helper()
	byCode := map[string]*fixtureObject{}
	reg := doctor.NewRegistry()
	for _, o := range objs {
		o := o
		o.present.Store(true)
		byCode[o.code] = o
		reg.MustRegister(doctor.Check{
			ID: o.check(), Title: "Fixture " + o.code, Group: "fixture", Code: o.code,
			Run: func(ctx context.Context, env *doctor.Env) []doctor.Finding {
				n := o.runs.Add(1)
				if o.fixedAfterRuns > 0 && n >= o.fixedAfterRuns {
					o.present.Store(false)
				}
				if !o.present.Load() {
					return nil
				}
				return []doctor.Finding{o.finding()}
			},
		})
	}
	verbs := doctor.NewVerbRegistry()
	if err := verbs.Register("fixture.repair", doctor.VerbFuncs{
		PreviewFunc: func(ctx context.Context, f doctor.Finding) (string, error) {
			return "live preview: repair " + f.Code, nil
		},
		PreconditionFunc: func(ctx context.Context, f doctor.Finding) error { return nil },
		ApplyFunc: func(ctx context.Context, f doctor.Finding) error {
			o := byCode[f.Code]
			o.applied.Add(1)
			if o.onApply != nil {
				o.onApply()
			}
			o.present.Store(false)
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}
	return &doctor.Fixer{
		Registry: reg, Verbs: verbs,
		Env: &doctor.Env{Cwd: t.TempDir(), Now: time.Now()},
		Log: &doctor.FixLog{Path: filepath.Join(t.TempDir(), "fix-log.jsonl")},
	}
}

// syncBuffer is a bytes.Buffer safe for the runner's concurrent observer.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// scriptedTerm is an interactive, colorless 80-column terminal whose stdin is
// the scripted keys.
func scriptedTerm(keys string, out *syncBuffer) doctorTerm {
	return doctorTerm{in: strings.NewReader(keys), out: out, interactive: true, width: 80}
}

func guided(t *testing.T, keys string, fx *doctor.Fixer) (string, int) {
	t.Helper()
	out := &syncBuffer{}
	code := runGuidedRepair(context.Background(), scriptedTerm(keys, out), fx)
	return out.String(), code
}

// reportLine returns the final report's line for code.
func reportLine(t *testing.T, out, code string) string {
	t.Helper()
	_, report, ok := strings.Cut(out, "\nRepair report:\n")
	if !ok {
		t.Fatalf("no repair report in output:\n%s", out)
	}
	for _, line := range strings.Split(report, "\n") {
		if strings.Contains(line, code) {
			return line
		}
	}
	t.Fatalf("repair report has no line for %s:\n%s", code, report)
	return ""
}

func threeFindings() (*fixtureObject, *fixtureObject, *fixtureObject) {
	return &fixtureObject{code: "NGD901", severity: doctor.SeverityBlocker, kind: doctor.RemedyAuto, title: "first fixture finding"},
		&fixtureObject{code: "NGD902", severity: doctor.SeverityWarning, kind: doctor.RemedyAuto, title: "second fixture finding"},
		&fixtureObject{code: "NGD903", severity: doctor.SeverityHousekeeping, kind: doctor.RemedyConfirm, title: "third fixture finding"}
}

func TestDoctorInteractive_FixSkipQuit(t *testing.T) {
	a, b, c := threeFindings()
	out, code := guided(t, "fsq", fixtureFixer(t, a, b, c))

	if a.applied.Load() != 1 || b.applied.Load() != 0 || c.applied.Load() != 0 {
		t.Fatalf("applied = %d/%d/%d, want 1/0/0\n%s", a.applied.Load(), b.applied.Load(), c.applied.Load(), out)
	}
	for code, want := range map[string]string{"NGD901": "FIXED", "NGD902": "SKIPPED", "NGD903": "NOT ATTEMPTED"} {
		if line := reportLine(t, out, code); !strings.Contains(line, want) {
			t.Errorf("report line for %s = %q, want %s", code, line, want)
		}
	}
	// The fix is verified inline, before the next finding is offered.
	fixed := strings.Index(out, "FIXED verified: fixture_ngd901 no longer reports it")
	next := strings.Index(out, "\n[2/3] WARNING")
	if fixed < 0 || next < 0 || fixed > next {
		t.Errorf("inline verify result missing or after the next finding:\n%s", out)
	}
	// The warning is still present, so the post-repair state is degraded.
	if code != 1 {
		t.Errorf("exit = %d, want 1 (warning still present)", code)
	}
	if !strings.Contains(out, "1 fixed, 0 still present, 1 skipped, 1 not attempted, 0 need you") {
		t.Errorf("counts line missing:\n%s", out)
	}
}

func TestDoctorInteractive_ConfirmShowsPreviewBeforeApply(t *testing.T) {
	c := &fixtureObject{code: "NGD903", severity: doctor.SeverityWarning, kind: doctor.RemedyConfirm, title: "confirm fixture"}
	out := &syncBuffer{}
	var atApply string
	c.onApply = func() { atApply = out.String() }
	code := runGuidedRepair(context.Background(), scriptedTerm("fy", out), fixtureFixer(t, c))

	if c.applied.Load() != 1 {
		t.Fatalf("confirm remedy not applied:\n%s", out.String())
	}
	// At the moment Apply ran, the operator had seen the verb's live preview
	// and the explicit confirm question.
	preview := strings.Index(atApply, "preview: live preview: repair NGD903")
	question := strings.Index(atApply, "Apply this confirm remedy?")
	if preview < 0 || question < 0 || preview > question {
		t.Fatalf("preview not shown before the confirm question and apply; output at apply:\n%s", atApply)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
}

func TestDoctorInteractive_ConfirmDeclinedIsNotApplied(t *testing.T) {
	c := &fixtureObject{code: "NGD903", severity: doctor.SeverityWarning, kind: doctor.RemedyConfirm, title: "confirm fixture"}
	out, code := guided(t, "fns", fixtureFixer(t, c))
	if c.applied.Load() != 0 {
		t.Fatalf("declined confirm remedy was applied:\n%s", out)
	}
	if !strings.Contains(reportLine(t, out, "NGD903"), "SKIPPED") || code != 1 {
		t.Errorf("want SKIPPED and exit 1, got exit %d:\n%s", code, out)
	}
}

func TestDoctorInteractive_AllSafeAppliesRemainingAutoOnly(t *testing.T) {
	a, b, c := threeFindings()
	// a: all safe applies a and b without a prompt; the confirm c still asks.
	out, _ := guided(t, "as", fixtureFixer(t, a, b, c))
	if a.applied.Load() != 1 || b.applied.Load() != 1 || c.applied.Load() != 0 {
		t.Fatalf("applied = %d/%d/%d, want 1/1/0\n%s", a.applied.Load(), b.applied.Load(), c.applied.Load(), out)
	}
	if !strings.Contains(out, "applying (all safe): Repair NGD902") {
		t.Errorf("second auto remedy was not applied under all safe:\n%s", out)
	}
	if !strings.Contains(reportLine(t, out, "NGD903"), "SKIPPED") {
		t.Errorf("confirm remedy not skipped:\n%s", out)
	}
}

func TestDoctorInteractive_ManualCheckAgainRerunsOnlyThatCheck(t *testing.T) {
	m := &fixtureObject{code: "NGD904", severity: doctor.SeverityBlocker, kind: doctor.RemedyManual,
		title: "GitHub App permission missing", fixedAfterRuns: 3,
		steps: []string{"Open the App settings", "Grant Projects read and write"}}
	other := &fixtureObject{code: "NGD905", severity: doctor.SeverityHousekeeping, kind: doctor.RemedyAuto, title: "other"}
	out, code := guided(t, "ccs", fixtureFixer(t, m, other))

	if !strings.Contains(out, "1. Open the App settings") || !strings.Contains(out, "2. Grant Projects read and write") {
		t.Errorf("numbered steps missing:\n%s", out)
	}
	still := strings.Index(out, "STILL PRESENT fixture_ngd904 still reports it")
	fixed := strings.Index(out, "FIXED verified: fixture_ngd904 no longer reports it")
	if still < 0 || fixed < 0 || still > fixed {
		t.Fatalf("want STILL PRESENT then FIXED on check again:\n%s", out)
	}
	// Scan (1) plus two check-agains; the other check ran only in the scan.
	if m.runs.Load() != 3 || other.runs.Load() != 1 {
		t.Errorf("runs = %d/%d, want 3/1: check again must re-run only its own check", m.runs.Load(), other.runs.Load())
	}
	if !strings.Contains(reportLine(t, out, "NGD904"), "FIXED") {
		t.Errorf("manual finding not reported fixed:\n%s", out)
	}
	if code != 0 {
		t.Errorf("exit = %d, want 0 once the blocker is gone", code)
	}
}

func TestDoctorInteractive_OpenOnlyAllowlistedLinksAsOneArgument(t *testing.T) {
	m := &fixtureObject{code: "NGD904", severity: doctor.SeverityWarning, kind: doctor.RemedyManual, title: "manual",
		steps: []string{"do it"},
		links: []string{
			"https://github.com/settings/apps",
			"https://evil.example.com/phish",
			"http://github.com/insecure",
			"https://github.com/a; rm -rf /",
		}}
	out := &syncBuffer{}
	term := scriptedTerm("os", out)
	var opened []string
	term.open = func(u string) error { opened = append(opened, u); return nil }
	runGuidedRepair(context.Background(), term, fixtureFixer(t, m))

	if len(opened) != 1 || opened[0] != "https://github.com/settings/apps" {
		t.Fatalf("opened = %q, want only the allowlisted https github.com link", opened)
	}
	if !strings.Contains(out.String(), "not opened (host not on the allowlist): https://evil.example.com/phish") {
		t.Errorf("disallowed link not reported:\n%s", out.String())
	}
}

func TestDoctorInteractive_CtrlCKeyQuitsWithoutApplying(t *testing.T) {
	a, b, c := threeFindings()
	out, _ := guided(t, "\x03", fixtureFixer(t, a, b, c))
	if a.applied.Load()+b.applied.Load()+c.applied.Load() != 0 {
		t.Fatalf("a remedy was applied after Ctrl-C:\n%s", out)
	}
	for _, code := range []string{"NGD901", "NGD902", "NGD903"} {
		if !strings.Contains(reportLine(t, out, code), "NOT ATTEMPTED") {
			t.Errorf("%s not reported as not attempted:\n%s", code, out)
		}
	}
}

// An interrupt that arrives while a remedy applies lets it finish, verified,
// and stops before the next one without reading another key.
func TestDoctorInteractive_InterruptFinishesCurrentRemedyThenStops(t *testing.T) {
	a, b, c := threeFindings()
	sig := make(chan os.Signal)
	a.onApply = func() {
		// The second send completes only after the watcher stored the first.
		sig <- os.Interrupt
		sig <- os.Interrupt
	}
	out := &syncBuffer{}
	term := scriptedTerm("f", out)
	term.interrupts = func() (<-chan os.Signal, func()) { return sig, func() {} }
	runGuidedRepair(context.Background(), term, fixtureFixer(t, a, b, c))

	if a.applied.Load() != 1 || b.applied.Load() != 0 || c.applied.Load() != 0 {
		t.Fatalf("applied = %d/%d/%d, want 1/0/0\n%s", a.applied.Load(), b.applied.Load(), c.applied.Load(), out.String())
	}
	s := out.String()
	if !strings.Contains(reportLine(t, s, "NGD901"), "FIXED") || !strings.Contains(reportLine(t, s, "NGD902"), "NOT ATTEMPTED") {
		t.Errorf("want NGD901 fixed and NGD902 not attempted:\n%s", s)
	}
	if !strings.Contains(s, "Interrupted: the last remedy finished; stopping here.") {
		t.Errorf("interrupt notice missing:\n%s", s)
	}
}

func TestDoctorInteractive_ProgressSummaryAndPlainOutput(t *testing.T) {
	a, b, c := threeFindings()
	a.title = strings.Repeat("a very long finding title that must not overflow ", 4)
	out, _ := guided(t, "qqq", fixtureFixer(t, a, b, c))

	for _, want := range []string{
		"running 3 checks in 1 groups (all PENDING)",
		"[3/3] FINDING",
		"\nfixture\n",
		"3 checks in ",
		"Summary: 1 blocker, 1 warning, 1 housekeeping, 0 info",
		"HOUSEKEEPING 1 item(s): NGD903 x1. Press h at a prompt to list them.",
		"Before: 1 blocker, 1 warning, 1 housekeeping",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	// Blockers first in the summary.
	if i, j := strings.Index(out, "BLOCKER NGD901"), strings.Index(out, "WARNING NGD902"); i < 0 || j < 0 || i > j {
		t.Errorf("summary is not blockers first:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("colorless terminal received an escape sequence:\n%q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("line of %d columns on an 80-column terminal: %q", n, line)
		}
	}
}

func TestDoctorInteractive_HousekeepingExpands(t *testing.T) {
	a, b, c := threeFindings()
	out, _ := guided(t, "hq", fixtureFixer(t, a, b, c))
	if !strings.Contains(out, "Housekeeping:\n    NGD903  third fixture finding") {
		t.Errorf("h did not list housekeeping:\n%s", out)
	}
}

func TestDoctorInteractive_EscapeSequenceIsNotACommand(t *testing.T) {
	a := &fixtureObject{code: "NGD901", severity: doctor.SeverityWarning, kind: doctor.RemedyAuto, title: "one"}
	// Up-arrow (ESC [ A) must not read as "a" (all safe).
	out, _ := guided(t, "\x1b[As", fixtureFixer(t, a))
	if a.applied.Load() != 0 || !strings.Contains(reportLine(t, out, "NGD901"), "SKIPPED") {
		t.Fatalf("an arrow key acted as a command:\n%s", out)
	}
}

// failingReader fails the test on any read: a non-interactive doctor must
// never touch stdin.
type failingReader struct{ reads atomic.Int32 }

func (r *failingReader) Read([]byte) (int, error) {
	r.reads.Add(1)
	return 0, errors.New("stdin must not be read")
}

func TestDoctorNonTTY_NeverReadsStdinAndMatchesNoInteractive(t *testing.T) {
	a, b, c := threeFindings()
	scanned := doctor.BuildResult(doctor.Runner{}.Run(context.Background(), fixtureFixer(t, a, b, c).Registry, &doctor.Env{}))
	deps := doctorDeps{
		scan: func(context.Context) doctor.DoctorResult { return scanned },
		newFixer: func() *doctor.Fixer {
			t.Fatal("the list report must not build the remedy engine")
			return nil
		},
	}

	stdin := &failingReader{}
	var pipeOut bytes.Buffer
	pipeCode := runDoctorCommand(context.Background(),
		doctorTerm{in: stdin, out: &pipeOut, interactive: false, width: 80}, doctorInvocation{}, deps)

	var listOut bytes.Buffer
	listCode := runDoctorCommand(context.Background(),
		doctorTerm{in: stdin, out: &listOut, interactive: true, width: 80}, doctorInvocation{noInteractive: true}, deps)

	if stdin.reads.Load() != 0 {
		t.Fatalf("stdin was read %d time(s)", stdin.reads.Load())
	}
	if pipeOut.String() != listOut.String() || pipeCode != listCode {
		t.Fatalf("non-TTY output differs from --no-interactive:\n--- no TTY (exit %d)\n%s\n--- --no-interactive (exit %d)\n%s",
			pipeCode, pipeOut.String(), listCode, listOut.String())
	}
	if pipeCode != 2 || !strings.Contains(pipeOut.String(), "NGD901") {
		t.Errorf("exit %d, want 2 with the blocker listed:\n%s", pipeCode, pipeOut.String())
	}
	if strings.Contains(pipeOut.String(), "\x1b") {
		t.Errorf("escape sequence in plain output")
	}
}

func TestDoctorNonTTY_FixWithoutYesAppliesOnlyAuto(t *testing.T) {
	a, b, c := threeFindings()
	fx := fixtureFixer(t, a, b, c)
	stdin := &failingReader{}
	var out bytes.Buffer
	inv := doctorInvocation{fix: doctorFixFlags{fix: true}}
	var err error
	if inv.opts, err = inv.fix.options(); err != nil {
		t.Fatal(err)
	}
	code := runDoctorCommand(context.Background(), doctorTerm{in: stdin, out: &out, width: 80}, inv,
		doctorDeps{newFixer: func() *doctor.Fixer { return fx }})

	if stdin.reads.Load() != 0 {
		t.Fatalf("stdin was read without a terminal")
	}
	if a.applied.Load() != 1 || b.applied.Load() != 1 || c.applied.Load() != 0 {
		t.Fatalf("applied = %d/%d/%d, want auto only (1/1/0)\n%s", a.applied.Load(), b.applied.Load(), c.applied.Load(), out.String())
	}
	if !strings.Contains(out.String(), "[NEEDS --yes] NGD903") || code != 0 {
		t.Errorf("exit %d, want 0 and the confirm remedy awaiting --yes:\n%s", code, out.String())
	}
}

func TestDoctorInteractive_FixOnTerminalAsksBeforeConfirm(t *testing.T) {
	a, _, c := threeFindings()
	fx := fixtureFixer(t, a, c)
	out := &syncBuffer{}
	inv := doctorInvocation{fix: doctorFixFlags{fix: true}}
	var err error
	if inv.opts, err = inv.fix.options(); err != nil {
		t.Fatal(err)
	}
	var atApply string
	c.onApply = func() { atApply = out.String() }
	runDoctorCommand(context.Background(), scriptedTerm("y", out), inv, doctorDeps{newFixer: func() *doctor.Fixer { return fx }})

	if a.applied.Load() != 1 || c.applied.Load() != 1 {
		t.Fatalf("applied = %d/%d, want 1/1\n%s", a.applied.Load(), c.applied.Load(), out.String())
	}
	if !strings.Contains(atApply, "preview: live preview: repair NGD903") || !strings.Contains(atApply, "Apply? [y]es [n]o") {
		t.Errorf("confirm remedy applied before its preview and question:\n%s", atApply)
	}
}

func TestDoctorInteractive_JSONOnTerminalNeverPrompts(t *testing.T) {
	a, b, c := threeFindings()
	scanned := doctor.BuildResult(doctor.Runner{}.Run(context.Background(), fixtureFixer(t, a, b, c).Registry, &doctor.Env{}))
	stdin := &failingReader{}
	var out bytes.Buffer
	code := runDoctorCommand(context.Background(), doctorTerm{in: stdin, out: &out, interactive: true, width: 80},
		doctorInvocation{json: true}, doctorDeps{scan: func(context.Context) doctor.DoctorResult { return scanned }})
	if stdin.reads.Load() != 0 || code != 2 || !strings.HasPrefix(out.String(), "{") {
		t.Fatalf("--json on a terminal: reads %d, exit %d, output %q", stdin.reads.Load(), code, out.String())
	}
}

func TestWrapText(t *testing.T) {
	lines := wrapText("alpha beta gamma delta "+strings.Repeat("x", 45), 20)
	for _, l := range lines {
		if len([]rune(l)) > 20 {
			t.Errorf("line %q exceeds 20 columns", l)
		}
	}
	if lines[0] != "alpha beta gamma" {
		t.Errorf("first line = %q", lines[0])
	}
}
