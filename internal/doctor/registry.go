package doctor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// DefaultCheckTimeout bounds a check that declares no Timeout of its own.
const DefaultCheckTimeout = 10 * time.Second

// DefaultWorkers is the runner's worker-pool size.
const DefaultWorkers = 8

// Check is one registered doctor check (ADR-025). Run returns zero findings
// when the check passes.
type Check struct {
	ID    string // registry ID, e.g. "worktree_leaks"; the Finding.Check value
	Title string // one-line human name
	Group string // presentation group, e.g. "github", "hygiene"
	// Code is the check's primary finding code, used for the info finding a
	// skipped check reports.
	Code      string
	Timeout   time.Duration // 0 means DefaultCheckTimeout
	DependsOn []string      // IDs of checks that must pass before this one runs
	Run       func(ctx context.Context, env *Env) []Finding
}

// Registry is an ordered set of checks. Registration order is render order.
type Registry struct {
	checks []Check
	index  map[string]int
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{index: map[string]int{}}
}

// Register adds c. A dependency must already be registered, which keeps the
// dependency graph acyclic by construction.
func (r *Registry) Register(c Check) error {
	if c.ID == "" || c.Run == nil {
		return fmt.Errorf("doctor: check needs an ID and a Run function")
	}
	if _, dup := r.index[c.ID]; dup {
		return fmt.Errorf("doctor: check %q registered twice", c.ID)
	}
	for _, dep := range c.DependsOn {
		if _, ok := r.index[dep]; !ok {
			return fmt.Errorf("doctor: check %q depends on unregistered check %q", c.ID, dep)
		}
	}
	r.index[c.ID] = len(r.checks)
	r.checks = append(r.checks, c)
	return nil
}

// MustRegister is Register for static registration; it panics on error.
func (r *Registry) MustRegister(c Check) {
	if err := r.Register(c); err != nil {
		panic(err)
	}
}

// Checks returns the registered checks in registration order.
func (r *Registry) Checks() []Check {
	return append([]Check(nil), r.checks...)
}

// IDs returns the registered check IDs in registration order.
func (r *Registry) IDs() []string {
	ids := make([]string, len(r.checks))
	for i, c := range r.checks {
		ids[i] = c.ID
	}
	return ids
}

// CheckStatus is the outcome of one check in a run.
type CheckStatus string

const (
	StatusPassed  CheckStatus = "passed"  // no findings
	StatusFailed  CheckStatus = "failed"  // at least one finding
	StatusSkipped CheckStatus = "skipped" // a dependency did not pass
	StatusTimeout CheckStatus = "timeout" // did not complete (NGD000)
)

// CheckResult is one check's outcome, in registry order.
type CheckResult struct {
	ID       string
	Title    string
	Group    string
	Status   CheckStatus
	Detail   string // human detail the check recorded via Env.SetDetail
	Findings []Finding
}

// blocksDependents reports whether dependents of this result must be skipped:
// the check was skipped, could not complete, or raised a blocker.
func (r CheckResult) blocksDependents() bool {
	if r.Status == StatusSkipped || r.Status == StatusTimeout {
		return true
	}
	for _, f := range r.Findings {
		if f.Severity == SeverityBlocker {
			return true
		}
	}
	return false
}

// Env is the input every check reads. Checks run concurrently, so every
// mutable member is guarded.
type Env struct {
	Cfg      *config.Config
	CfgErr   error
	Client   *gh.Client
	Cwd      string
	Now      time.Time
	Adapters []string

	mu      sync.Mutex
	details map[string]string
	items   map[string]CheckItem
	install string
	adapter []AdapterHealth

	scopesOnce sync.Once
	scopes     *gh.TokenScopeInfo
	scopesErr  error
	rlOnce     sync.Once
	rl         *gh.RateLimitInfo
	rlErr      error
}

// SetDetail records the human-readable detail for a check (shown for a
// passing check, where there is no finding to carry it).
func (e *Env) SetDetail(checkID, detail string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.details == nil {
		e.details = map[string]string{}
	}
	e.details[checkID] = detail
}

func (e *Env) detail(checkID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.details[checkID]
}

// tokenScopes memoizes the token-scope probe that github_auth, api_user and
// scopes all read.
func (e *Env) tokenScopes(ctx context.Context) (*gh.TokenScopeInfo, error) {
	e.scopesOnce.Do(func() {
		e.scopes, e.scopesErr = e.Client.CheckTokenScopes(ctx)
	})
	return e.scopes, e.scopesErr
}

// rateLimit memoizes the rate-limit probe rate_limit and github_identity read.
func (e *Env) rateLimit(ctx context.Context) (*gh.RateLimitInfo, error) {
	e.rlOnce.Do(func() {
		e.rl, e.rlErr = e.Client.GetRateLimit(ctx)
	})
	return e.rl, e.rlErr
}

// Runner executes a registry with a bounded worker pool. Independent checks
// run concurrently; each gets its own deadline.
type Runner struct {
	Workers        int           // 0 means DefaultWorkers
	DefaultTimeout time.Duration // 0 means DefaultCheckTimeout
	// Progress, when set, receives each check's start and outcome in
	// registration order (progress.go).
	Progress func(CheckProgress)
}

type completion struct {
	idx    int
	result CheckResult
}

// Run executes every check in reg and returns one result per check, in
// registration order. Every check context is cancelled before Run returns.
func (r Runner) Run(ctx context.Context, reg *Registry, env *Env) []CheckResult {
	workers := r.Workers
	if workers <= 0 {
		workers = DefaultWorkers
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	checks := reg.checks
	n := len(checks)
	results := make([]CheckResult, n)
	if n == 0 {
		return results
	}
	order := newProgressOrder(r.Progress, checks)

	remaining := make([]int, n)    // unfinished dependencies per check
	dependents := make([][]int, n) // reverse edges
	for i, c := range checks {
		remaining[i] = len(c.DependsOn)
		for _, dep := range c.DependsOn {
			d := reg.index[dep]
			dependents[d] = append(dependents[d], i)
		}
	}

	jobs := make(chan int, n)
	done := make(chan completion, n)
	var wg sync.WaitGroup
	for w := 0; w < workers && w < n; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				order.start(idx)
				done <- completion{idx: idx, result: r.runOne(ctx, checks[idx], env)}
			}
		}()
	}

	for i := range checks {
		if remaining[i] == 0 {
			jobs <- i
		}
	}

	// finish records a result and releases its dependents. A dependent whose
	// dependency did not pass is skipped here, without occupying a worker.
	finished := 0
	var finish func(idx int, res CheckResult)
	finish = func(idx int, res CheckResult) {
		results[idx] = res
		order.finish(idx, res)
		finished++
		for _, dep := range dependents[idx] {
			remaining[dep]--
			if remaining[dep] > 0 {
				continue
			}
			if blocker := firstBlockingDependency(checks[dep], results, reg); blocker != "" {
				finish(dep, skippedResult(checks[dep], blocker))
				continue
			}
			jobs <- dep
		}
	}
	for finished < n {
		c := <-done
		finish(c.idx, c.result)
	}
	close(jobs)
	wg.Wait()
	return results
}

// firstBlockingDependency returns the first dependency of c whose result
// blocks dependents, or "".
func firstBlockingDependency(c Check, results []CheckResult, reg *Registry) string {
	for _, dep := range c.DependsOn {
		if results[reg.index[dep]].blocksDependents() {
			return dep
		}
	}
	return ""
}

// skippedResult is a check that never ran because dep did not pass. It
// carries an info finding so a skipped check never reads as healthy.
func skippedResult(c Check, dep string) CheckResult {
	return CheckResult{
		ID: c.ID, Title: c.Title, Group: c.Group, Status: StatusSkipped,
		Findings: []Finding{{
			Code:        c.Code,
			Check:       c.ID,
			Severity:    SeverityInfo,
			Title:       fmt.Sprintf("%s skipped: dependency %s did not pass", c.ID, dep),
			Cause:       fmt.Sprintf("%s needs %s to pass first", c.ID, dep),
			Evidence:    map[string]string{"depends_on": dep},
			Docs:        DocsAnchor(c.Code),
			Fingerprint: Fingerprint(c.Code, c.ID, "skipped", dep),
			Remedies:    []Remedy{},
		}},
	}
}

// runOne runs c under its own deadline. A check that overruns it, or panics,
// yields an NGD000 warning instead of hanging or crashing the command.
func (r Runner) runOne(ctx context.Context, c Check, env *Env) CheckResult {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = r.DefaultTimeout
	}
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type outcome struct {
		findings []Finding
		panicked any
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- outcome{panicked: p}
			}
		}()
		ch <- outcome{findings: c.Run(cctx, env)}
	}()

	res := CheckResult{ID: c.ID, Title: c.Title, Group: c.Group}
	select {
	case o := <-ch:
		if o.panicked != nil {
			res.Status = StatusTimeout
			res.Findings = []Finding{incompleteFinding(c, "internal error", fmt.Sprint(o.panicked))}
			return res
		}
		res.Findings = o.findings
		res.Detail = env.detail(c.ID)
		if len(res.Findings) == 0 {
			res.Status = StatusPassed
		} else {
			res.Status = StatusFailed
		}
	case <-cctx.Done():
		res.Status = StatusTimeout
		reason := "timed out after " + timeout.String()
		if ctx.Err() != nil {
			reason = "cancelled"
		}
		res.Findings = []Finding{incompleteFinding(c, reason, "")}
	}
	return res
}

// incompleteFinding is NGD000: the check could not complete.
func incompleteFinding(c Check, reason, detail string) Finding {
	ev := map[string]string{"reason": reason}
	if detail != "" {
		ev["detail"] = detail
	}
	return Finding{
		Code:        codeTimeout,
		Check:       c.ID,
		Severity:    SeverityWarning,
		Title:       fmt.Sprintf("check %s could not complete: %s", c.ID, reason),
		Cause:       "the check did not return a result, so its condition is unknown",
		Evidence:    ev,
		Docs:        DocsAnchor(codeTimeout),
		Fingerprint: Fingerprint(codeTimeout, c.ID),
		Remedies:    []Remedy{},
	}
}
