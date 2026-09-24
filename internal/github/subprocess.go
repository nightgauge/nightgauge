package github

// Instrumenting the `gh` CLI (#1913).
//
// The API ledger records what the Go transport sees, which is everything
// EXCEPT the traffic this binary generates by shelling out to `gh` — post
// condition gates, recovery actions, non-terminal reconcile, survival
// detection and the PR-merge stage all did, unrecorded and ungated. Measured
// over one workspace's whole ledger, 77% of the GraphQL points consumed had
// no nightgauge record within five seconds of the drop: spend that could not
// be attributed after an exhaustion, and could not be slowed before one.
//
// A subprocess cannot be instrumented at the transport, so it is instrumented
// at the only two points we own: the gate before it runs, and one free
// GET /rate_limit after it, which is how the record gets an honest
// `remaining` and an honest `cost`.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ghRateLimitURL is the REST endpoint that reports every resource's budget
// without spending from any of them.
const ghRateLimitURL = "https://api.github.com/rate_limit"

// ghSubprocessProbeTimeout bounds the post-exec /rate_limit refresh. The probe
// is instrumentation: it may never become the reason a pipeline stage hangs,
// so it gets its own short deadline independent of the caller's context.
const ghSubprocessProbeTimeout = 10 * time.Second

// sharedHeadroomTrackerPath resolves the machine-wide tracker file. A var so
// tests can point the gate at a temporary file instead of $HOME.
var sharedHeadroomTrackerPath = DefaultSharedTrackerPath

// ghBinary is the CLI this package execs. A var so tests can substitute a stub
// and stay hermetic — the behaviour under test is the gate and the ledger
// record, neither of which needs a real `gh` on the machine.
var ghBinary = "gh"

// ghSubprocessHTTPClient issues the /rate_limit probe. Deliberately NOT a
// *Client's instrumented transport: that transport writes its own ledger
// record and updates the per-resource baseline the probe is being read to
// compute, so the gh call's cost would always come out as zero. The probe
// stays invisible; the subprocess it prices is what gets recorded.
var ghSubprocessHTTPClient = &http.Client{Timeout: ghSubprocessProbeTimeout}

// ghSubprocessToken resolves the credential for the /rate_limit probe. The
// ambient GH_TOKEN/GITHUB_TOKEN is what the `gh` child itself will use (the
// root command exports the configured one), so reading the same variables
// measures the same account. `gh auth token` is the fallback for a process
// that was not launched through that path.
var ghSubprocessToken = func(ctx context.Context) (string, error) {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v, nil
		}
	}
	return ghAuthToken(ctx, "")
}

// waitSharedHeadroomGate pays the machine-wide rate-limit headroom gate for a
// caller that has no *Client — the `gh` subprocess path.
//
// It waits rather than fails fast: a gh subprocess is always an in-flight
// operation (merge a PR, read a gate's post-condition, reconcile a run) that
// must eventually happen, which is the same reason every in-process client on
// that path is built WithRateLimitWait. The wait is bounded by ctx, so a
// short-deadline caller still bails quickly. NIGHTGAUGE_GITHUB_RATELIMIT_NO_WAIT
// forces fail-fast, matching WithRateLimitWait's own escape hatch for test
// harnesses that must never block on a real reset window.
func waitSharedHeadroomGate(ctx context.Context) error {
	path, err := sharedHeadroomTrackerPath()
	if err != nil {
		return nil // no tracker file resolvable — nothing to gate on
	}
	return headroomGate{
		tracker: NewSharedRateLimitTracker(path),
		// No resource: `gh issue view` bills GraphQL, `gh api repos/...` bills
		// core, and the gate runs before the child names which. Empty gates on
		// the lower of the two (GetBudgetAcrossPools).
		resource: "",
		wait:     os.Getenv(rateLimitNoWaitEnv) == "",
	}.await(ctx)
}

// RunGhSubprocess runs `gh args...` as an instrumented, gated call.
//
// It is the only way this binary should shell out to `gh` for anything that
// touches the GitHub API: it pays the same rate-limit headroom gate an
// in-process call pays, and it writes one API-ledger record naming the real
// calling frame, so `nightgauge api-usage` can attribute the spend. workdir,
// when non-empty, becomes the child's working directory (gh resolves the
// current repository from it).
//
// The returned bytes and error are exactly what exec.Cmd.Output would have
// returned, including *exec.ExitError with its captured Stderr — callers that
// unwrap it (normalizeGhError, gitErrDetail) keep working unchanged.
func RunGhSubprocess(ctx context.Context, workdir string, args ...string) ([]byte, error) {
	if err := waitSharedHeadroomGate(ctx); err != nil {
		return nil, err
	}
	// Captured before exec: ledgerCaller walks the live stack, and the record
	// is written from a helper one frame deeper.
	caller := ledgerCaller()
	start := ledgerNow()
	cmd := exec.CommandContext(ctx, ghBinary, args...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	out, err := cmd.Output()
	recordGhSubprocess(ctx, caller, args, err == nil, ledgerNow().Sub(start))
	return out, err
}

// recordGhSubprocess prices the call that just ran and appends its record.
// Every failure here is swallowed: instrumentation must never break the thing
// it measures.
func recordGhSubprocess(ctx context.Context, caller string, args []string, ok bool, dur time.Duration) {
	ledger := activeAPILedger()
	if ledger == nil {
		return
	}
	rec := APILedgerRecord{
		TS:         ledgerNow().UTC().Format(time.RFC3339Nano),
		Method:     ghSubcommand(args),
		Path:       "gh " + strings.Join(args, " "),
		Caller:     caller,
		DurationMs: dur.Milliseconds(),
		PID:        os.Getpid(),
	}
	if ok {
		// `gh` reports an exit status, never an HTTP status. 200 is the honest
		// translation of "the call the CLI made succeeded"; a failure leaves
		// Status at 0, which every ledger reader already treats as an error of
		// unknown kind (the CLI does not tell us whether it was 403, 422 or a
		// dropped connection).
		rec.Status = http.StatusOK
	}
	remaining := ""
	if snap, err := ghRateLimitSnapshot(ctx); err == nil {
		kind, res := ghSubprocessResource(snap, ledger, args)
		rec.Kind = kind
		rec.Reset = res.Reset
		remaining = strconv.Itoa(res.Remaining)
	}
	ledger.record(rec, remaining)
}

// ghSubcommand is the verb an operator would grep for — "pr", "api", "issue".
func ghSubcommand(args []string) string {
	if len(args) == 0 {
		return "gh"
	}
	return args[0]
}

// ghRateLimitResource is one pool in the /rate_limit response.
type ghRateLimitResource struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset"`
}

// ghRateLimitSnapshot reads every resource's budget in one free REST GET.
//
// GET /rate_limit is the only request that observes the counters without
// moving them, which is what makes it usable as a probe after a call whose
// own headers we never saw.
func ghRateLimitSnapshot(ctx context.Context) (map[string]ghRateLimitResource, error) {
	token, err := ghSubprocessToken(ctx)
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, ghSubprocessProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, ghRateLimitURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := ghSubprocessHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rate_limit probe: status %d", resp.StatusCode)
	}
	var payload struct {
		Resources map[string]ghRateLimitResource `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if len(payload.Resources) == 0 {
		return nil, fmt.Errorf("rate_limit probe: no resources reported")
	}
	return payload.Resources, nil
}

// ghSubprocessResource names the pool the subprocess actually billed.
//
// `gh` prints its payload, never its rate-limit headers, so the resource has
// to be inferred. The one honest signal is which pool MOVED between this
// process's previous observation and the probe taken moments after the call:
// a `gh pr view` draws GraphQL points, a `gh api repos/...` draws core, and
// the counter says which. When nothing moved (first call in this process, or
// a cached/failed call that spent nothing) the drop tells us nothing, so the
// subcommand shape decides — and the record's cost is zero either way, so the
// only thing riding on the fallback is which pool gets a baseline.
func ghSubprocessResource(snap map[string]ghRateLimitResource, ledger *apiLedger, args []string) (string, ghRateLimitResource) {
	bestKind, bestDrop := "", 0
	for kind, res := range snap {
		prev, ok := ledger.previousRemaining(kind)
		if !ok {
			continue
		}
		if drop := prev - res.Remaining; drop > bestDrop {
			bestKind, bestDrop = kind, drop
		}
	}
	if bestKind != "" {
		return bestKind, snap[bestKind]
	}
	kind := ghLikelyResource(args)
	if res, ok := snap[kind]; ok {
		return kind, res
	}
	return "core", snap["core"]
}

// ghLikelyResource guesses the pool from the subcommand shape. `gh api
// graphql` and every high-level object command (pr/issue/project/repo view,
// list, merge…) are GraphQL; a bare `gh api <path>` is REST.
func ghLikelyResource(args []string) string {
	switch ghSubcommand(args) {
	case "api":
		if len(args) > 1 && args[1] == "graphql" {
			return "graphql"
		}
		return "core"
	case "pr", "issue", "project", "repo", "release":
		return "graphql"
	case "search":
		return "search"
	}
	return "core"
}
