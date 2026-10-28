package blogwatch

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	for _, v := range Verdicts {
		if got, err := ParseVerdict(string(v)); err != nil || got != v {
			t.Errorf("ParseVerdict(%q) = %q, %v", v, got, err)
		}
	}
	for _, bad := range []string{"", "adopted", "Rejected", "Already Covered", " Adopted"} {
		if _, err := ParseVerdict(bad); err == nil {
			t.Errorf("ParseVerdict(%q) accepted", bad)
		}
	}
}

func TestAppendValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blog-watch", "decisions.jsonl")
	u := "https://blog.test/eng/a"
	for _, d := range []Decision{
		{URL: u, Verdict: "Maybe", Reason: "r"},
		{URL: u, Verdict: VerdictDeclined},
		{URL: u, Verdict: VerdictAlreadyCovered, Reason: "r"},
		{URL: u, Verdict: VerdictAlreadyFiled, Reason: "r"},
		{URL: u, Verdict: VerdictAdopted, Reason: "r"},
		{URL: "http://blog.test/x", Verdict: VerdictDeclined, Reason: "r"},
	} {
		if err := Append(path, d); err == nil {
			t.Errorf("Append(%+v) accepted", d)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected rows created the log")
	}
	if err := Append(path, Decision{URL: u + "/", Verdict: VerdictAlreadyCovered, Reason: "r", Ref: "docs/X.md"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	var dup *DuplicateError
	if err := Append(path, Decision{URL: u, Verdict: VerdictDeclined, Reason: "again"}); !errors.As(err, &dup) {
		t.Fatalf("duplicate accepted: %v", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("duplicate changed the log")
	}
	log, err := LoadLog(path)
	if err != nil || len(log) != 1 || log[0].URL != u {
		t.Fatalf("log = %+v, %v", log, err)
	}
}

func TestNew(t *testing.T) {
	got := New([]string{"a", "b", "c"}, map[string]bool{"b": true})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("got %v", got)
	}
}
