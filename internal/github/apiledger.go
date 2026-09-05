package github

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// apiLedgerEnv names the env var that overrides the GitHub API call ledger.
// Set it to a path to choose the file, to "1" to force the default location
// under the workspace's .nightgauge/logs/, or to "0" to switch the ledger off.
//
// The ledger exists because every previous attempt to cut this repo's GitHub
// API consumption reasoned from a code audit — counting call sites and
// multiplying by an assumed cadence. That method cannot see what actually runs
// (a producer whose calls fail and retry, a timer fired by a trigger nobody
// counted) and it cannot price a call: a single board query can cost dozens of
// GraphQL points while a REST GET costs one. Attribution has to be measured at
// the only place that sees every request with its real price — the transport.
//
// It is ON BY DEFAULT (#1347). Opt-in instrumentation answers the question
// only for the operator who predicted they would need it: every quota
// exhaustion this workspace has hit was unattributable AFTER THE FACT, because
// the one instrument that prices a call was switched off while the spending
// happened. An exhaustion is not reproducible on demand — the idle burn that
// causes it takes an hour of a real daemon against a real board — so "turn it
// on and try again" is not a diagnosis, it is a request to wait for the next
// outage. The cost of always-on is one bounded, rotated file; the cost of
// opt-in is that the data never exists when it is needed.
const apiLedgerEnv = "NIGHTGAUGE_GITHUB_API_LOG"

// apiLedgerDefaultPath is used unless apiLedgerEnv names a different file:
// a JSONL file beside the other per-workspace logs.
const apiLedgerDefaultPath = ".nightgauge/logs/github-api.jsonl"

// The ledger is a rolling file so "always on" cannot become "fills the disk".
// One record is ~250 bytes, so 5 MB holds roughly 20k requests — several days
// of idle daemon traffic, and far more than the one-hour window every consumer
// of the ledger actually asks about. One backup is kept so a rotation in the
// middle of the window under investigation does not erase its first half.
const (
	ledgerMaxBytes  = 5 * 1024 * 1024
	ledgerKeepFiles = 2
)

// APILedgerRecord is one GitHub API request as the transport saw it. Written
// as JSONL so `nightgauge api-usage` (and any ad-hoc jq) can aggregate without
// a schema migration.
type APILedgerRecord struct {
	TS string `json:"ts"`
	// Kind is the rate-limit resource GitHub billed the call to, straight from
	// X-RateLimit-Resource: "graphql", "core", "search", "graphql_mutation".
	// It is the billed truth, not our guess from the URL.
	Kind   string `json:"kind"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Op is the GraphQL operation name when the body declared one, else the
	// first selected field. Empty for REST.
	Op string `json:"op,omitempty"`
	// Caller is the first stack frame outside this package AND outside the
	// pass-through layers between it and real code — the frame an operator can
	// actually delete or throttle. A layer that merely forwards a request
	// (net/http, the GraphQL client, oauth2, the board cache) is skipped: it is
	// never the answer to "what is costing me points?", and leaving one in
	// silently retires the ledger's whole purpose (#860).
	Caller string `json:"caller,omitempty"`
	Status int    `json:"status"`
	// Cost is points billed, derived from the drop in X-RateLimit-Remaining
	// since this process's previous call on the same resource. Zero for the
	// first call on a resource (no baseline) and for a 304 served from cache
	// (which never left the machine). Negative drops mean the window reset
	// between calls and are reported as 0 rather than as a bogus refund.
	Cost      int   `json:"cost"`
	Remaining int   `json:"remaining"`
	Reset     int64 `json:"reset,omitempty"`
	// HeaderObserved is true exactly when this request's response carried a
	// parseable X-RateLimit-Remaining header — independent of Cost and
	// Cached. A cached (304) hit legitimately costs 0 points (Cost is the
	// drop since the previous call, and a call made while already exhausted
	// computes 0-0=0), so "Cost is zero" and "the header was never observed"
	// are different facts; without this field the aggregator could not tell
	// them apart and a real remaining:0 header from a cached call read as
	// "unknown" instead of "exhausted" (#1452). Records written before this
	// field existed decode it as false, so they conservatively read as
	// header-not-observed rather than being misread as a spurious exhaustion.
	HeaderObserved bool `json:"header_observed,omitempty"`
	// Cached marks a conditional GET the server answered 304 — free, and the
	// ratio of these to full-price GETs is the headline number for whether
	// conditional requests are actually working.
	Cached     bool  `json:"cached,omitempty"`
	DurationMs int64 `json:"duration_ms"`
	PID        int   `json:"pid"`
}

// apiLedger appends request records to a rolling JSONL file. A nil *apiLedger
// is a no-op, so the disabled path costs one nil check per request.
type apiLedger struct {
	mu   sync.Mutex
	path string
	f    *os.File
	enc  *json.Encoder
	prev map[string]int // resource -> last observed Remaining
}

var (
	// ledgerReady/ledgerPtr resolve the process-wide ledger exactly once, with
	// a lock-free fast path on every subsequent request. Resolution is latched
	// deliberately: a long-lived daemon must not stat the environment on every
	// request, and flipping the setting mid-process would split one run's
	// records across two files.
	ledgerReady atomic.Bool
	ledgerPtr   atomic.Pointer[apiLedger]
	ledgerMu    sync.Mutex
	// ledgerConfigOff records `github.api_ledger.enabled: false`. Default zero
	// value (false) is therefore "not disabled" — the ledger is on unless
	// something explicitly turns it off.
	ledgerConfigOff atomic.Bool
	// testLedgerOverride is set only by tests, via withTestLedger.
	testLedgerOverride atomic.Value
)

// SetAPILedgerEnabled applies the `github.api_ledger.enabled` config setting.
//
// It lives here rather than reading config directly because internal/config
// already imports internal/github, so the dependency can only run this way.
// Call it once, from config load, BEFORE the first GitHub request: after the
// ledger has resolved, disabling still takes effect (the open file is closed)
// but the requests already made are on disk, and re-enabling is refused
// because the process would then hold two half-windows.
//
// The environment wins over config in both directions — NIGHTGAUGE_GITHUB_API_LOG
// is a NIGHTGAUGE_* override, which this workspace treats as top precedence.
func SetAPILedgerEnabled(enabled bool) {
	if enabled {
		return // on is the default; nothing to do, and see the doc above
	}
	if envForcesLedgerOn() {
		return // an explicit env override outranks config
	}
	ledgerConfigOff.Store(true)
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if ledgerReady.Load() {
		if l := ledgerPtr.Load(); l != nil {
			l.close()
			ledgerPtr.Store(nil)
		}
	}
}

// envForcesLedgerOn reports whether the env var explicitly asks for a ledger
// (as opposed to being unset, which merely leaves the default in place).
func envForcesLedgerOn() bool {
	raw := strings.TrimSpace(os.Getenv(apiLedgerEnv))
	switch raw {
	case "", "0", "false", "off":
		return false
	}
	return true
}

// activeAPILedger returns the process-wide ledger, or nil when it is switched
// off by the environment or by config.
func activeAPILedger() *apiLedger {
	// A ledger installed by a test wins over everything else. Resolution is
	// latched precisely so a long-lived daemon never re-stats it, which also
	// makes it untestable — this seam is how the transport's ledger behaviour
	// gets covered without that trade-off.
	if l := testLedgerOverride.Load(); l != nil {
		return l.(*apiLedger)
	}
	if ledgerReady.Load() {
		return ledgerPtr.Load()
	}
	ledgerMu.Lock()
	defer ledgerMu.Unlock()
	if ledgerReady.Load() {
		return ledgerPtr.Load()
	}
	ledgerPtr.Store(openAPILedger())
	ledgerReady.Store(true)
	return ledgerPtr.Load()
}

// openAPILedger resolves the configured path and opens the file, returning nil
// for every "off" or "cannot" case. Instrumentation must never break the thing
// it measures, so an unwritable path degrades to no ledger, not to an error.
func openAPILedger() *apiLedger {
	raw := strings.TrimSpace(os.Getenv(apiLedgerEnv))
	switch raw {
	case "0", "false", "off":
		return nil // env opt-out, outranks config
	}
	if raw == "" && ledgerConfigOff.Load() {
		return nil // `github.api_ledger.enabled: false`
	}
	path := raw
	explicitPath := true
	if path == "" || path == "1" || path == "true" || path == "on" {
		path = apiLedgerDefaultPath
		explicitPath = false
	}
	if !filepath.IsAbs(path) {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		path = filepath.Join(wd, path)
	}
	// At the DEFAULT path, the ledger writes into an existing workspace and
	// never conjures one. An always-on instrument that creates .nightgauge/
	// wherever it happens to be started scatters the tree: `go test` runs each
	// package with its own directory as the cwd, so the first run after this
	// became default-on left .nightgauge/logs/ inside four internal/ packages,
	// untracked and unignored. The rule also matches the semantics — the
	// ledger is per-workspace, and a process running outside one has no
	// workspace to bill.
	//
	// An explicitly configured path is a different statement: the operator
	// named a file, so the directories for it are created.
	if !explicitPath {
		if _, err := os.Stat(filepath.Dir(filepath.Dir(path))); err != nil {
			return nil // no .nightgauge/ here — this is not a workspace
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	l := &apiLedger{path: path, prev: map[string]int{}}
	if err := l.open(); err != nil {
		return nil
	}
	return l
}

// open attaches the writer to l.path. Caller must hold l.mu (or own l).
func (l *apiLedger) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	l.f = f
	l.enc = json.NewEncoder(f)
	return nil
}

// close releases the file. Safe on an already-closed ledger.
func (l *apiLedger) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f != nil {
		_ = l.f.Close()
		l.f = nil
		l.enc = nil
	}
}

// record writes one request. Every failure is swallowed: a ledger that can
// break a pipeline run is worse than no ledger.
func (l *apiLedger) record(rec APILedgerRecord, remainingHdr string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.enc == nil {
		return // closed by SetAPILedgerEnabled(false)
	}
	if remainingHdr != "" {
		if n, err := strconv.Atoi(remainingHdr); err == nil {
			rec.Remaining = n
			rec.HeaderObserved = true
			if prev, ok := l.prev[rec.Kind]; ok && prev >= n {
				rec.Cost = prev - n
			}
			l.prev[rec.Kind] = n
		}
	}
	_ = l.enc.Encode(&rec)
	l.rotateIfFull()
}

// rotateIfFull shifts the numbered backups up and reopens a fresh file once
// the current one passes ledgerMaxBytes. Caller must hold l.mu.
//
// The size is read from the PATH, not from the open handle, because several
// nightgauge processes share one workspace ledger: if a sibling has already
// rotated, this process is holding a renamed inode and must reopen rather than
// rotate again. Reading the path makes that case self-correcting — the stat
// reports the small, fresh file and no second rotation happens.
func (l *apiLedger) rotateIfFull() {
	if l.f == nil {
		return
	}
	info, err := os.Stat(l.path)
	if err != nil || info.Size() < ledgerMaxBytes {
		if err == nil && l.rotatedAway(info) {
			// A sibling process rotated our file out from under us; follow it
			// to the new one so records stop landing in an orphaned inode.
			_ = l.f.Close()
			l.f, l.enc = nil, nil
			_ = l.open()
		}
		return
	}
	_ = l.f.Close()
	l.f, l.enc = nil, nil
	// Shift: .(keep-2) -> .(keep-1), ..., .1 -> .2, base -> .1. ledgerKeepFiles
	// counts the TOTAL files retained (live + backups), so the highest backup
	// number is keep-1 and the loop must start one below that — starting at
	// keep-1 would rename the oldest backup to .keep and quietly retain one
	// more file than the bound promises.
	for i := ledgerKeepFiles - 2; i >= 1; i-- {
		src := l.path + "." + strconv.Itoa(i)
		dst := l.path + "." + strconv.Itoa(i+1)
		if _, serr := os.Stat(src); serr == nil {
			_ = os.Rename(src, dst)
		}
	}
	_ = os.Rename(l.path, l.path+".1")
	_ = l.open()
}

// rotatedAway reports whether the file at l.path is a different inode from the
// one l.f is writing to — the signature of a sibling process having rotated.
func (l *apiLedger) rotatedAway(pathInfo os.FileInfo) bool {
	ourInfo, err := l.f.Stat()
	if err != nil {
		return false
	}
	return !os.SameFile(ourInfo, pathInfo)
}

// ledgerCaller walks the stack for the first frame outside this package and
// outside the HTTP/GraphQL plumbing. That frame is the code an operator can
// actually act on; everything between it and the socket is machinery.
func ledgerCaller() string {
	pcs := make([]uintptr, 24)
	n := runtime.Callers(3, pcs)
	frames := runtime.CallersFrames(pcs[:n])
	for {
		fr, more := frames.Next()
		if fr.Function == "" {
			if !more {
				break
			}
			continue
		}
		if !isPassThroughFrame(fr.Function) {
			return trimCallerFunc(fr.Function) + " " + filepath.Base(fr.File) + ":" + strconv.Itoa(fr.Line)
		}
		if !more {
			break
		}
	}
	return ""
}

// isPassThroughFrame reports whether a stack frame merely FORWARDS a request
// rather than originating one.
//
// This predicate is the whole meaning of the Caller field: the ledger exists to
// answer "what is costing me points, and can I delete or throttle it?", and a
// layer nobody can delete is never that answer. Each entry is here because it
// sits between real code and the wire:
//
//   - internal/github — this package: the adapter and the transport itself.
//   - net/http, shurcooL/graphql, oauth2 — the HTTP and GraphQL plumbing.
//   - internal/forge/boardcache — the board snapshot cache (#845). Added after
//     it silently became the attributed caller for every board read, which is
//     the single most expensive call in the product (#860).
//
// Matching is on the package path with its trailing dot ("…/boardcache.") so a
// different package that merely shares a prefix is not swallowed. Attribution
// that silently drops real callers is the same defect as attribution that names
// a cache — both leave an operator reading a confident, wrong answer.
func isPassThroughFrame(fn string) bool {
	switch {
	case strings.Contains(fn, "/internal/github."),
		strings.HasPrefix(fn, "net/http."),
		strings.HasPrefix(fn, "net/http/"),
		strings.Contains(fn, "shurcooL"),
		strings.Contains(fn, "golang.org/x/oauth2"),
		strings.Contains(fn, "/internal/forge/boardcache."):
		return true
	}
	return false
}

// trimCallerFunc shortens a fully-qualified function name to the last two
// path segments, which is enough to identify the caller without turning every
// record into a module path.
func trimCallerFunc(fn string) string {
	parts := strings.Split(fn, "/")
	if len(parts) <= 2 {
		return fn
	}
	return strings.Join(parts[len(parts)-2:], "/")
}

// ledgerGraphQLOp extracts an operation name from a GraphQL request body. It
// prefers a declared name ("query Foo(...)"), and falls back to the first
// selected field, which is what the shurcooL struct-based client sends since
// it never names its operations.
func ledgerGraphQLOp(req *http.Request) string {
	if req == nil || req.GetBody == nil {
		return ""
	}
	rc, err := req.GetBody()
	if err != nil || rc == nil {
		return ""
	}
	defer rc.Close()
	// A bounded read: query bodies are small, and an unbounded one would let a
	// pathological payload pull megabytes into memory for a log line.
	raw, err := io.ReadAll(io.LimitReader(rc, 8192))
	if err != nil {
		return ""
	}
	var payload struct {
		Query         string `json:"query"`
		OperationName string `json:"operationName"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	if payload.OperationName != "" {
		return payload.OperationName
	}
	return graphQLOpFromQuery(payload.Query)
}

// graphQLOpFromQuery names an anonymous GraphQL document by its first selected
// field — "query{repository(...){...}}" reads as "repository".
func graphQLOpFromQuery(q string) string {
	q = strings.TrimSpace(q)
	if q == "" {
		return ""
	}
	for _, kw := range []string{"query", "mutation"} {
		rest, ok := strings.CutPrefix(q, kw)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		// A declared name comes before the '(' or '{' that follows it.
		if name := leadingIdent(rest); name != "" {
			return name
		}
		// Anonymous: step into the selection set and take the first field.
		if _, after, found := strings.Cut(rest, "{"); found {
			return leadingIdent(strings.TrimSpace(after))
		}
		return kw
	}
	return leadingIdent(q)
}

// leadingIdent returns the identifier at the start of s, or "" when s does not
// begin with one.
func leadingIdent(s string) string {
	end := 0
	for end < len(s) {
		ch := s[end]
		isIdent := ch == '_' ||
			(ch >= 'a' && ch <= 'z') ||
			(ch >= 'A' && ch <= 'Z') ||
			(end > 0 && ch >= '0' && ch <= '9')
		if !isIdent {
			break
		}
		end++
	}
	return s[:end]
}

// ledgerNow is swappable so tests can assert on a fixed timestamp.
var ledgerNow = time.Now
