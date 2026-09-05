package doctor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/github"
)

// writeLedger lays down a ledger file inside a fake workspace root.
func writeLedger(t *testing.T, root string, recs ...github.APILedgerRecord) {
	t.Helper()
	path := github.DefaultLedgerPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create ledger: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range recs {
		if err := enc.Encode(&r); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
}

func rec(now time.Time, ago time.Duration, caller string, cost, remaining int) github.APILedgerRecord {
	return github.APILedgerRecord{
		TS:             now.Add(-ago).UTC().Format(time.RFC3339Nano),
		Kind:           "graphql",
		Method:         "POST",
		Path:           "/graphql",
		Caller:         caller,
		Status:         200,
		Cost:           cost,
		Remaining:      remaining,
		HeaderObserved: true,
	}
}

// A workspace that has never talked to GitHub has no ledger. That is the
// normal state of a fresh install and must not be reported as a problem — an
// arm that fires on every fresh install is an arm operators switch off.
func TestAPIBudgetAbsentLedgerIsNotAFinding(t *testing.T) {
	item, warning := checkGitHubAPIBudget(t.TempDir(), time.Now())
	if !item.OK {
		t.Errorf("OK = false for a workspace with no ledger: %+v", item)
	}
	if warning != "" {
		t.Errorf("warning = %q, want none", warning)
	}
}

func TestAPIBudgetNoWorkspaceRoot(t *testing.T) {
	item, warning := checkGitHubAPIBudget("", time.Now())
	if !item.OK || warning != "" {
		t.Errorf("checkGitHubAPIBudget(\"\") = %+v / %q, want a clean skip", item, warning)
	}
}

// Quiet hour: the arm reports the spend and stays green.
func TestAPIBudgetQuietWindowIsGreen(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeLedger(t, root,
		rec(now, 40*time.Minute, "boardcache.Refresh", 17, 4983),
		rec(now, 10*time.Minute, "boardcache.Refresh", 17, 4966),
	)
	item, warning := checkGitHubAPIBudget(root, now)
	if !item.OK {
		t.Errorf("OK = false on a 34-point hour: %+v", item)
	}
	if warning != "" {
		t.Errorf("warning = %q on a quiet hour, want none", warning)
	}
	if !strings.Contains(item.Detail, "34 GraphQL point") {
		t.Errorf("Detail = %q, want the point total", item.Detail)
	}
	if !strings.Contains(item.Detail, "boardcache.Refresh") {
		t.Errorf("Detail = %q, want the top caller named", item.Detail)
	}
}

// The whole point of the feature: an exhaustion nobody was watching for is
// still legible afterwards, and it names the caller that caused it.
func TestAPIBudgetExhaustionIsAFindingThatNamesTheCaller(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeLedger(t, root,
		rec(now, 50*time.Minute, "sweep.Producers", 2500, 2500),
		rec(now, 30*time.Minute, "sweep.Producers", 2500, 0),
		rec(now, 20*time.Minute, "depgraph.Rebuild", 3, 0),
	)
	item, warning := checkGitHubAPIBudget(root, now)
	if item.OK {
		t.Fatalf("OK = true on an exhausted window: %+v", item)
	}
	if !strings.Contains(warning, "github-api-budget-exhausted") {
		t.Errorf("warning = %q, want the exhausted finding id", warning)
	}
	if !strings.Contains(warning, "sweep.Producers") {
		t.Errorf("warning = %q, want the top spender named — an unattributed "+
			"exhaustion is the exact state #1347 exists to end", warning)
	}
	if !strings.Contains(warning, "30 minute(s) ago") {
		t.Errorf("warning = %q, want the time of the exhaustion", warning)
	}
}

// Half the hourly quota from one workspace is the rate at which a single open
// window can exhaust the budget on its own.
func TestAPIBudgetHighIdleSpendWarns(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeLedger(t, root,
		rec(now, 55*time.Minute, "boardcache.Refresh", 1600, 3400),
		rec(now, 5*time.Minute, "boardcache.Refresh", 1500, 1900),
	)
	item, warning := checkGitHubAPIBudget(root, now)
	if item.OK {
		t.Fatalf("OK = true at 3100 points/hour: %+v", item)
	}
	if !strings.Contains(warning, "github-api-budget-high") {
		t.Errorf("warning = %q, want the high-spend finding id", warning)
	}
	if strings.Contains(warning, "exhausted") {
		t.Errorf("warning = %q claims exhaustion; the quota never hit zero", warning)
	}
}

// Records older than the window are not this hour's bill. Without the bound,
// an exhaustion from yesterday warns forever and the arm becomes furniture.
func TestAPIBudgetIgnoresRecordsOutsideTheWindow(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeLedger(t, root,
		rec(now, 6*time.Hour, "yesterday.Caller", 5000, 0),
		rec(now, 10*time.Minute, "today.Caller", 17, 4983),
	)
	item, warning := checkGitHubAPIBudget(root, now)
	if !item.OK {
		t.Fatalf("OK = false: a six-hour-old exhaustion is not this hour's bill: %+v / %q", item, warning)
	}
	if strings.Contains(item.Detail, "yesterday.Caller") {
		t.Errorf("Detail = %q names a caller from outside the window", item.Detail)
	}
}

// A caller that spent nothing is not a spender, and padding the finding with
// it hides the row that is the answer.
func TestTopCallerListDropsZeroPointCallers(t *testing.T) {
	w := github.LedgerWindow{TopCallers: []github.LedgerCallerSpend{
		{Caller: "expensive", Points: 40, Calls: 2},
		{Caller: "free", Points: 0, Calls: 90},
	}}
	got := topCallerList(w)
	if strings.Contains(got, "free") {
		t.Errorf("topCallerList = %q, want the zero-point caller dropped", got)
	}
	if !strings.Contains(got, "expensive") {
		t.Errorf("topCallerList = %q, want the spender named", got)
	}
}

func TestTopCallerListWithNoPricedCalls(t *testing.T) {
	got := topCallerList(github.LedgerWindow{})
	if !strings.Contains(got, "none attributed") {
		t.Errorf("topCallerList(empty) = %q, want an explicit no-attribution string", got)
	}
}
