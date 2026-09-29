package doctor

import "sync"

// Streamed check progress. The IPC `doctor.run` method forwards each event as
// a `doctor.progress` notification so the VS Code Doctor panel can draw rows
// while the scan runs.
//
// Checks run concurrently, so events are re-sequenced into registration
// order before delivery: a check's `started` is delivered once every earlier
// check has been reported, and its terminal event follows once it finishes.
// The stream is therefore deterministic, and a consumer can treat it as "the
// next row is now running" without reordering anything itself.

// ProgressPhase is one step of a check's progress.
type ProgressPhase string

const (
	ProgressStarted  ProgressPhase = "started"  // the check is running
	ProgressFinished ProgressPhase = "finished" // it completed: passed or failed
	ProgressSkipped  ProgressPhase = "skipped"  // a dependency did not pass; it never ran
	ProgressTimedOut ProgressPhase = "timeout"  // it could not complete (NGD000)
)

// CheckProgress is one progress event. Status and Findings are set on
// terminal phases only.
type CheckProgress struct {
	Index    int           `json:"index"` // registration position, 0-based
	Total    int           `json:"total"` // checks in the run
	Check    string        `json:"check"`
	Title    string        `json:"title"`
	Phase    ProgressPhase `json:"phase"`
	Status   CheckStatus   `json:"status,omitempty"`
	Findings int           `json:"findings"`
}

// progressOrder re-sequences concurrent start and finish signals into
// registration order. A nil callback makes every method a no-op.
type progressOrder struct {
	mu      sync.Mutex
	fn      func(CheckProgress)
	checks  []Check
	started []bool
	done    []*CheckResult
	head    int  // the first check not yet fully reported
	headOn  bool // whether head's `started` has been delivered
}

func newProgressOrder(fn func(CheckProgress), checks []Check) *progressOrder {
	if fn == nil {
		return nil
	}
	return &progressOrder{
		fn:      fn,
		checks:  checks,
		started: make([]bool, len(checks)),
		done:    make([]*CheckResult, len(checks)),
	}
}

// start records that check idx began running.
func (p *progressOrder) start(idx int) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started[idx] = true
	p.flush()
}

// finish records check idx's result. A skipped check finishes without ever
// starting.
func (p *progressOrder) finish(idx int, res CheckResult) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	r := res
	p.done[idx] = &r
	p.flush()
}

// flush delivers every event the head of the order allows. It runs under
// p.mu, so the callback is never called concurrently.
func (p *progressOrder) flush() {
	total := len(p.checks)
	for p.head < total {
		c := p.checks[p.head]
		if !p.headOn && p.started[p.head] {
			p.fn(CheckProgress{Index: p.head, Total: total, Check: c.ID, Title: c.Title, Phase: ProgressStarted})
			p.headOn = true
		}
		res := p.done[p.head]
		if res == nil {
			return
		}
		phase := ProgressFinished
		switch res.Status {
		case StatusSkipped:
			phase = ProgressSkipped
		case StatusTimeout:
			phase = ProgressTimedOut
		}
		p.fn(CheckProgress{
			Index: p.head, Total: total, Check: c.ID, Title: c.Title,
			Phase: phase, Status: res.Status, Findings: len(res.Findings),
		})
		p.head++
		p.headOn = false
	}
}
