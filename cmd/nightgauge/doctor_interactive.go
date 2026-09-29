package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/nightgauge/nightgauge/internal/doctor"
)

// Plain `nightgauge doctor` on a terminal is a guided repair session (#2095,
// ADR-025 § 6): a live progress view while the checks run, a severity-grouped
// summary, a walk through every finding that declares a remedy, and a
// before/after report. It drives the remedy engine through FixOptions.Consent
// and FixOptions.Progress; the engine still decides and verifies every
// outcome. Without a terminal, or with --json or --no-interactive, doctor
// never prompts and never reads stdin.

// doctorTerm is the terminal one doctor invocation talks to.
type doctorTerm struct {
	in  io.Reader
	out io.Writer
	// interactive: stdin and stdout are both terminals, so prompting is allowed.
	interactive bool
	// color allows ANSI color (never under NO_COLOR, TERM=dumb or a pipe).
	color bool
	// live allows the single-line spinner; otherwise progress is one line per
	// state change.
	live  bool
	width int
	// raw switches stdin to single-key mode for one read; nil reads the
	// scripted stream as it is (tests).
	raw func() (restore func(), err error)
	// open hands one allowlisted URL to the OS opener.
	open func(url string) error
	// interrupts delivers Ctrl-C; nil means none are watched.
	interrupts func() (<-chan os.Signal, func())
}

// stdioDoctorTerm is the process's own terminal.
func stdioDoctorTerm() doctorTerm {
	inFd, outFd := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	t := doctorTerm{
		in: os.Stdin, out: os.Stdout,
		interactive: term.IsTerminal(inFd) && term.IsTerminal(outFd),
		color:       doctor.ColorEnabled(os.Stdout),
		width:       80,
		open:        openURL,
	}
	t.live = t.interactive && t.color
	if w, _, err := term.GetSize(outFd); err == nil && w > 0 {
		t.width = w
	}
	if t.interactive {
		t.raw = func() (func(), error) {
			old, err := term.MakeRaw(inFd)
			if err != nil {
				return nil, err
			}
			return func() { _ = term.Restore(inFd, old) }, nil
		}
		t.interrupts = func() (<-chan os.Signal, func()) {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, os.Interrupt)
			return ch, func() { signal.Stop(ch) }
		}
	}
	return t
}

// openURL passes one URL, as a single argument, to the OS opener. No shell is
// involved; callers check doctor.AllowedLink first.
func openURL(u string) error {
	if !doctor.AllowedLink(u) {
		return fmt.Errorf("not opened: %s is not on the link allowlist", u)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// doctorInvocation is the parsed doctor flags.
type doctorInvocation struct {
	fix           doctorFixFlags
	opts          doctor.FixOptions
	json          bool
	noInteractive bool
}

// doctorDeps are the scans a doctor invocation can run.
type doctorDeps struct {
	scan     func(ctx context.Context) doctor.DoctorResult
	newFixer func() *doctor.Fixer
}

// runDoctorCommand dispatches one doctor invocation and returns its exit code.
func runDoctorCommand(ctx context.Context, t doctorTerm, inv doctorInvocation, deps doctorDeps) int {
	prompt := t.interactive && !inv.json && !inv.noInteractive
	if inv.fix.active() {
		opts := inv.opts
		if prompt && !opts.DryRun && !opts.Yes {
			// Interactive consent (ADR-025 § 3): each confirm remedy is shown
			// with its preview and applied only on an explicit yes. Auto
			// remedies are asked too, only so that Ctrl-C stops the pass
			// between remedies instead of in the middle of one.
			interrupted, stop := watchInterrupts(t)
			defer stop()
			keys := newKeyReader(t)
			opts.ConsentAuto = true
			opts.Consent = func(f doctor.Finding, r doctor.Remedy) bool {
				if interrupted.Load() {
					return false
				}
				if r.Kind == doctor.RemedyAuto {
					return true
				}
				yes, stopped := confirmOnTerminal(t, keys, f, r)
				if stopped {
					interrupted.Store(true)
				}
				return yes && !interrupted.Load()
			}
		}
		return runDoctorFix(ctx, t.out, deps.newFixer(), opts, inv.json)
	}
	if prompt {
		return runGuidedRepair(ctx, t, deps.newFixer())
	}
	result := deps.scan(ctx)
	if inv.json {
		data, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to encode JSON output: %v\n", err)
		} else {
			fmt.Fprintln(t.out, string(data))
		}
		return result.ExitCode
	}
	renderDoctorHuman(t.out, result, t.color)
	return result.ExitCode
}

// confirmOnTerminal is `--fix`'s consent prompt on a terminal: the preview
// first, then an explicit yes.
// stopped reports Ctrl-C or end of input: the rest of the pass is declined.
func confirmOnTerminal(t doctorTerm, keys *keyReader, f doctor.Finding, r doctor.Remedy) (yes, stopped bool) {
	st := newStyler(t)
	fmt.Fprintf(t.out, "\n%s %s %s  %s\n", st.label("CONFIRM"), f.Code, strings.ToUpper(string(f.Severity)), f.Title)
	st.wrapf("  remedy: ", r.Summary)
	st.wrapf("  preview: ", r.Preview)
	fmt.Fprint(t.out, "  Apply? [y]es [n]o > ")
	k, err := keys.key()
	if err != nil || k == keyInterrupt {
		fmt.Fprintln(t.out, "(stopped: remaining remedies are not applied)")
		return false, true
	}
	fmt.Fprintf(t.out, "%c\n", printableKey(k))
	return k == 'y', false
}

// watchInterrupts turns Ctrl-C into a flag the session reads between
// remedies, so an interrupt never lands in the middle of an apply. While
// stdin is raw, Ctrl-C arrives as a key instead and is handled as quit.
func watchInterrupts(t doctorTerm) (*atomic.Bool, func()) {
	flag := &atomic.Bool{}
	if t.interrupts == nil {
		return flag, func() {}
	}
	sig, stopSignals := t.interrupts()
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-sig:
				flag.Store(true)
			case <-quit:
				return
			}
		}
	}()
	return flag, func() {
		stopSignals()
		close(quit)
	}
}

// --- keys ---

const keyInterrupt = 0x03 // Ctrl-C arrives as a byte while stdin is raw

// keyReader reads one keystroke at a time. Whitespace, Enter and terminal
// escape sequences are ignored, so an arrow key is never read as a command.
type keyReader struct {
	r   *bufio.Reader
	raw func() (func(), error)
}

func newKeyReader(t doctorTerm) *keyReader {
	return &keyReader{r: bufio.NewReader(t.in), raw: t.raw}
}

func (k *keyReader) key() (byte, error) {
	if k.raw != nil {
		if restore, err := k.raw(); err == nil {
			defer restore()
		}
	}
	for {
		b, err := k.r.ReadByte()
		if err != nil {
			return 0, err
		}
		switch {
		case b == 0x1b:
			k.drainEscape()
			continue
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			continue
		case b >= 'A' && b <= 'Z':
			return b + ('a' - 'A'), nil
		}
		return b, nil
	}
}

// drainEscape discards the rest of an escape sequence that arrived with its
// ESC byte (terminals send a sequence in one write).
func (k *keyReader) drainEscape() {
	if k.r.Buffered() == 0 {
		return
	}
	b, _ := k.r.ReadByte()
	if b != '[' && b != 'O' {
		return
	}
	for k.r.Buffered() > 0 {
		c, _ := k.r.ReadByte()
		if c >= 0x40 && c <= 0x7e {
			return
		}
	}
}

func printableKey(k byte) byte {
	if k < 0x20 || k > 0x7e {
		return '?'
	}
	return k
}

// --- styling ---

// styler writes labelled, width-aware lines. Every state carries a text
// label; color only ever repeats it.
type styler struct {
	out   io.Writer
	color bool
	width int
}

func newStyler(t doctorTerm) styler {
	w := t.width
	if w <= 0 {
		w = 80
	}
	if w < 40 {
		w = 40
	}
	return styler{out: t.out, color: t.color, width: w}
}

var labelColors = map[string]string{
	"BLOCKER": "\x1b[31m", "WARNING": "\x1b[33m", "HOUSEKEEPING": "\x1b[36m", "INFO": "\x1b[2m",
	"OK": "\x1b[32m", "FIXED": "\x1b[32m", "FINDING": "\x1b[31m", "SKIPPED": "\x1b[2m",
	"TIMED OUT": "\x1b[33m", "STILL PRESENT": "\x1b[33m", "BLOCKED": "\x1b[31m", "CONFLICT": "\x1b[31m",
	"NOT ATTEMPTED": "\x1b[2m", "NEEDS YOU": "\x1b[35m", "CONFIRM": "\x1b[35m", "MANUAL": "\x1b[35m",
	"STALE": "\x1b[2m", "INCOMPLETE": "\x1b[33m",
}

// padLabel is label padded to n columns; the padding stays outside the color.
func (s styler) padLabel(l string, n int) string {
	pad := n - len(l)
	if pad < 0 {
		pad = 0
	}
	return s.label(l) + strings.Repeat(" ", pad)
}

func (s styler) label(l string) string {
	if c, ok := labelColors[l]; ok && s.color {
		return c + l + "\x1b[0m"
	}
	return l
}

// wrapf writes prefix+text word-wrapped to the terminal width, continuation
// lines indented under the text.
func (s styler) wrapf(prefix, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	for i, line := range wrapText(text, s.width-len(prefix)) {
		if i == 0 {
			fmt.Fprintf(s.out, "%s%s\n", prefix, line)
		} else {
			fmt.Fprintf(s.out, "%s%s\n", strings.Repeat(" ", len(prefix)), line)
		}
	}
}

// wrapText splits text into lines of at most width runes, breaking at spaces
// where it can and hard-breaking a word longer than the line.
func wrapText(text string, width int) []string {
	if width < 20 {
		width = 20
	}
	var lines []string
	for _, para := range strings.Split(text, "\n") {
		cur := ""
		for _, word := range strings.Fields(para) {
			for len([]rune(word)) > width {
				if cur != "" {
					lines = append(lines, cur)
					cur = ""
				}
				r := []rune(word)
				lines = append(lines, string(r[:width]))
				word = string(r[width:])
			}
			switch {
			case cur == "":
				cur = word
			case len([]rune(cur))+1+len([]rune(word)) <= width:
				cur += " " + word
			default:
				lines = append(lines, cur)
				cur = word
			}
		}
		if cur != "" || len(lines) == 0 {
			lines = append(lines, cur)
		}
	}
	return lines
}

// truncate shortens s to width runes with a trailing ellipsis.
func truncate(s string, width int) string {
	r := []rune(s)
	if len(r) <= width || width < 2 {
		return s
	}
	return string(r[:width-1]) + "…"
}

// --- the guided session ---

// walkState is how the session left one finding.
type walkState string

const (
	walkFixed        walkState = "FIXED"
	walkStill        walkState = "STILL PRESENT"
	walkSkipped      walkState = "SKIPPED"
	walkNotAttempted walkState = "NOT ATTEMPTED"
	walkNeedsYou     walkState = "NEEDS YOU"
)

type walkEntry struct {
	finding doctor.Finding
	state   walkState
	label   string // engine label when it differs from state (BLOCKED, CONFLICT, STALE)
	detail  string
}

type checkProgress struct {
	started  time.Time
	elapsed  time.Duration
	running  bool
	finished bool
	status   doctor.CheckStatus
}

type guidedSession struct {
	ctx  context.Context
	t    doctorTerm
	st   styler
	fx   *doctor.Fixer
	keys *keyReader

	// progress, guarded by mu (Observe runs on worker goroutines)
	mu         sync.Mutex
	checks     []doctor.Check
	progress   map[string]*checkProgress
	results    map[string]doctor.CheckResult
	done       int
	scanStart  time.Time
	spinStop   chan struct{}
	spinDone   chan struct{}
	statusLine bool

	summaryShown bool
	scan         doctor.DoctorResult // redacted scan, for the summary
	walkable     int
	position     int

	allSafe     bool
	quit        bool
	interrupted *atomic.Bool
	declined    map[string]walkState // fingerprint -> why Consent said no
	entries     []walkEntry
}

func runGuidedRepair(ctx context.Context, t doctorTerm, fx *doctor.Fixer) int {
	g := &guidedSession{
		ctx: ctx, t: t, st: newStyler(t), fx: fx, keys: newKeyReader(t),
		checks:   fx.Registry.Checks(),
		progress: map[string]*checkProgress{},
		results:  map[string]doctor.CheckResult{},
		declined: map[string]walkState{},
	}
	for _, c := range g.checks {
		g.progress[c.ID] = &checkProgress{}
	}
	interrupted, stop := watchInterrupts(t)
	defer stop()
	g.interrupted = interrupted

	fx.Runner.Observe = g.observe
	defer func() { fx.Runner.Observe = nil }()
	g.startProgress()
	rep := fx.Run(ctx, doctor.FixOptions{
		Consent:     g.consent,
		ConsentAuto: true,
		Progress:    g.onResult,
	})
	g.stopProgress()
	g.showSummary()
	g.finalReport(rep)
	return rep.ExitCode
}

// --- live progress ---

var spinnerFrames = []string{"|", "/", "-", "\\"}

func (g *guidedSession) startProgress() {
	g.scanStart = time.Now()
	groups := map[string]bool{}
	for _, c := range g.checks {
		groups[c.Group] = true
	}
	fmt.Fprintf(g.t.out, "nightgauge doctor: running %d checks in %d groups (all PENDING)\n", len(g.checks), len(groups))
	if !g.t.live {
		return
	}
	g.spinStop, g.spinDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(g.spinDone)
		tick := time.NewTicker(120 * time.Millisecond)
		defer tick.Stop()
		for i := 0; ; i++ {
			select {
			case <-g.spinStop:
				return
			case <-tick.C:
				g.mu.Lock()
				if g.done < len(g.checks) {
					g.drawStatus(spinnerFrames[i%len(spinnerFrames)])
				}
				g.mu.Unlock()
			}
		}
	}()
}

// drawStatus rewrites the one status line in place (live mode only). Caller
// holds mu.
func (g *guidedSession) drawStatus(frame string) {
	var running []string
	for _, c := range g.checks {
		if p := g.progress[c.ID]; p.running {
			running = append(running, c.ID)
		}
	}
	pending := len(g.checks) - g.done - len(running)
	line := fmt.Sprintf("%s RUNNING %d done, %d running, %d pending: %s",
		frame, g.done, len(running), pending, strings.Join(running, ", "))
	fmt.Fprintf(g.t.out, "\r\x1b[2K%s", truncate(line, g.st.width-1))
	g.statusLine = true
}

func (g *guidedSession) clearStatus() {
	if g.statusLine {
		fmt.Fprint(g.t.out, "\r\x1b[2K")
		g.statusLine = false
	}
}

func (g *guidedSession) stopProgress() {
	if g.spinStop != nil {
		close(g.spinStop)
		<-g.spinDone
		g.spinStop = nil
	}
	g.mu.Lock()
	g.clearStatus()
	g.mu.Unlock()
}

// observe receives the runner's check events.
func (g *guidedSession) observe(ev doctor.CheckEvent) {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.progress[ev.ID]
	if !ok {
		return
	}
	if ev.Running {
		p.running, p.started = true, time.Now()
		if g.t.live {
			g.drawStatus(spinnerFrames[0])
		}
		return
	}
	if ev.Result == nil || p.finished {
		return
	}
	p.running, p.finished, p.status = false, true, ev.Result.Status
	if !p.started.IsZero() {
		p.elapsed = time.Since(p.started)
	}
	g.results[ev.ID] = *ev.Result
	g.done++
	if !g.t.live {
		// One line per state change: screen readers and dumb terminals get
		// no redraw (ADR-025 § 6).
		fmt.Fprintf(g.t.out, "  [%d/%d] %s %s\n", g.done, len(g.checks), g.st.label(checkLabel(p.status)), ev.ID)
	}
}

func checkLabel(s doctor.CheckStatus) string {
	switch s {
	case doctor.StatusPassed:
		return "OK"
	case doctor.StatusFailed:
		return "FINDING"
	case doctor.StatusSkipped:
		return "SKIPPED"
	case doctor.StatusTimeout:
		return "TIMED OUT"
	}
	return strings.ToUpper(string(s))
}

// --- summary ---

// showSummary prints, once, the grouped check table, the wall time and the
// severity-grouped findings. The engine calls Consent or Progress only after
// the scan, so the first callback is the moment the scan is complete.
func (g *guidedSession) showSummary() {
	if g.summaryShown {
		return
	}
	g.summaryShown = true
	if g.spinStop != nil {
		g.stopProgress()
	}
	g.mu.Lock()
	wall := time.Since(g.scanStart)
	var raw []doctor.CheckResult
	for _, c := range g.checks {
		if r, ok := g.results[c.ID]; ok {
			raw = append(raw, r)
		}
	}
	var groups []string
	byGroup := map[string][]doctor.Check{}
	for _, c := range g.checks {
		if _, seen := byGroup[c.Group]; !seen {
			groups = append(groups, c.Group)
		}
		byGroup[c.Group] = append(byGroup[c.Group], c)
	}
	width := 0
	for _, c := range g.checks {
		if len(c.ID) > width {
			width = len(c.ID)
		}
	}
	out := g.t.out
	fmt.Fprintln(out, "\nChecks:")
	for _, grp := range groups {
		name := grp
		if name == "" {
			name = "other"
		}
		fmt.Fprintf(out, "%s\n", name)
		for _, c := range byGroup[grp] {
			p := g.progress[c.ID]
			label := "PENDING"
			if p.finished {
				label = checkLabel(p.status)
			}
			fmt.Fprintf(out, "  %s  %-*s  %s\n", g.st.padLabel(label, 9), width, c.ID, p.elapsed.Round(10*time.Millisecond))
		}
	}
	fmt.Fprintf(out, "%d checks in %s wall time\n", len(g.checks), wall.Round(10*time.Millisecond))
	g.mu.Unlock()

	g.scan = doctor.BuildResult(raw)
	s := g.scan.Summary
	fmt.Fprintf(out, "\nSummary: %d blocker, %d warning, %d housekeeping, %d info\n",
		s.Blocker, s.Warning, s.Housekeeping, s.Info)
	for _, f := range g.scan.Findings {
		if len(f.Remedies) > 0 {
			g.walkable++
		}
		if f.Severity != doctor.SeverityBlocker && f.Severity != doctor.SeverityWarning {
			continue
		}
		fmt.Fprintf(out, "  %s %s  %s\n", g.st.label(severityLabel(f.Severity)), f.Code, truncate(f.Title, g.st.width-len(f.Code)-12))
		if c := firstLineOf(f.Cause); c != "" {
			fmt.Fprintf(out, "      cause: %s\n", truncate(c, g.st.width-13))
		}
	}
	if s.Housekeeping > 0 {
		g.st.wrapf("  "+g.st.label("HOUSEKEEPING")+" ", fmt.Sprintf("%d item(s): %s. Press h at a prompt to list them.",
			s.Housekeeping, codeTally(g.scan.Findings, doctor.SeverityHousekeeping)))
	}
	if s.Info > 0 {
		g.st.wrapf("  "+g.st.label("INFO")+" ", fmt.Sprintf("%d note(s), context only; `nightgauge doctor --no-interactive` lists them.", s.Info))
	}
	if g.interrupted.Load() {
		fmt.Fprintln(out, "\nInterrupted: stopping before any repair.")
		g.quit = true
	} else if g.walkable > 0 {
		fmt.Fprintf(out, "\n%d finding(s) declare a remedy. Walking through them, most severe first.\n", g.walkable)
	}
}

func severityLabel(s doctor.Severity) string { return strings.ToUpper(string(s)) }

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// reportListLimit is how many findings one report state lists one per line.
const reportListLimit = 8

// codeTally is "NGD017 x3, NGD018 x1" for one severity.
func codeTally(fs []doctor.Finding, sev doctor.Severity) string {
	var of []doctor.Finding
	for _, f := range fs {
		if f.Severity == sev {
			of = append(of, f)
		}
	}
	return codeTallyAll(of)
}

// codeTallyAll is "NGD017 x3, NGD018 x1" in first-seen order.
func codeTallyAll(fs []doctor.Finding) string {
	count := map[string]int{}
	var order []string
	for _, f := range fs {
		if count[f.Code] == 0 {
			order = append(order, f.Code)
		}
		count[f.Code]++
	}
	parts := make([]string, len(order))
	for i, c := range order {
		parts[i] = fmt.Sprintf("%s x%d", c, count[c])
	}
	return strings.Join(parts, ", ")
}

func (g *guidedSession) listHousekeeping() {
	fmt.Fprintln(g.t.out, "  Housekeeping:")
	for _, f := range g.scan.Findings {
		if f.Severity == doctor.SeverityHousekeeping {
			fmt.Fprintf(g.t.out, "    %s  %s\n", f.Code, truncate(f.Title, g.st.width-14))
		}
	}
}

// --- the walk ---

func (g *guidedSession) stopping() bool {
	if g.interrupted.Load() && !g.quit {
		fmt.Fprintln(g.t.out, "\nInterrupted: the last remedy finished; stopping here.")
		g.quit = true
	}
	return g.quit
}

func (g *guidedSession) header(f doctor.Finding) {
	g.position++
	head := fmt.Sprintf("[%d/%d] %s %s  ", g.position, g.walkable, severityLabel(f.Severity), f.Code)
	fmt.Fprintf(g.t.out, "\n[%d/%d] %s %s  %s\n", g.position, g.walkable,
		g.st.label(severityLabel(f.Severity)), f.Code, truncate(f.Title, g.st.width-len(head)))
	g.st.wrapf("  cause: ", f.Cause)
}

// consent is the engine's question for one auto or confirm remedy. It
// returns true to apply; the engine then verifies and calls onResult.
func (g *guidedSession) consent(f doctor.Finding, r doctor.Remedy) bool {
	g.showSummary()
	if g.stopping() {
		g.declined[f.Fingerprint] = walkNotAttempted
		return false
	}
	if g.allSafe && r.Kind == doctor.RemedyAuto {
		g.header(f)
		g.st.wrapf("  applying (all safe): ", r.Summary)
		return true
	}
	g.header(f)
	g.st.wrapf(fmt.Sprintf("  remedy (%s): ", r.Kind), r.Summary)
	for {
		fmt.Fprint(g.t.out, g.promptLine("[f]ix [s]kip [d]etails [o]pen [a]ll safe [q]uit"))
		k, err := g.keys.key()
		g.echo(k, err)
		if err != nil || k == keyInterrupt {
			g.quit = true
			g.declined[f.Fingerprint] = walkNotAttempted
			return false
		}
		switch k {
		case 'f':
			if r.Kind == doctor.RemedyConfirm {
				// A confirm remedy is destructive or visible to others: the
				// preview comes first, then an explicit yes.
				g.st.wrapf("  preview: ", r.Preview)
				fmt.Fprint(g.t.out, "  Apply this confirm remedy? [y]es [n]o > ")
				y, yerr := g.keys.key()
				g.echo(y, yerr)
				if yerr != nil || y == keyInterrupt {
					g.quit = true
					g.declined[f.Fingerprint] = walkNotAttempted
					return false
				}
				if y != 'y' {
					continue
				}
			} else {
				g.st.wrapf("  applying: ", r.Preview)
			}
			return true
		case 's':
			g.declined[f.Fingerprint] = walkSkipped
			return false
		case 'd':
			g.details(f, r)
		case 'o':
			g.openLinks(f)
		case 'a':
			g.allSafe = true
			if r.Kind == doctor.RemedyAuto {
				g.st.wrapf("  applying: ", r.Preview)
				return true
			}
			g.st.wrapf("  ", "All safe: remaining auto remedies apply without asking; this confirm remedy still needs your answer.")
		case 'q':
			g.quit = true
			g.declined[f.Fingerprint] = walkNotAttempted
			return false
		case 'h':
			g.listHousekeeping()
		default:
			g.st.wrapf("  ", "Keys: f fix, s skip, d details, o open links, a apply every safe remedy, q quit, h list housekeeping")
		}
	}
}

func (g *guidedSession) promptLine(keys string) string {
	if g.scan.Summary.Housekeeping > 0 {
		keys += " [h]ousekeeping"
	}
	return "  " + keys + " > "
}

func (g *guidedSession) echo(k byte, err error) {
	if err != nil {
		fmt.Fprintln(g.t.out)
		return
	}
	if k == keyInterrupt {
		fmt.Fprintln(g.t.out, "^C")
		return
	}
	fmt.Fprintf(g.t.out, "%c\n", printableKey(k))
}

// details prints the finding's evidence, cause, remedy and docs link.
func (g *guidedSession) details(f doctor.Finding, r doctor.Remedy) {
	g.st.wrapf("  check: ", f.Check+"  fingerprint: "+f.Fingerprint)
	g.st.wrapf("  cause: ", f.Cause)
	keys := make([]string, 0, len(f.Evidence))
	for k := range f.Evidence {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g.st.wrapf("  "+k+": ", f.Evidence[k])
	}
	if r.Verb != "" {
		g.st.wrapf("  verb: ", fmt.Sprintf("%s  reversible: %t  verified by: %s", r.Verb, r.Reversible, r.Verify))
	}
	g.st.wrapf("  preview: ", r.Preview)
	if u := doctor.DocsURL(f.Docs); u != "" {
		g.st.wrapf("  docs: ", u)
	}
}

// openLinks opens the finding's allowlisted remedy links with the OS opener,
// one URL as one argument; any other link is printed and not opened.
func (g *guidedSession) openLinks(f doctor.Finding) {
	var links []string
	for _, r := range f.Remedies {
		links = append(links, r.Links...)
	}
	if len(links) == 0 {
		fmt.Fprintln(g.t.out, "  No links to open for this finding.")
		return
	}
	for _, u := range links {
		if !doctor.AllowedLink(u) {
			g.st.wrapf("  not opened (host not on the allowlist): ", u)
			continue
		}
		if g.t.open == nil {
			g.st.wrapf("  open: ", u)
			continue
		}
		if err := g.t.open(u); err != nil {
			g.st.wrapf("  could not open: ", u+": "+err.Error())
			continue
		}
		g.st.wrapf("  opened: ", u)
	}
}

// onResult is the engine's report of one decided finding.
func (g *guidedSession) onResult(res doctor.FixResult) {
	g.showSummary()
	f := res.Finding
	switch res.Action {
	case doctor.ActionNoRemedy:
		if f.Severity == doctor.SeverityBlocker || f.Severity == doctor.SeverityWarning {
			g.record(f, walkNeedsYou, "", "no remedy is registered; see "+doctor.DocsURL(f.Docs))
		}
	case doctor.ActionAwaitingConsent:
		state := g.declined[f.Fingerprint]
		if state == "" {
			state = walkSkipped
		}
		g.record(f, state, "", "")
	case doctor.ActionManual:
		g.manual(res)
	case doctor.ActionApplied:
		entry := walkEntry{finding: f, detail: res.Detail}
		switch res.Outcome {
		case doctor.OutcomeFixed:
			entry.state = walkFixed
		case doctor.OutcomeStillPresent:
			entry.state = walkStill
		default:
			entry.state, entry.label = walkStill, fixLabel(res)
		}
		g.entries = append(g.entries, entry)
		label := string(entry.state)
		if entry.label != "" {
			label = entry.label
		}
		g.st.wrapf("  "+g.st.label(label)+" ", res.Detail)
		if len(res.Evidence) > 0 {
			g.st.wrapf("  evidence now: ", evidenceLine(res.Evidence))
		}
	}
}

func (g *guidedSession) record(f doctor.Finding, s walkState, label, detail string) {
	g.entries = append(g.entries, walkEntry{finding: f, state: s, label: label, detail: detail})
}

// manual walks a manual remedy: numbered steps, then [c]heck again re-runs
// only the owning check until the finding is gone or the operator moves on.
func (g *guidedSession) manual(res doctor.FixResult) {
	f := res.Finding
	if g.stopping() {
		g.record(f, walkNotAttempted, "", "")
		return
	}
	var rem doctor.Remedy
	if res.Remedy != nil {
		rem = *res.Remedy
	}
	g.header(f)
	g.st.wrapf("  "+g.st.label("MANUAL")+" remedy: ", rem.Summary)
	for i, s := range rem.Steps {
		g.st.wrapf(fmt.Sprintf("    %d. ", i+1), s)
	}
	for _, u := range rem.Links {
		if doctor.AllowedLink(u) {
			g.st.wrapf("    link: ", u)
		} else {
			g.st.wrapf("    link (not opened, host not on the allowlist): ", u)
		}
	}
	verify := rem.Verify
	if verify == "" {
		verify = f.Check
	}
	for {
		fmt.Fprint(g.t.out, g.promptLine("[c]heck again [o]pen [d]etails [s]kip [q]uit"))
		k, err := g.keys.key()
		g.echo(k, err)
		if err != nil || k == keyInterrupt {
			g.quit = true
			g.record(f, walkNotAttempted, "", "")
			return
		}
		switch k {
		case 'c':
			if state, detail, done := g.checkAgain(verify, f.Fingerprint); done {
				g.record(f, state, "", detail)
				return
			}
		case 'o':
			g.openLinks(f)
		case 'd':
			g.details(f, rem)
		case 's':
			g.record(f, walkNeedsYou, "", "skipped; the steps above still apply")
			return
		case 'q':
			g.quit = true
			g.record(f, walkNeedsYou, "", "not re-checked; the steps above still apply")
			return
		case 'h':
			g.listHousekeeping()
		default:
			g.st.wrapf("  ", "Keys: c re-run this check, o open links, d details, s skip, q quit, h list housekeeping")
		}
	}
}

// checkAgain re-runs one check. done is true once the fingerprint is gone.
func (g *guidedSession) checkAgain(checkID, fingerprint string) (walkState, string, bool) {
	g.st.wrapf("  re-running ", checkID+" ...")
	after, ok := g.fx.Recheck(g.ctx, checkID)
	switch {
	case !ok:
		g.st.wrapf("  "+g.st.label("INCOMPLETE")+" ", fmt.Sprintf("check %q is not registered", checkID))
	case after.Status == doctor.StatusTimeout || after.Status == doctor.StatusSkipped:
		g.st.wrapf("  "+g.st.label("INCOMPLETE")+" ", fmt.Sprintf("%s did not complete (%s); try again", checkID, after.Status))
	default:
		for _, nf := range after.Findings {
			if nf.Fingerprint == fingerprint {
				g.st.wrapf("  "+g.st.label("STILL PRESENT")+" ", checkID+" still reports it; finish the steps, then press c")
				return "", "", false
			}
		}
		detail := fmt.Sprintf("verified: %s no longer reports it", checkID)
		g.st.wrapf("  "+g.st.label("FIXED")+" ", detail)
		return walkFixed, detail, true
	}
	return "", "", false
}

// --- final report ---

func (g *guidedSession) finalReport(rep doctor.FixReport) {
	out := g.t.out
	before := g.scan.Summary
	after := rep.Doctor.Summary
	if len(g.entries) > 0 {
		fmt.Fprintln(out, "\nRepair report:")
		for _, state := range []walkState{walkFixed, walkStill, walkSkipped, walkNotAttempted, walkNeedsYou} {
			var group []walkEntry
			for _, e := range g.entries {
				if e.state == state {
					group = append(group, e)
				}
			}
			if len(group) > reportListLimit {
				// A long tail (typically housekeeping after [q]) collapses to
				// one tally line so the report stays readable.
				fs := make([]doctor.Finding, len(group))
				for i, e := range group {
					fs[i] = e.finding
				}
				g.st.wrapf("  "+g.st.padLabel(string(state), 13)+"  ", fmt.Sprintf("%d findings: %s", len(group), codeTallyAll(fs)))
				continue
			}
			for _, e := range group {
				label := string(e.state)
				if e.label != "" {
					label = e.label
				}
				fmt.Fprintf(out, "  %s  %s  %s\n", g.st.padLabel(label, 13), e.finding.Code,
					truncate(e.finding.Title, g.st.width-max(len(label), 13)-len(e.finding.Code)-6))
				if e.detail != "" && e.state != walkFixed {
					g.st.wrapf("      ", e.detail)
				}
			}
		}
		n := map[walkState]int{}
		for _, e := range g.entries {
			n[e.state]++
		}
		fmt.Fprintf(out, "%d fixed, %d still present, %d skipped, %d not attempted, %d need you\n",
			n[walkFixed], n[walkStill], n[walkSkipped], n[walkNotAttempted], n[walkNeedsYou])
	} else if before.Blocker+before.Warning+before.Housekeeping == 0 {
		fmt.Fprintln(out, "\nNothing to repair.")
	}
	fmt.Fprintf(out, "Before: %d blocker, %d warning, %d housekeeping\n", before.Blocker, before.Warning, before.Housekeeping)
	fmt.Fprintf(out, "After:  %d blocker, %d warning, %d housekeeping (exit %d)\n",
		after.Blocker, after.Warning, after.Housekeeping, rep.ExitCode)
}
