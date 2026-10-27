package finding

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

var now = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func validRecord() Record {
	return Record{
		Kind:     "environment",
		Scope:    "pipeline",
		Summary:  "Gate needs mikefarah yq, not the python yq",
		Remedy:   "brew install yq before scripts/ci-local.sh",
		Evidence: []string{"yq: error: unrecognized arguments"},
		Paths:    []string{"scripts/ci-local.sh"},
	}
}

func TestFinding_RecordAndList(t *testing.T) {
	file := filepath.Join(t.TempDir(), ".nightgauge", "findings.jsonl")
	got, added, err := Append(file, validRecord(), now)
	if err != nil || !added {
		t.Fatalf("append: added=%v err=%v", added, err)
	}
	recs, err := List(file)
	if err != nil {
		t.Fatal(err)
	}
	want := validRecord()
	want.SchemaVersion = "1"
	want.ID = ID("environment", "pipeline", want.Summary)
	want.RecordedAt = "2026-10-26T12:00:00Z"
	if len(recs) != 1 || !reflect.DeepEqual(recs[0], want) || !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", recs, want)
	}
}

func TestFinding_IdempotentReRecord(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	first, _, _ := Append(file, validRecord(), now)
	before, _ := os.ReadFile(file)
	again := validRecord()
	again.Remedy = "different remedy"
	got, added, err := Append(file, again, now.Add(time.Hour))
	if err != nil || added || got.ID != first.ID || got.Remedy != first.Remedy {
		t.Fatalf("re-record: added=%v err=%v got=%+v", added, err, got)
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("re-record rewrote the file")
	}
}

func assertRefused(t *testing.T, rec Record, field string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	if _, _, err := Append(file, validRecord(), now); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(file)
	_, _, err := Append(file, rec, now)
	if err == nil || !strings.HasPrefix(err.Error(), field+":") {
		t.Fatalf("err = %v, want prefix %q", err, field+":")
	}
	after, _ := os.ReadFile(file)
	if !bytes.Equal(before, after) {
		t.Fatal("a refused record changed the file")
	}
}

func TestFinding_UnknownKindRefused(t *testing.T) {
	r := validRecord()
	r.Kind = "bogus"
	assertRefused(t, r, "kind")
}

func TestFinding_MissingSummaryRefused(t *testing.T) {
	r := validRecord()
	r.Summary = ""
	assertRefused(t, r, "summary")
}

func TestFinding_ValidationRules(t *testing.T) {
	cases := map[string]struct {
		mut   func(*Record)
		field string
	}{
		"scope":        {func(r *Record) { r.Scope = "global" }, "scope"},
		"control":      {func(r *Record) { r.Summary = "bad \x1b[31m" }, "summary"},
		"bidi":         {func(r *Record) { r.Remedy = "x‮y" }, "remedy"},
		"multiline":    {func(r *Record) { r.Summary = "a\nb" }, "summary"},
		"long":         {func(r *Record) { r.Summary = strings.Repeat("a", 201) }, "summary"},
		"absolute":     {func(r *Record) { r.Paths = []string{"/etc/passwd"} }, "paths[0]"},
		"dotdot":       {func(r *Record) { r.Paths = []string{"a/../../b"} }, "paths[0]"},
		"dash":         {func(r *Record) { r.Paths = []string{"-rf"} }, "paths[0]"},
		"evidence":     {func(r *Record) { r.Evidence = []string{""} }, "evidence[0]"},
		"too-many":     {func(r *Record) { r.Paths = make([]string, 21) }, "paths"},
		"bad-glob":     {func(r *Record) { r.Paths = []string{"a["} }, "paths[0]"},
		"wrong-id":     {func(r *Record) { r.ID = "fd-000000000000" }, "id"},
		"wrong-schema": {func(r *Record) { r.SchemaVersion = "2" }, "schema_version"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := validRecord()
			c.mut(&r)
			assertRefused(t, r, c.field)
		})
	}
}

func TestFinding_AbsentFileIsEmptyAndNotCreated(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	recs, err := List(file)
	if err != nil || recs == nil || len(recs) != 0 {
		t.Fatalf("list = %v, %v", recs, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("List created the file")
	}
}

func TestFinding_MalformedLineNamesLine(t *testing.T) {
	file := filepath.Join(t.TempDir(), "findings.jsonl")
	if _, _, err := Append(file, validRecord(), now); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(`{"schema_version":"1","extra":true}` + "\n")
	f.Close()
	if _, err := List(file); err == nil || !strings.Contains(err.Error(), ":2:") {
		t.Fatalf("err = %v", err)
	}
}

func TestFinding_SymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, nil, 0o644)
	link := filepath.Join(dir, "findings.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	if _, _, err := Append(link, validRecord(), now); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v", err)
	}
}

func TestFinding_MatchRule(t *testing.T) {
	r := Record{Summary: "s", Remedy: "edit internal/ci/wait.go first", Paths: []string{"scripts", "docs/*.md"}}
	for target, want := range map[string]bool{
		"scripts/ci-local.sh": true,  // directory prefix
		"scripts":             true,  // exact
		"docs/TESTING.md":     true,  // glob
		"internal/ci/wait.go": true,  // substring in remedy
		"scriptsx/run.sh":     false, // not a segment prefix
		"docs/sub/TESTING.md": false, // glob does not cross '/'
		"cmd/nightgauge/x.go": false,
	} {
		if got := Matches(r, []string{target}); got != want {
			t.Errorf("Matches(%q) = %v, want %v", target, got, want)
		}
	}
	recs := []Record{r, {Kind: "gotcha", Scope: "project", Summary: "other", Paths: []string{}}}
	recs[0].Kind, recs[0].Scope = "environment", "pipeline"
	if got := Filter(recs, "", "", []string{"scripts/x"}); len(got) != 1 || got[0].Summary != "s" {
		t.Fatalf("filter by path: %+v", got)
	}
	if got := Filter(recs, "gotcha", "project", nil); len(got) != 1 || got[0].Summary != "other" {
		t.Fatalf("filter by kind/scope: %+v", got)
	}
}

// TestFinding_SchemaKeySetParity pins Record's JSON keys to the committed
// schema's properties and required list.
func TestFinding_SchemaKeySetParity(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "schemas", "finding-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	var goKeys []string
	rt := reflect.TypeOf(Record{})
	for i := 0; i < rt.NumField(); i++ {
		goKeys = append(goKeys, strings.Split(rt.Field(i).Tag.Get("json"), ",")[0])
	}
	var props []string
	for k := range schema.Properties {
		props = append(props, k)
	}
	sort.Strings(goKeys)
	sort.Strings(props)
	req := append([]string(nil), schema.Required...)
	sort.Strings(req)
	if !reflect.DeepEqual(goKeys, props) {
		t.Errorf("properties: schema %v, Go %v", props, goKeys)
	}
	if !reflect.DeepEqual(goKeys, req) {
		t.Errorf("required: schema %v, Go %v", req, goKeys)
	}
}

// TestFinding_PathResolvesToMainCheckout: a linked worktree resolves to the
// main checkout's file, so removing the worktree cannot lose findings (#2535).
func TestFinding_PathResolvesToMainCheckout(t *testing.T) {
	main, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		gittest.Run(t, dir, args...)
	}
	git(main, "init", "-q")
	git(main, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "wt")
	git(main, "worktree", "add", "-q", wt)
	got, err := Path(wt)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(main, ".nightgauge", "findings.jsonl"); got != want {
		t.Fatalf("Path(worktree) = %s, want %s", got, want)
	}
}
