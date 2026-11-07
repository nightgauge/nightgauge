package deliverable

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/gateledger"
)

// UnitSourceGateLedger marks a `unit_tests` block whose pass comes from the
// gate ledger rather than from the stage's own report (#2941).
const UnitSourceGateLedger = "gate-ledger"

// LedgerUnitPass reports whether the gate ledger holds a full local-gate pass
// for the tree checked out at workspace. A full local gate runs the
// repository's complete suite, unit tests included, so it answers "did the
// unit tests run and pass on this code?" even when the stage that ran it — a
// background `ci-local.sh` it polled, say — wrote no structured result (#2941).
// Any doubt (no fingerprint, unreadable ledger, a later failure) is false.
func LedgerUnitPass(ctx context.Context, workspace string) (gateledger.Answer, bool) {
	ans, err := gateledger.LatestForWorktree(ctx, workspace, gateledger.KindLocalGate)
	if err != nil || !ans.Reusable {
		return ans, false
	}
	return ans, true
}

// ConfirmUnitFromLedger records a ledger pass in a validate document whose
// `unit_tests` block does not already say the suite ran. A block that ran
// keeps its own verdict, so a recorded failure is never overwritten by a
// pass. It reports whether it changed the document.
func ConfirmUnitFromLedger(doc map[string]any, ans gateledger.Answer) bool {
	if !ans.Reusable || tierRan(doc, "unit_tests") {
		return false
	}
	block, _ := doc["unit_tests"].(map[string]any)
	if block == nil {
		block = map[string]any{}
	}
	block["ran"] = true
	block["passed"] = true
	block["source"] = UnitSourceGateLedger
	block["tree"] = ans.Pass.Tree
	doc["unit_tests"] = block
	return true
}

// UnitTestsLog is the gate log a validate document names in
// `unit_tests.log`, or "".
func UnitTestsLog(doc map[string]any) string {
	block, _ := doc["unit_tests"].(map[string]any)
	s, _ := block["log"].(string)
	return strings.TrimSpace(s)
}

// TestOutcomes is what a gate log says about individual tests.
type TestOutcomes struct {
	Passed map[string]bool
	Failed map[string]bool
}

// goTestResultRE matches `go test -v` result lines, nested subtests
// included: `--- PASS: TestX (0.35s)`, `    --- FAIL: TestX/sub (0.00s)`.
var goTestResultRE = regexp.MustCompile(`^\s*--- (PASS|FAIL): (\S+) \(`)

// ParseGoTestOutcomes reads per-test results from a gate log. Only Go's own
// result lines count: anything else that happens to say PASS proves nothing
// about a named test. A test reported both ways (a -count run with a flake)
// is failed.
func ParseGoTestOutcomes(path string) (TestOutcomes, error) {
	out := TestOutcomes{Passed: map[string]bool{}, Failed: map[string]bool{}}
	f, err := os.Open(path)
	if err != nil {
		return out, fmt.Errorf("open gate log: %w", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		m := goTestResultRE.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		if m[1] == "PASS" {
			out.Passed[m[2]] = true
		} else {
			out.Failed[m[2]] = true
		}
	}
	if err := sc.Err(); err != nil {
		return out, fmt.Errorf("read gate log: %w", err)
	}
	for name := range out.Failed {
		delete(out.Passed, name)
	}
	return out, nil
}

// LogFromPass reports whether the gate log at path was written by the ledger
// pass pass: its last write falls inside that run's window. Without a pass
// for the current tree, no log can be tied to it, so a stale log from an
// earlier run never answers for named tests (#2941).
func LogFromPass(path string, pass *gateledger.Event) bool {
	if pass == nil {
		return false
	}
	finish, err := time.Parse(time.RFC3339Nano, pass.TS)
	if err != nil {
		return false
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	start := finish.Add(-time.Duration(pass.Seconds)*time.Second - logWindowSlack)
	mt := info.ModTime()
	return !mt.Before(start) && !mt.After(finish.Add(logWindowSlack))
}

// logWindowSlack absorbs clock granularity and the gap between the log's last
// write and the ledger's finish event.
const logWindowSlack = 2 * time.Minute
