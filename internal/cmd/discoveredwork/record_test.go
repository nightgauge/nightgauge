package discoveredwork

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/runstate"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func validRecord(issue int) Record {
	return Record{
		Summary:              "Retry budget ignores the configured ceiling",
		Evidence:             []string{"internal/ci/wait.go:88 hard-codes 5"},
		SourceStage:          "feature-validate",
		IssueNumber:          issue,
		SuggestedType:        "bug",
		SuggestedPriority:    "medium",
		SuggestedSize:        "S",
		CandidateTargetFiles: []string{"internal/ci/wait.go"},
	}
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestRecordSchema_KeySetMatchesSDK: the Go structs and the shared fixture
// (which the SDK test also pins its zod shape against) carry identical keys,
// both ways, for the envelope and the record.
func TestRecordSchema_KeySetMatchesSDK(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "record-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := Decode(data)
	if err != nil {
		t.Fatalf("fixture does not decode strictly: %v", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(c)
	var round map[string]any
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatal(err)
	}
	if a, b := sortedKeys(fixture), sortedKeys(round); !reflect.DeepEqual(a, b) {
		t.Errorf("envelope keys: fixture %v, Go %v", a, b)
	}
	fr := fixture["records"].([]any)[0].(map[string]any)
	gr := round["records"].([]any)[0].(map[string]any)
	if a, b := sortedKeys(fr), sortedKeys(gr); !reflect.DeepEqual(a, b) {
		t.Errorf("record keys: fixture %v, Go %v", a, b)
	}
	if !reflect.DeepEqual(fr, gr) {
		t.Errorf("record values do not round-trip:\nfixture %v\nGo      %v", fr, gr)
	}
}

func seedCapture(t *testing.T, dir string, issue int) []byte {
	t.Helper()
	if _, _, err := Append(dir, validRecord(issue), now); err != nil {
		t.Fatal(err)
	}
	p, _ := CapturePath(dir, issue)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertRefusedUnchanged(t *testing.T, rec Record, field string) {
	t.Helper()
	dir := t.TempDir()
	before := seedCapture(t, dir, rec.IssueNumber)
	_, _, err := Append(dir, rec, now)
	if err == nil || !strings.HasPrefix(err.Error(), field+":") {
		t.Fatalf("err = %v, want one naming %q", err, field)
	}
	p, _ := CapturePath(dir, rec.IssueNumber)
	after, _ := os.ReadFile(p)
	if !bytes.Equal(before, after) {
		t.Fatal("capture file changed on a refused record")
	}
}

func TestRecord_MissingSummary_RefusedWithNamedError(t *testing.T) {
	r := validRecord(7)
	r.Summary = "  "
	assertRefusedUnchanged(t, r, "summary")
}

func TestRecord_UnknownPriority_RefusedWithNamedError(t *testing.T) {
	r := validRecord(7)
	r.Summary = "different"
	r.SuggestedPriority = "P2"
	assertRefusedUnchanged(t, r, "suggested_priority")
}

func TestRecord_FieldBounds(t *testing.T) {
	cases := map[string]func(*Record){
		"summary":                   func(r *Record) { r.Summary = "a\nb" },
		"evidence":                  func(r *Record) { r.Evidence = nil },
		"source_stage":              func(r *Record) { r.SourceStage = "producer:Bad_Name" },
		"suggested_type":            func(r *Record) { r.SuggestedType = "refactor" },
		"suggested_size":            func(r *Record) { r.SuggestedSize = "XXL" },
		"candidate_target_files[0]": func(r *Record) { r.CandidateTargetFiles = []string{"../etc/passwd"} },
	}
	for field, mut := range cases {
		r := validRecord(7)
		mut(&r)
		r.ID = ID(r.SourceStage, r.Summary)
		r.RecordedAt = now.Format(time.RFC3339)
		if err := Validate(&r); err == nil || !strings.HasPrefix(err.Error(), field+":") {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	r := validRecord(7)
	r.SourceStage = "producer:post-merge-impact-set"
	r.ID = ID(r.SourceStage, r.Summary)
	r.RecordedAt = now.Format(time.RFC3339)
	if err := Validate(&r); err != nil {
		t.Errorf("producer source refused: %v", err)
	}
}

func TestRecord_PathSafety(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "a/../../b", "..", "-rf", "a\x00b", strings.Repeat("a", 257)} {
		if err := validateTargetPath(p); err == nil {
			t.Errorf("%q accepted", p)
		}
	}
	for _, p := range []string{"a/b.go", "a/../b.go", "./x"} {
		if err := validateTargetPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	dir := t.TempDir()
	if _, err := CapturePath(dir, 0); err == nil {
		t.Error("issue 0 accepted")
	}
	if _, err := CapturePath(filepath.Join(dir, "missing"), 1); err == nil {
		t.Error("missing run dir accepted")
	}
}

// A symlinked run dir resolves to its target, and a symlinked capture file
// (which would redirect the write elsewhere) is refused.
func TestRecord_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	p, err := CapturePath(link, 5)
	if err != nil {
		t.Fatal(err)
	}
	realOutside, _ := filepath.EvalSymlinks(outside)
	if filepath.Dir(p) != realOutside {
		t.Errorf("path %s not under resolved %s", p, realOutside)
	}
	target := filepath.Join(outside, "victim")
	if err := os.Symlink(target, filepath.Join(dir, "discovered-work-5.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Append(dir, validRecord(5), now); err == nil {
		t.Fatal("append through a symlinked capture file succeeded")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("symlink target was written")
	}
}

func TestRecord_IdempotentReRecord(t *testing.T) {
	dir := t.TempDir()
	first, added, err := Append(dir, validRecord(3), now)
	if err != nil || !added {
		t.Fatalf("first: %v %v", added, err)
	}
	second, added, err := Append(dir, validRecord(3), now.Add(time.Hour))
	if err != nil || added {
		t.Fatalf("second: added=%v err=%v", added, err)
	}
	if second.RecordedAt != first.RecordedAt {
		t.Error("re-record replaced the original")
	}
	c, err := Load(dir, 3)
	if err != nil || len(c.Records) != 1 {
		t.Fatalf("records = %v, %v", c, err)
	}
}

func TestRecord_ConcurrentAppends(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := validRecord(4)
			r.Summary = "item " + string(rune('a'+i))
			if _, _, err := Append(dir, r, now); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	c, err := Load(dir, 4)
	if err != nil || len(c.Records) != 8 {
		t.Fatalf("got %v records, err %v", c, err)
	}
}

func TestLoad_CorruptFileRefused(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(dir, 9); c != nil || err != nil {
		t.Fatalf("absent file: %v %v", c, err)
	}
	p, _ := CapturePath(dir, 9)
	os.WriteFile(p, []byte(`{"schema_version":"1.0","issue_number":9,"records":[],"extra":1}`), 0o644)
	if _, err := Load(dir, 9); err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("unknown key: %v", err)
	}
	os.WriteFile(p, []byte(`{"schema_version":"1.0","issue_number":8,"records":[]}`), 0o644)
	if _, err := Load(dir, 9); err == nil || !strings.Contains(err.Error(), "issue_number") {
		t.Fatalf("mismatched issue: %v", err)
	}
}

// TestCaptureFile_SurvivesPRMergeCleanupPatterns replays the post-merge
// cleanup patterns (docs/CONTEXT_ARCHITECTURE.md § Cleanup and the pr-merge
// skill's batch cleanup) with N=9999 and asserts only the capture file
// survives. A pattern that starts matching it breaks this test (ADR 027).
func TestCaptureFile_SurvivesPRMergeCleanupPatterns(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	plans := filepath.Join(dir, "plans")
	os.Mkdir(plans, 0o755)
	victims := []string{
		"issue-9999.json", "planning-9999.json", "ac-reconcile-9999.json", "dev-9999.json",
		"validate-9999.json", "validate-9999-checklist.md", "pr-9999.json", "running-x-9999.json",
		"feedback-9999.json", "conflict-context-9999.json",
		"batch-9999.json", "planning-batch-9999.json", "dev-batch-9999.json",
	}
	for _, v := range victims {
		os.WriteFile(filepath.Join(dir, v), []byte("{}"), 0o644)
	}
	os.WriteFile(filepath.Join(plans, "9999-x.md"), []byte("x"), 0o644)
	seedCapture(t, dir, 9999)

	script := `set -e
P="$1"; L="$2"; N=9999; E=9999
rm -f "$P/issue-$N.json" "$P/planning-$N.json" "$P/ac-reconcile-$N.json" "$P/dev-$N.json"
rm -f "$P/validate-$N.json" "$P/pr-$N.json" "$P/feedback-$N.json" "$P/conflict-context-$N.json"
find "$P" -name "validate-$N-*.md" -delete
rm -f "$P"/running-*-$N.json
rm -f "$L/$N-"*.md
rm -f "$P/batch-$E.json" "$P/planning-batch-$E.json" "$P/dev-batch-$E.json" "$P/validate-$E.json" "$P/pr-$E.json"
`
	if out, err := exec.Command("sh", "-c", script, "sh", dir, plans).CombinedOutput(); err != nil {
		t.Fatalf("cleanup replay: %v\n%s", err, out)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	sort.Strings(left)
	want := []string{"discovered-work-9999.json", "discovered-work-9999.json.lock", "plans"}
	if !reflect.DeepEqual(left, want) {
		t.Fatalf("left %v, want %v", left, want)
	}
}

func TestCaptureFile_UnmaterializedIsReported(t *testing.T) {
	dir := t.TempDir()
	seedCapture(t, dir, 9999)
	seedCapture(t, dir, 10)
	os.WriteFile(filepath.Join(dir, "issue-10.json"), []byte("{}"), 0o644)

	got, err := Orphans(dir, now, DefaultOrphanAge)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IssueNumber != 9999 || got[0].Reason != ReasonNoLiveRun {
		t.Fatalf("orphans = %+v", got)
	}
	if _, err := os.Stat(got[0].Path); err != nil {
		t.Fatal("orphan was deleted")
	}

	old := now.Add(-8 * 24 * time.Hour)
	p, _ := CapturePath(dir, 10)
	os.Chtimes(p, old, old)
	got, _ = Orphans(dir, now, DefaultOrphanAge)
	if len(got) != 2 || got[0].Reason != ReasonTooOld {
		t.Fatalf("aged orphans = %+v", got)
	}
}

// The Go and SDK validators must agree on hostile input (parser differential)
// and keep terminal escapes out of printed records.
func TestRecord_RejectsHostileText(t *testing.T) {
	cases := map[string]func(*Record){
		"backslash dotdot": func(r *Record) { r.CandidateTargetFiles = []string{`a\..\..\etc`} },
		"drive prefix":     func(r *Record) { r.CandidateTargetFiles = []string{"C:foo"} },
		"summary ESC":      func(r *Record) { r.Summary = "x\x1b[2Jy" },
		"summary bidi":     func(r *Record) { r.Summary = "x‮y" },
		"evidence C1":      func(r *Record) { r.Evidence = []string{"x\u009by"} },
	}
	for name, mut := range cases {
		r := validRecord(7)
		mut(&r)
		r.ID = ID(r.SourceStage, r.Summary)
		r.RecordedAt = now.Format(time.RFC3339)
		if err := Validate(&r); err == nil {
			t.Errorf("%s: Validate accepted hostile input", name)
		}
	}
	r := validRecord(7)
	r.Evidence = []string{"line one\n\tline two"}
	r.ID = ID(r.SourceStage, r.Summary)
	r.RecordedAt = now.Format(time.RFC3339)
	if err := Validate(&r); err != nil {
		t.Errorf("multi-line evidence rejected: %v", err)
	}
}

func TestConsume_KeepsSelectedAndDeletesWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	a, _, err := Append(dir, validRecord(7), now)
	if err != nil {
		t.Fatal(err)
	}
	r2 := validRecord(7)
	r2.Summary = "A second discovery"
	b, _, err := Append(dir, r2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := Consume(dir, 7, func(r Record) bool { return r.ID == b.ID }); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir, 7)
	if err != nil {
		t.Fatal(err)
	}
	if c == nil || len(c.Records) != 1 || c.Records[0].ID != b.ID {
		t.Fatalf("after partial consume: %+v (consumed %s)", c, a.ID)
	}
	if err := Consume(dir, 7, func(Record) bool { return false }); err != nil {
		t.Fatal(err)
	}
	p, _ := CapturePath(dir, 7)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("capture file still present after full consume: %v", err)
	}
	// Missing file: no-op.
	if err := Consume(dir, 7, nil); err != nil {
		t.Fatalf("consume on missing file: %v", err)
	}
}

func TestConsume_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere.json")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "discovered-work-7.json")); err != nil {
		t.Fatal(err)
	}
	if err := Consume(dir, 7, nil); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want symlink refusal", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("symlink target touched: %v", err)
	}
}

func TestReplaceProducer(t *testing.T) {
	const producer = "producer:independent-review"
	ofProducer := func(summary string) Record {
		r := validRecord(7)
		r.SourceStage, r.Summary = producer, summary
		return r
	}
	dir := t.TempDir()
	if _, _, err := Append(dir, validRecord(7), now); err != nil {
		t.Fatal(err)
	}
	summaries := func() []string {
		t.Helper()
		c, err := Load(dir, 7)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		if c != nil {
			for _, r := range c.Records {
				out = append(out, r.SourceStage+"|"+r.Summary)
			}
		}
		return out
	}
	stage := "feature-validate|" + validRecord(7).Summary

	if _, err := ReplaceProducer(dir, 7, producer, []Record{ofProducer("a"), ofProducer("b"), ofProducer("a")}, now); err != nil {
		t.Fatal(err)
	}
	if got, want := summaries(), []string{stage, producer + "|a", producer + "|b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first round = %v, want %v", got, want)
	}

	bad := ofProducer("c")
	bad.SuggestedSize = "huge"
	stray := validRecord(7)
	rejected, err := ReplaceProducer(dir, 7, producer, []Record{bad, ofProducer("b"), stray}, now)
	if err != nil {
		t.Fatal(err)
	}
	if rejected[0] == nil || rejected[1] != nil || rejected[2] == nil {
		t.Fatalf("rejected = %v", rejected)
	}
	if got, want := summaries(), []string{stage, producer + "|b"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second round = %v, want %v", got, want)
	}

	if _, err := ReplaceProducer(dir, 7, producer, nil, now); err != nil {
		t.Fatal(err)
	}
	if got, want := summaries(), []string{stage}; !reflect.DeepEqual(got, want) {
		t.Fatalf("emptied = %v, want %v", got, want)
	}
}

func TestReplaceProducerDeletesAnEmptiedCapture(t *testing.T) {
	dir := t.TempDir()
	r := validRecord(7)
	r.SourceStage = "producer:independent-review"
	if _, err := ReplaceProducer(dir, 7, r.SourceStage, []Record{r}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceProducer(dir, 7, r.SourceStage, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "discovered-work-7.json")); !os.IsNotExist(err) {
		t.Fatalf("capture still exists: %v", err)
	}
}

func TestReplaceProducerRefusesAStage(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := Append(dir, validRecord(7), now); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceProducer(dir, 7, "feature-validate", nil, now); err == nil {
		t.Fatal("a stage's records were replaceable")
	}
	if c, err := Load(dir, 7); err != nil || len(c.Records) != 1 {
		t.Fatalf("capture = %+v, %v", c, err)
	}
}

func TestValidateCriterion(t *testing.T) {
	for name, tc := range map[string]struct {
		criterion string
		ok        bool
	}{
		"absent":       {"", true},
		"one line":     {"`opencode.json` records the newest tested version.", true},
		"untrimmed":    {" padded", false},
		"multiline":    {"a\nb", false},
		"bidi":         {"a‮b", false},
		"control":      {"a\u0007b", false},
		"at the cap":   {strings.Repeat("x", MaxCriterion), true},
		"over the cap": {strings.Repeat("x", MaxCriterion+1), false},
	} {
		t.Run(name, func(t *testing.T) {
			rec := validRecord(7)
			rec.Criterion = tc.criterion
			rec.ID = ID(rec.SourceStage, rec.Summary)
			rec.RecordedAt = "2026-11-11T00:00:00Z"
			err := Validate(&rec)
			if (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "criterion:") {
				t.Fatalf("error does not name the field: %v", err)
			}
		})
	}
}

// TestCriterionIsOmittedWhenEmpty: a record with no criterion keeps the v1
// key set the SDK and older captures carry.
func TestCriterionIsOmittedWhenEmpty(t *testing.T) {
	out, _ := json.Marshal(validRecord(7))
	if strings.Contains(string(out), `"criterion"`) {
		t.Fatalf("empty criterion serialised: %s", out)
	}
}

func TestLiveRunID(t *testing.T) {
	root := gittest.InitRepo(t, t.TempDir(), "-q")
	if got := LiveRunID(root, 7); got != nil {
		t.Fatalf("no run-state, got %s", *got)
	}
	checkout, err := layout.CheckoutDir(root)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := runstate.MarkRunning(checkout, runstate.MarkRunningOptions{IssueNumber: 7, Branch: "feat/7-x"})
	if err != nil {
		t.Fatal(err)
	}
	if got := LiveRunID(root, 7); got == nil || *got != rs.RunID {
		t.Fatalf("got %v, want %s", got, rs.RunID)
	}
	if got := LiveRunID(root, 8); got != nil {
		t.Fatalf("another issue's run-state, got %s", *got)
	}
}
