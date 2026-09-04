package orchestrator

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	pmstages "github.com/nightgauge/nightgauge/internal/orchestrator/stages"
)

// PhaseStreamPrefix marks a live phase-transition line on the CLI's stderr.
//
// stdout carries exactly one thing — the stage's JSON result — and that
// contract predates this and is depended on by the extension's parser, so the
// live stream goes to stderr and is sentinel-prefixed. A consumer treats
// prefixed lines as phase events and everything else as ordinary log output.
const PhaseStreamPrefix = "@@nightgauge-phase@@"

// PhaseTransition is one phase transition a deterministic runner reported.
//
// It is the wire shape of `nightgauge pr-stage --json`'s `phases` array, so the
// JSON tags are a published contract the extension parses (#1397).
type PhaseTransition struct {
	Stage string `json:"stage"`
	Name  string `json:"name"`
	Index int    `json:"index"`
	Total int    `json:"total"`
	// Status is the transition, not a summary: "running" when the phase
	// started, then one of "complete" / "failed" / "skipped" when it settled.
	// The starts are carried deliberately — a consumer replaying only terminal
	// states can rebuild the durable record but cannot reproduce the LIVE
	// progression, which is the half that was showing 0/14.
	Status string `json:"status"`
}

// PhaseRecorder is a stages.PhaseReporter that accumulates transitions instead
// of writing them to a running pipeline (#1397).
//
// WHY THIS EXISTS. #1247 gave the deterministic pr-merge / pr-create runners
// real phase reporting, but only the Go scheduler attaches a reporter. The
// extension reaches the same runners a second way — HeadlessOrchestrator shells
// out to `nightgauge pr-stage create|merge --json` in a SEPARATE PROCESS — and
// nothing there attached one, so that route still showed 0/14 while it ran.
// That is the dual-path-drift class: two routes to one stage, one instrumented
// and one not, and the uninstrumented one is the route an ordinary VS Code user
// takes.
//
// A separate process cannot share the scheduler's RuntimeState or its live IPC
// callbacks, so the CLI records what the runner reported and returns it in its
// result for the caller to replay. deterministicPhaseReporter (the in-process
// sibling) writes the same transitions straight to both sinks;
// TestPhaseRecorderMatchesTheSchedulerReporter pins that the two agree for the
// same run rather than merely that each produces something.
//
// Safe for concurrent use: the runners are single-goroutine today, but this is
// handed to arbitrary runner code and a lock is cheaper than the assumption.
type PhaseRecorder struct {
	mu          sync.Mutex
	transitions []PhaseTransition
	// stream, when non-nil, receives every transition AS IT HAPPENS.
	//
	// The accumulated array alone does not fix the reported defect. The
	// deterministic pr-merge waits out in-flight CI on a 30s x 30 budget, so a
	// caller that only sees phases when the process EXITS sits at 0/14 for up
	// to fifteen minutes and then jumps to the end — which is the symptom
	// ("still shows 0/14 while it runs"), not a fix for it. The array remains
	// the durable authority; this is what makes the count move.
	stream io.Writer
}

// NewPhaseRecorder returns a recorder that only accumulates.
func NewPhaseRecorder() *PhaseRecorder { return &PhaseRecorder{} }

// NewStreamingPhaseRecorder returns a recorder that also writes each transition
// to w as a sentinel-prefixed JSON line the moment it is reported.
//
// A write failure is deliberately ignored: a broken pipe on the progress
// channel must never fail the merge that was reporting progress.
func NewStreamingPhaseRecorder(w io.Writer) *PhaseRecorder { return &PhaseRecorder{stream: w} }

func (p *PhaseRecorder) append(stage, name string, index, total int, status string) {
	t := PhaseTransition{Stage: stage, Name: name, Index: index, Total: total, Status: status}

	p.mu.Lock()
	p.transitions = append(p.transitions, t)
	w := p.stream
	p.mu.Unlock()

	if w == nil {
		return
	}
	// Marshalling a struct of strings and ints cannot fail; ignore rather than
	// grow an error path that can never be taken.
	if line, err := json.Marshal(t); err == nil {
		fmt.Fprintf(w, "%s%s\n", PhaseStreamPrefix, line)
	}
}

func (p *PhaseRecorder) PhaseStart(stage, name string, index, total int) {
	p.append(stage, name, index, total, "running")
}

// PhaseComplete carries no position, because the runner's complete() call does
// not. The scheduler's reporter re-resolves it from the registry rather than
// publishing -1, and this does the same so the two records are comparable.
func (p *PhaseRecorder) PhaseComplete(stage, name string) {
	idx, total := pmstages.PhasePosition(stage, name)
	p.append(stage, name, idx, total, "complete")
}

func (p *PhaseRecorder) PhaseFail(stage, name string, index, total int) {
	p.append(stage, name, index, total, "failed")
}

func (p *PhaseRecorder) PhaseSkip(stage, name string, index, total int) {
	p.append(stage, name, index, total, "skipped")
}

// Transitions returns a copy of what was recorded, in report order.
func (p *PhaseRecorder) Transitions() []PhaseTransition {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PhaseTransition, len(p.transitions))
	copy(out, p.transitions)
	return out
}
