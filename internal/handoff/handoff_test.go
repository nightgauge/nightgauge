package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

const populated = `<!-- nightgauge:handoff
repo: acme-api
session: 12
updated: 2026-09-20
tip: 0123abcd
open_issues: 41
open_prs: 2
next: [acme-api#88, acme-api#91]
needs:
  - { from: acme-web, issue: acme-web#17, what: the login redirect }
  - { from: acme-web, what: a token refresh endpoint }
  - acme-web
provides:
  - { to: acme-web, issue: acme-api#90, what: the session cookie }
-->

# Handoff — acme-api
`

var fixedNow = time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)

func codes(fs []Finding) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Code+"/"+f.Severity)
	}
	return out
}

func TestParseHeader_Populated(t *testing.T) {
	h, err := ParseHeader(populated)
	if err != nil {
		t.Fatal(err)
	}
	if h.Repo != "acme-api" || h.Session != "12" || h.Updated != "2026-09-20" {
		t.Fatalf("scalars: %+v", h)
	}
	// A tip with a leading zero must survive verbatim, not as YAML octal.
	if h.Tip != "0123abcd" {
		t.Fatalf("tip = %q", h.Tip)
	}
	if h.OpenIssues == nil || *h.OpenIssues != 41 || h.OpenPRs == nil || *h.OpenPRs != 2 {
		t.Fatalf("counts: %v %v", h.OpenIssues, h.OpenPRs)
	}
	if strings.Join(h.Next, ",") != "acme-api#88,acme-api#91" {
		t.Fatalf("next = %v", h.Next)
	}
	if len(h.Needs) != 2 || len(h.Provides) != 1 || len(h.malformed) != 1 {
		t.Fatalf("rows: needs=%d provides=%d malformed=%v", len(h.Needs), len(h.Provides), h.malformed)
	}
}

func TestParseHeader_NumericTipStaysVerbatim(t *testing.T) {
	h, err := ParseHeader("<!-- nightgauge:handoff\nrepo: x\ntip: 12791854\n-->")
	if err != nil {
		t.Fatal(err)
	}
	if h.Tip != "12791854" {
		t.Fatalf("tip = %q", h.Tip)
	}
}

func TestParseHeader_Missing(t *testing.T) {
	if _, err := ParseHeader("# no header here\n"); !errors.Is(err, ErrNoHeader) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseHeader_NotAMapping(t *testing.T) {
	if _, err := ParseHeader("<!-- nightgauge:handoff\n- a\n- b\n-->"); err == nil {
		t.Fatal("want error for a list header")
	}
}

func TestAssess_Populated(t *testing.T) {
	revs := func(_ context.Context, checkout, tip, ref string) (int, bool, error) {
		if checkout != "/ws/acme-api" || tip != "0123abcd" || ref != "origin/main" {
			t.Fatalf("revs(%q, %q, %q)", checkout, tip, ref)
		}
		return 5, true, nil
	}
	r := Assess(context.Background(),
		[]Input{{Path: "handoffs/acme-api.md", Text: populated, Explicit: true}},
		Options{Now: fixedNow, Checkouts: map[string]string{"acme-api": "/ws/acme-api"}, Revs: revs})

	if len(r.Handoffs) != 1 {
		t.Fatalf("handoffs = %d", len(r.Handoffs))
	}
	e := r.Handoffs[0]
	if e.TipBehind == nil || *e.TipBehind != 5 {
		t.Fatalf("tip_behind = %v", e.TipBehind)
	}
	if e.DaysSinceUpdated == nil || *e.DaysSinceUpdated != 11 {
		t.Fatalf("days = %v", e.DaysSinceUpdated)
	}
	got := strings.Join(codes(r.Findings), " ")
	want := "handoff-stale/finding handoff-stale/finding cross-repo-row-malformed/finding cross-repo-need-unmaterialized/finding"
	if got != want {
		t.Fatalf("findings:\n got %s\nwant %s", got, want)
	}
	if !r.HasFindings() {
		t.Fatal("HasFindings = false")
	}
	if r.Findings[0].Repo != "acme-api" || r.Findings[0].Path != "handoffs/acme-api.md" {
		t.Fatalf("finding attribution: %+v", r.Findings[0])
	}
}

func TestAssess_FreshHandoffHasNoFindings(t *testing.T) {
	text := "<!-- nightgauge:handoff\nrepo: acme-api\nupdated: 2026-09-30\ntip: abc\nneeds: []\nprovides: []\n-->"
	revs := func(context.Context, string, string, string) (int, bool, error) { return 0, true, nil }
	r := Assess(context.Background(), []Input{{Path: "a.md", Text: text, Explicit: true}},
		Options{Now: fixedNow, Checkouts: map[string]string{"acme-api": "/x"}, Revs: revs})
	if len(r.Findings) != 0 || r.HasFindings() {
		t.Fatalf("findings = %v", codes(r.Findings))
	}
}

func TestAssess_Empty(t *testing.T) {
	r := Assess(context.Background(), nil, Options{Now: fixedNow})
	if r.HasFindings() || len(r.Handoffs) != 0 {
		t.Fatalf("report = %+v", r)
	}
	// An empty report is arrays, never null, for a JSON consumer.
	b, _ := json.Marshal(r)
	for _, key := range []string{`"handoffs":[]`, `"skipped":[]`, `"findings":[]`} {
		if !strings.Contains(string(b), key) {
			t.Fatalf("%s missing from %s", key, b)
		}
	}
}

func TestAssess_MissingHeaderExplicitVersusScanned(t *testing.T) {
	r := Assess(context.Background(), []Input{
		{Path: "README.md", Text: "# index"},
		{Path: "x.md", Text: "# x", Explicit: true},
	}, Options{Now: fixedNow})
	if strings.Join(r.Skipped, ",") != "README.md" {
		t.Fatalf("skipped = %v", r.Skipped)
	}
	if got := strings.Join(codes(r.Findings), " "); got != "handoff-header-missing/finding" {
		t.Fatalf("findings = %s", got)
	}
}

func TestAssess_UnmeasuredAndUnresolvableTips(t *testing.T) {
	text := "<!-- nightgauge:handoff\nrepo: acme-api\ntip: deadbeef\n-->"
	in := []Input{{Path: "a.md", Text: text, Explicit: true}}

	// No checkout configured: info, not a finding.
	r := Assess(context.Background(), in, Options{Now: fixedNow})
	if got := strings.Join(codes(r.Findings), " "); got != "handoff-tip-unmeasured/info" || r.HasFindings() {
		t.Fatalf("no checkout: %s", got)
	}

	unresolved := func(context.Context, string, string, string) (int, bool, error) { return 0, false, nil }
	r = Assess(context.Background(), in, Options{Now: fixedNow, Checkouts: map[string]string{"acme-api": "/x"}, Revs: unresolved})
	if got := strings.Join(codes(r.Findings), " "); got != "handoff-tip-unresolvable/finding" {
		t.Fatalf("unresolvable: %s", got)
	}
}

func TestGitRevCounter_RealRepository(t *testing.T) {
	dir := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	commit := func(name string) string {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		gittest.Run(t, dir, "add", name)
		gittest.Run(t, dir, "commit", "-q", "-m", name)
		return gittest.Run(t, dir, "rev-parse", "HEAD")
	}
	first := commit("a")
	commit("b")
	commit("c")

	behind, ok, err := GitRevCounter(context.Background(), dir, first[:8], "main")
	if err != nil || !ok || behind != 2 {
		t.Fatalf("behind=%d ok=%v err=%v", behind, ok, err)
	}
	if _, ok, err := GitRevCounter(context.Background(), dir, "0000000000", "main"); err != nil || ok {
		t.Fatalf("unknown tip: ok=%v err=%v", ok, err)
	}
	if _, _, err := GitRevCounter(context.Background(), dir, first, "origin/main"); err == nil {
		t.Fatal("want an error for a ref that does not resolve")
	}
}

func TestCollectInputs(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"b.md": populated, "a.md": "# a", "notes.txt": "x"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	explicit := filepath.Join(dir, "notes.txt")
	in, err := CollectInputs([]string{dir, explicit})
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 3 || filepath.Base(in[0].Path) != "a.md" || filepath.Base(in[1].Path) != "b.md" || !in[2].Explicit || in[0].Explicit {
		t.Fatalf("inputs = %+v", in)
	}
	if _, err := CollectInputs([]string{filepath.Join(dir, "missing.md")}); err == nil {
		t.Fatal("want an error for a missing path")
	}
}
