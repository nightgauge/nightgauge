package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
)

// PagedFixture serves GitHub REST list endpoints page by page, the way the
// real API does: `page` selects the body, per_page must be 100 (#1681), and a
// Link header points at the next page until the last. Paths absent from Pages
// are 404.
type PagedFixture struct {
	T     *testing.T
	Pages map[string][]string // URL path -> page bodies, in order
}

func (f PagedFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	pages, ok := f.Pages[r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if got := r.URL.Query().Get("per_page"); got != "100" {
		f.T.Errorf("%s requested per_page=%q, want 100", r.URL.Path, got)
	}
	page := 1
	if p := r.URL.Query().Get("page"); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > len(pages) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		page = n
	}
	if page < len(pages) {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(page+1))
		next := fmt.Sprintf("https://api.github.com%s?%s", r.URL.Path, q.Encode())
		last := fmt.Sprintf("https://api.github.com%s?per_page=100&page=%d", r.URL.Path, len(pages))
		w.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next", <%s>; rel="last"`, next, last))
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(pages[page-1]))
}

// CheckRunsPage renders a check-runs page; each run is name:status:conclusion.
func CheckRunsPage(total int, runs ...string) string {
	var parts []string
	for _, run := range runs {
		f := strings.SplitN(run, ":", 3)
		conclusion := "null"
		if f[2] != "" {
			conclusion = strconv.Quote(f[2])
		}
		parts = append(parts, fmt.Sprintf(`{"name":%q,"status":%q,"conclusion":%s}`, f[0], f[1], conclusion))
	}
	return fmt.Sprintf(`{"total_count":%d,"check_runs":[%s]}`, total, strings.Join(parts, ","))
}

// SuccessfulRuns returns n completed, successful runs named prefix-01...
func SuccessfulRuns(prefix string, n int) []string {
	runs := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		runs = append(runs, fmt.Sprintf("%s-%02d:completed:success", prefix, i))
	}
	return runs
}

func TestNextPageURL(t *testing.T) {
	cases := map[string]string{
		``: ``,
		`<https://api.github.com/x?page=2>; rel="next", <https://api.github.com/x?page=5>; rel="last"`:  `https://api.github.com/x?page=2`,
		`<https://api.github.com/x?page=1>; rel="prev", <https://api.github.com/x?page=3>; rel="next"`:  `https://api.github.com/x?page=3`,
		`<https://api.github.com/x?page=1>; rel="first", <https://api.github.com/x?page=4>; rel="last"`: ``,
		`garbage`: ``,
	}
	for header, want := range cases {
		if got := nextPageURL(header); got != want {
			t.Errorf("nextPageURL(%q) = %q, want %q", header, got, want)
		}
	}
}

// TestGetIndividualCheckRuns_RequiredCheckOnPageTwo is the #1681 regression:
// 30 runs fill the first page, and the required check is the 31st. A
// single-page read never saw it, so the verdict was NOT-YET forever.
func TestGetIndividualCheckRuns_RequiredCheckOnPageTwo(t *testing.T) {
	srv := httptest.NewServer(PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/commits/abc/check-runs": {
			CheckRunsPage(31, SuccessfulRuns("job", 30)...),
			CheckRunsPage(31, "publication boundary:completed:success"),
		},
	}})
	defer srv.Close()

	checks, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatalf("GetIndividualCheckRuns: %v", err)
	}
	if len(checks) != 31 {
		t.Fatalf("got %d check runs, want all 31 across both pages", len(checks))
	}
	if verdict, reasons := EvaluateCommitChecks(checks, []string{"job-01", "publication boundary"}); verdict != ChecksComplete {
		t.Errorf("verdict = %q (%v), want green: the required check on page 2 was not found", verdict, reasons)
	}
}

func TestGetCommitStatuses_ReadsEveryPage(t *testing.T) {
	page := func(contexts ...string) string {
		var parts []string
		for _, c := range contexts {
			parts = append(parts, fmt.Sprintf(`{"context":%q,"state":"success"}`, c))
		}
		return fmt.Sprintf(`{"sha":"abc","statuses":[%s]}`, strings.Join(parts, ","))
	}
	first := make([]string, 0, 100)
	for i := 0; i < 100; i++ {
		first = append(first, fmt.Sprintf("ctx-%03d", i))
	}
	srv := httptest.NewServer(PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/commits/abc/status": {page(first...), page("cla")},
	}})
	defer srv.Close()

	statuses, err := newCIServiceForRESTTest(srv).GetCommitStatuses(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatalf("GetCommitStatuses: %v", err)
	}
	if len(statuses) != 101 || statuses[100].Name != "cla" {
		t.Fatalf("got %d statuses, want 101 with cla last", len(statuses))
	}
}

func TestGetWorkflowRunsForRef_ReadsEveryPage(t *testing.T) {
	srv := httptest.NewServer(PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/actions/runs": {
			`{"workflow_runs":[{"id":1,"name":"CI","status":"completed","conclusion":"success"}]}`,
			`{"workflow_runs":[{"id":2,"name":"Nightly","status":"in_progress"}]}`,
		},
	}})
	defer srv.Close()

	runs, err := newCIServiceForRESTTest(srv).GetWorkflowRunsForRef(context.Background(), "o", "r", "abc")
	if err != nil {
		t.Fatalf("GetWorkflowRunsForRef: %v", err)
	}
	if len(runs) != 2 || runs[1].Status != "IN_PROGRESS" {
		t.Fatalf("runs = %+v, want both pages with the in-flight run last", runs)
	}
}

func TestGetRequiredCheckNames_ReadsEveryRulesPage(t *testing.T) {
	rule := func(context string) string {
		return fmt.Sprintf(`{"type":"required_status_checks","parameters":{"required_status_checks":[{"context":%q}]}}`, context)
	}
	srv := httptest.NewServer(PagedFixture{T: t, Pages: map[string][]string{
		"/repos/o/r/rules/branches/main": {"[" + rule("build") + "]", "[" + rule("cla") + "]"},
	}})
	defer srv.Close()

	names, err := newCIServiceForRESTTest(srv).GetRequiredCheckNames(context.Background(), "o", "r", "main")
	if err != nil {
		t.Fatalf("GetRequiredCheckNames: %v", err)
	}
	if strings.Join(names, ",") != "build,cla" {
		t.Fatalf("names = %v, want build and cla from both pages", names)
	}
}

// A non-200 on a later page is an error, never a silently shorter list, and
// the forge sentinel survives the wrapping.
func TestGetIndividualCheckRuns_LaterPageErrorIsNotTruncation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Link", `<https://api.github.com/repos/o/r/commits/abc/check-runs?per_page=100&page=2>; rel="next"`)
		_, _ = w.Write([]byte(CheckRunsPage(2, "build:completed:success")))
	}))
	defer srv.Close()

	_, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "abc")
	if !errors.Is(err, forge.ErrUnauthorized) {
		t.Fatalf("err = %v, want forge.ErrUnauthorized from page 2", err)
	}
}

// A forge that keeps answering with a next link cannot hold the caller: a
// repeated link and the page ceiling are both errors.
func TestGetAllPages_Bounded(t *testing.T) {
	t.Run("repeated next link", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Link", `<https://api.github.com/repos/o/r/commits/abc/check-runs?per_page=100>; rel="next"`)
			_, _ = w.Write([]byte(CheckRunsPage(1, "build:completed:success")))
		}))
		defer srv.Close()
		if _, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "abc"); err == nil {
			t.Fatal("a next link pointing at the page just read must be an error")
		}
	})
	t.Run("page ceiling", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n, _ := strconv.Atoi(r.URL.Query().Get("page"))
			w.Header().Set("Link", fmt.Sprintf(`<https://api.github.com/repos/o/r/commits/abc/check-runs?per_page=100&page=%d>; rel="next"`, n+2))
			_, _ = w.Write([]byte(CheckRunsPage(1, "build:completed:success")))
		}))
		defer srv.Close()
		_, err := newCIServiceForRESTTest(srv).GetIndividualCheckRuns(context.Background(), "o", "r", "abc")
		if err == nil || !strings.Contains(err.Error(), "exceeded") {
			t.Fatalf("err = %v, want the page ceiling error", err)
		}
	})
}
