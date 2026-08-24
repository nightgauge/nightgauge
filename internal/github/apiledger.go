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

// apiLedgerEnv names the env var that turns the GitHub API call ledger on.
// Set it to a path to choose the file, or to "1" for the default location
// under the workspace's .nightgauge/logs/.
//
// The ledger exists because every previous attempt to cut this repo's GitHub
// API consumption reasoned from a code audit — counting call sites and
// multiplying by an assumed cadence. That method cannot see what actually runs
// (a producer whose calls fail and retry, a timer fired by a trigger nobody
// counted) and it cannot price a call: a single board query can cost dozens of
// GraphQL points while a REST GET costs one. Attribution has to be measured at
// the only place that sees every request with its real price — the transport.
const apiLedgerEnv = "NIGHTGAUGE_GITHUB_API_LOG"

// apiLedgerDefaultPath is used when apiLedgerEnv is "1"/"true": a JSONL file
// beside the other per-workspace logs.
const apiLedgerDefaultPath = ".nightgauge/logs/github-api.jsonl"

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
	// Caller is the first stack frame outside this package — the code that
	// asked for the call, which is what an operator needs in order to delete
	// or throttle it.
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
	// Cached marks a conditional GET the server answered 304 — free, and the
	// ratio of these to full-price GETs is the headline number for whether
	// conditional requests are actually working.
	Cached     bool  `json:"cached,omitempty"`
	DurationMs int64 `json:"duration_ms"`
	PID        int   `json:"pid"`
}

// apiLedger appends request records to a JSONL file. A nil *apiLedger is a
// no-op, so the disabled path costs one nil check per request.
type apiLedger struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	prev map[string]int // resource -> last observed Remaining
}

var (
	ledgerOnce sync.Once
	ledgerInst *apiLedger
	// testLedgerOverride is set only by tests, via withTestLedger.
	testLedgerOverride atomic.Value
)

// activeAPILedger returns the process-wide ledger, or nil when the env var is
// unset. Resolved once: a long-lived daemon must not stat the environment on
// every request, and flipping the var mid-process would split one run's
// records across two files.
func activeAPILedger() *apiLedger {
	// A ledger installed by a test wins over the environment. The env path is
	// resolved through sync.Once precisely so a long-lived daemon never
	// re-stats it, which also makes it untestable — this seam is how the
	// transport's ledger behaviour gets covered without that trade-off.
	if l := testLedgerOverride.Load(); l != nil {
		return l.(*apiLedger)
	}
	ledgerOnce.Do(func() {
		raw := strings.TrimSpace(os.Getenv(apiLedgerEnv))
		if raw == "" || raw == "0" || raw == "false" {
			return
		}
		path := raw
		if raw == "1" || raw == "true" {
			path = apiLedgerDefaultPath
		}
		if !filepath.IsAbs(path) {
			if wd, err := os.Getwd(); err == nil {
				path = filepath.Join(wd, path)
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			// Instrumentation must never break the thing it measures.
			return
		}
		ledgerInst = &apiLedger{f: f, enc: json.NewEncoder(f), prev: map[string]int{}}
	})
	return ledgerInst
}

// record writes one request. Every failure is swallowed: a ledger that can
// break a pipeline run is worse than no ledger.
func (l *apiLedger) record(rec APILedgerRecord, remainingHdr string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if remainingHdr != "" {
		if n, err := strconv.Atoi(remainingHdr); err == nil {
			rec.Remaining = n
			if prev, ok := l.prev[rec.Kind]; ok && prev >= n {
				rec.Cost = prev - n
			}
			l.prev[rec.Kind] = n
		}
	}
	_ = l.enc.Encode(&rec)
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
		switch {
		case strings.Contains(fr.Function, "/internal/github."),
			strings.HasPrefix(fr.Function, "net/http."),
			strings.HasPrefix(fr.Function, "net/http/"),
			strings.Contains(fr.Function, "shurcooL"),
			strings.Contains(fr.Function, "golang.org/x/oauth2"):
		default:
			return trimCallerFunc(fr.Function) + " " + filepath.Base(fr.File) + ":" + strconv.Itoa(fr.Line)
		}
		if !more {
			break
		}
	}
	return ""
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
