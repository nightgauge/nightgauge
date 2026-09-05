package github

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGraphQLOpFromQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"declared query name", "query BoardItems($n: Int!) { organization { ... } }", "BoardItems"},
		{"declared mutation name", "mutation AddItem($i: ID!) { addProjectV2ItemById { ... } }", "AddItem"},
		{
			// The shurcooL struct client never names its operations, so this
			// is the shape almost every real query arrives in. Falling back to
			// the first selected field is what makes the ledger's `op` column
			// useful at all.
			"anonymous falls back to first field",
			"query{organization(login: $owner){projectV2(number: $n){items{nodes{id}}}}}",
			"organization",
		},
		{"anonymous with whitespace", "query {\n  repository(owner: $o) {\n    id\n  }\n}", "repository"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := graphQLOpFromQuery(tc.in); got != tc.want {
				t.Errorf("graphQLOpFromQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestLedgerGraphQLOpReadsBodyWithoutConsumingIt(t *testing.T) {
	body := `{"query":"query{organization(login:\"x\"){id}}","variables":{}}`
	req, err := http.NewRequest(http.MethodPost, "https://api.github.com/graphql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if got := ledgerGraphQLOp(req); got != "organization" {
		t.Fatalf("op = %q, want %q", got, "organization")
	}
	// The request must still be sendable afterwards: instrumentation that
	// drains the body it inspects would break every GraphQL call the moment
	// the ledger is switched on.
	got, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body after inspection: %v", err)
	}
	if string(got) != body {
		t.Errorf("body after inspection = %q, want it unchanged", string(got))
	}
}

func TestLedgerGraphQLOpNoBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/o/r", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if got := ledgerGraphQLOp(req); got != "" {
		t.Errorf("op for a bodyless REST GET = %q, want empty", got)
	}
}

func TestAPILedgerCostIsDropInRemaining(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	l := &apiLedger{f: f, enc: json.NewEncoder(f), prev: map[string]int{}}

	// First call on a resource has no baseline, so it cannot claim a cost.
	l.record(APILedgerRecord{Kind: "graphql"}, "4900")
	// 4900 -> 4883 is a 17-point board read.
	l.record(APILedgerRecord{Kind: "graphql"}, "4883")
	// A different resource has its own baseline and must not borrow GraphQL's.
	l.record(APILedgerRecord{Kind: "core"}, "4999")
	l.record(APILedgerRecord{Kind: "core"}, "4998")
	// Remaining going UP means the hourly window reset between calls. That is
	// not a refund — reporting a negative cost would make any aggregate wrong.
	l.record(APILedgerRecord{Kind: "graphql"}, "5000")
	f.Close()

	costs := ledgerCosts(t, path)
	want := []int{0, 17, 0, 1, 0}
	if len(costs) != len(want) {
		t.Fatalf("got %d records, want %d", len(costs), len(want))
	}
	for i := range want {
		if costs[i] != want[i] {
			t.Errorf("record %d cost = %d, want %d", i, costs[i], want[i])
		}
	}
}

// HeaderObserved is the explicit signal SummarizeWindow now relies on instead
// of inferring "the header was there" from Cost or Status (#1452); record()
// must set it whenever the header actually parses, and leave it false when
// there was nothing to parse — the latter case (Kind only, no header) is
// exactly how the first call on a resource looks, and must not be mistaken
// for an observed remaining:0.
func TestAPILedgerRecordSetsHeaderObserved(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	l := &apiLedger{f: f, enc: json.NewEncoder(f), prev: map[string]int{}}

	l.record(APILedgerRecord{Kind: "graphql"}, "4900")
	l.record(APILedgerRecord{Kind: "graphql"}, "")
	f.Close()

	recs := readLedgerRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("got %d records, want 2", len(recs))
	}
	if !recs[0].HeaderObserved {
		t.Error("record 0: HeaderObserved = false, want true — remainingHdr was a parseable \"4900\"")
	}
	if recs[1].HeaderObserved {
		t.Error("record 1: HeaderObserved = true, want false — remainingHdr was empty, nothing was parsed")
	}
}

func TestAPILedgerNilIsNoOp(t *testing.T) {
	var l *apiLedger
	// A disabled ledger is the common case; it must not panic on the hot path.
	l.record(APILedgerRecord{Kind: "graphql"}, "100")
}

func ledgerCosts(t *testing.T, path string) []int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []int
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec APILedgerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, rec.Cost)
	}
	return out
}

// withTestLedger installs a ledger writing to a temp file for the duration of
// the test and returns the path.
func withTestLedger(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "api.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	testLedgerOverride.Store(&apiLedger{path: path, f: f, enc: json.NewEncoder(f), prev: map[string]int{}})
	t.Cleanup(func() {
		testLedgerOverride.Store((*apiLedger)(nil))
		f.Close()
	})
	return path
}

func readLedgerRecords(t *testing.T, path string) []APILedgerRecord {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	var out []APILedgerRecord
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec APILedgerRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestLedgerRecordsConditionalGETAsFree(t *testing.T) {
	// The headline number the ledger exists to report is "how much of this is
	// free?". A 304 is rewritten to 200 before the response leaves the
	// transport, so unless `cached` is stamped where the 304 is still visible,
	// every conditional hit would be indistinguishable from a full-price read.
	state := &etagServerState{
		body:      `{"id":1}`,
		etag:      `"etag-v1"`,
		remaining: 4999,
	}
	c := newETagTestServer(t, state)
	path := withTestLedger(t)

	ctx := context.Background()
	if _, err := c.restGet(ctx, "/repos/o/r"); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if _, err := c.restGet(ctx, "/repos/o/r"); err != nil {
		t.Fatalf("second fetch: %v", err)
	}

	recs := readLedgerRecords(t, path)
	if len(recs) != 2 {
		t.Fatalf("got %d ledger records, want 2", len(recs))
	}
	if recs[0].Cached {
		t.Error("first fetch recorded as cached; it paid full price")
	}
	if recs[0].Status != 200 {
		t.Errorf("first fetch status = %d, want 200", recs[0].Status)
	}
	if !recs[1].Cached {
		t.Error("second fetch was answered 304 but is not recorded as cached")
	}
	if recs[1].Status != 304 {
		t.Errorf("second fetch status = %d, want the 304 the server actually sent", recs[1].Status)
	}
	if recs[1].Cost != 0 {
		t.Errorf("cached fetch cost = %d, want 0 — a 304 never left the budget", recs[1].Cost)
	}
	if recs[0].Path != "/repos/o/r" {
		t.Errorf("path = %q, want /repos/o/r", recs[0].Path)
	}
}

func TestLedgerWriteFailureNeverBreaksTheRequest(t *testing.T) {
	// Instrumentation that can fail a pipeline run is worse than none. A
	// closed file is the cheapest stand-in for a full disk or a revoked
	// permission mid-run.
	// A non-empty ETag matters: the fake server compares If-None-Match to it,
	// and an empty one would match the absent header on the very first request.
	state := &etagServerState{body: `{"id":1}`, etag: `"etag-v1"`, remaining: 4999}
	c := newETagTestServer(t, state)

	f, err := os.Create(filepath.Join(t.TempDir(), "api.jsonl"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	f.Close() // every subsequent write fails
	testLedgerOverride.Store(&apiLedger{path: f.Name(), f: f, enc: json.NewEncoder(f), prev: map[string]int{}})
	t.Cleanup(func() { testLedgerOverride.Store((*apiLedger)(nil)) })

	body, err := c.restGet(context.Background(), "/repos/o/r")
	if err != nil {
		t.Fatalf("request failed because the ledger could not be written: %v", err)
	}
	if string(body) != state.body {
		t.Errorf("body = %q, want %q", body, state.body)
	}
}

// The ledger's Caller field answers "what is costing me points, and can I
// delete or throttle it?" A layer that only forwards a request is never that
// answer — and #845 proved the failure is silent: adding the board cache moved
// every board read's attribution off the producers and onto the cache, with
// nothing to notice (#860).
//
// The table is written as REAL frame names, including the producers whose
// attribution was lost, so it fails if a future skip entry swallows them.
func TestIsPassThroughFrame(t *testing.T) {
	for _, tc := range []struct {
		fn   string
		skip bool
		why  string
	}{
		// Layers between real code and the wire.
		{"github.com/nightgauge/nightgauge/internal/github.(*Client).graphQL", true, "the adapter itself"},
		{"net/http.(*Client).Do", true, "HTTP plumbing"},
		{"net/http/httputil.DumpRequest", true, "HTTP plumbing"},
		{"github.com/shurcooL/graphql.(*Client).do", true, "GraphQL client"},
		{"golang.org/x/oauth2.(*Transport).RoundTrip", true, "auth plumbing"},
		{"github.com/nightgauge/nightgauge/internal/forge/boardcache.(*cachedBoard).ListOpenItems", true, "#860: a cache cannot be deleted or throttled"},
		{"github.com/nightgauge/nightgauge/internal/forge/boardcache.(*Cache).get.func1", true, "#860: the fetch closure is still the cache"},

		// The frames an operator can actually act on. These are the exact
		// callers #845 hid; naming them is the point of the field.
		{"github.com/nightgauge/nightgauge/internal/attention/sweep.(*CoverageGap).discoverBoard", false, "a producer — deletable, throttleable"},
		{"github.com/nightgauge/nightgauge/internal/attention/sweep.(*StrandedReadyItems).boardUnreachable", false, "a producer"},
		{"github.com/nightgauge/nightgauge/internal/orchestrator.(*Scheduler).poll", false, "the scheduler"},

		// Near-misses. A skip entry that overmatches drops real callers, which
		// is the same defect as naming a cache: a confident, wrong answer.
		{"github.com/nightgauge/nightgauge/internal/forge/boardcachexyz.(*T).M", false, "different package, shared prefix"},
		{"github.com/nightgauge/nightgauge/internal/forgeboardcache.(*T).M", false, "different package, no separator"},
		{"github.com/nightgauge/nightgauge/internal/forge.(*Router).For", false, "forge itself is not the cache"},
		{"github.com/nightgauge/nightgauge/internal/githubactions.Run", false, "shares the 'github' prefix but is not this package"},
	} {
		if got := isPassThroughFrame(tc.fn); got != tc.skip {
			verb := "skipped"
			if !tc.skip {
				verb = "attributed"
			}
			t.Errorf("isPassThroughFrame(%q) = %v, want %v — it should be %s (%s)", tc.fn, got, tc.skip, verb, tc.why)
		}
	}
}
