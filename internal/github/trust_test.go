package github

import (
	"context"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/trust"
)

func trustServer(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body string)) *TrustForge {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		handle(w, r, string(b))
	}))
	t.Cleanup(srv.Close)
	return NewTrustForge(NewClientWithURL("ghp_test", srv.URL+"/graphql"))
}

func TestTrustForge_RepoPermissionReadsTheRoleName(t *testing.T) {
	cases := map[string]trust.Level{
		`{"permission":"write","role_name":"maintain","user":{"login":"alice"}}`: trust.LevelMaintain,
		`{"permission":"read","role_name":"triage","user":{"login":"alice"}}`:    trust.LevelTriage,
		`{"permission":"admin","role_name":"admin","user":{"login":"alice"}}`:    trust.LevelAdmin,
		`{"permission":"none","role_name":"","user":{"login":"alice"}}`:          trust.LevelNone,
	}
	for body, want := range cases {
		var path string
		f := trustServer(t, func(w http.ResponseWriter, r *http.Request, _ string) {
			path = r.URL.Path
			_, _ = w.Write([]byte(body))
		})
		got, err := f.RepoPermission(context.Background(), "o", "r", "alice")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if got != want {
			t.Errorf("%s: level = %q, want %q", body, got, want)
		}
		if path != "/repos/o/r/collaborators/alice/permission" {
			t.Errorf("path = %q", path)
		}
	}
}

func TestTrustForge_RepoPermissionErrorsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, `{"message":"Not Found"}`},
		{http.StatusForbidden, `{"message":"Resource not accessible by integration"}`},
		{http.StatusOK, `not json`},
		{http.StatusOK, `{"permission":"admin","role_name":"admin","user":{"login":"mallory"}}`},
	} {
		f := trustServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		})
		if level, err := f.RepoPermission(context.Background(), "o", "r", "alice"); err == nil {
			t.Errorf("%d %s: level %q with no error, want an error", tc.status, tc.body, level)
		}
	}
}

func TestTrustForge_IssueSignals(t *testing.T) {
	f := trustServer(t, func(w http.ResponseWriter, r *http.Request, body string) {
		if r.URL.Path != "/graphql" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		for _, want := range []string{"lastEditedAt", "timelineItems(last: 100, itemTypes: [LABELED_EVENT, UNLABELED_EVENT, RENAMED_TITLE_EVENT])", "... on LabeledEvent"} {
			if !strings.Contains(body, want) {
				t.Errorf("query lacks %q: %s", want, body)
			}
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{
			"author":{"login":"outsider","__typename":"User"},
			"lastEditedAt":"2026-10-01T10:00:00Z",
			"labels":{"nodes":[{"name":"pipeline:approved"},{"name":"type:bug"}]},
			"timelineItems":{"nodes":[
				{"__typename":"LabeledEvent","createdAt":"2026-10-01T11:00:00Z","actor":{"login":"maintainer","__typename":"User"},"label":{"name":"pipeline:approved"}},
				{"__typename":"UnlabeledEvent","createdAt":"2026-10-01T11:30:00Z","actor":{"login":"nightgauge-pipeline","__typename":"Bot"},"label":{"name":"type:bug"}},
				{"__typename":"RenamedTitleEvent","createdAt":"2026-10-01T12:00:00Z","actor":{"login":"outsider","__typename":"User"}}
			]}}}}}`))
	})
	sig, err := f.IssueSignals(context.Background(), "o", "r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Author != "outsider" {
		t.Errorf("author = %q", sig.Author)
	}
	if !sig.BodyEditedAt.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("BodyEditedAt = %v", sig.BodyEditedAt)
	}
	if len(sig.Labels) != 2 || sig.Labels[0] != "pipeline:approved" {
		t.Errorf("labels = %v", sig.Labels)
	}
	if len(sig.Events) != 3 {
		t.Fatalf("events = %+v", sig.Events)
	}
	if e := sig.Events[0]; e.Kind != trust.EventLabeled || e.Actor != "maintainer" || e.Label != "pipeline:approved" {
		t.Errorf("event 0 = %+v", e)
	}
	if e := sig.Events[1]; e.Kind != trust.EventUnlabeled || e.Actor != "nightgauge-pipeline[bot]" {
		t.Errorf("event 1 = %+v, want the bot login to carry [bot]", e)
	}
	if e := sig.Events[2]; e.Kind != trust.EventRenamed || e.Actor != "outsider" {
		t.Errorf("event 2 = %+v", e)
	}
}

func TestTrustForge_IssueSignalsBotAuthorAndMissingIssue(t *testing.T) {
	f := trustServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"author":{"login":"github-actions","__typename":"Bot"},"lastEditedAt":null,"labels":{"nodes":[]},"timelineItems":{"nodes":[]}}}}}`))
	})
	sig, err := f.IssueSignals(context.Background(), "o", "r", 1)
	if err != nil {
		t.Fatal(err)
	}
	if sig.Author != "github-actions[bot]" {
		t.Errorf("author = %q, want github-actions[bot]", sig.Author)
	}
	if !sig.BodyEditedAt.IsZero() {
		t.Errorf("BodyEditedAt = %v, want zero", sig.BodyEditedAt)
	}

	missing := trustServer(t, func(w http.ResponseWriter, _ *http.Request, _ string) {
		_, _ = w.Write([]byte(`{"data":{"repository":{"issue":null}}}`))
	})
	if _, err := missing.IssueSignals(context.Background(), "o", "r", 1); err == nil {
		t.Fatal("a missing issue read as signals, want an error")
	}
}

func TestTrustForge_IssueComments(t *testing.T) {
	f := trustServer(t, func(w http.ResponseWriter, r *http.Request, _ string) {
		if r.URL.Path != "/repos/o/r/issues/3/comments" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"body":"one","created_at":"2026-10-01T10:00:00Z","user":{"login":"alice"}},
			{"body":"two","created_at":"2026-10-01T11:00:00Z","user":{"login":"github-actions[bot]"}},
			{"body":"three","created_at":"2026-10-01T12:00:00Z","user":null}]`))
	})
	cs, err := f.IssueComments(context.Background(), "o", "r", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 3 || cs[0].Author != "alice" || cs[1].Author != "github-actions[bot]" || cs[2].Author != "" {
		t.Fatalf("comments = %+v", cs)
	}
}

// The approval label the gate reads is one the label registry provisions,
// with the description the issue specifies (#2130).
func TestRequiredLabels_IncludeTheApprovalLabel(t *testing.T) {
	for _, l := range RequiredLabels {
		if l.Name == trust.ApprovedLabel {
			if l.Description != "Maintainer approval for the pipeline to work an issue opened by a non-writer" {
				t.Errorf("description = %q", l.Description)
			}
			if l.BlocksRefinement {
				t.Error("pipeline:approved must not block refinement: most issues never carry it")
			}
			return
		}
	}
	t.Fatalf("%q is read by the eligibility gate but is not in RequiredLabels", trust.ApprovedLabel)
}

// Every label write through the shared IssueService refuses the approval
// label, whichever command or stage asked: it resolves the label ids to names
// first and refuses before the mutation. A resolution that fails refuses too
// (#2130 review).
func TestAddLabels_RefusesTheApprovalLabel(t *testing.T) {
	for _, tc := range []struct {
		name     string
		nodes    string
		status   int
		wantErr  bool
		mutation bool
	}{
		{"approval label", `{"data":{"nodes":[{"name":"type:bug"},{"name":"Pipeline:Approved"}]}}`, 200, true, false},
		{"ordinary labels", `{"data":{"nodes":[{"name":"type:bug"}]}}`, 200, false, true},
		{"unresolvable ids", `{"errors":[{"message":"boom"}]}`, 200, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(string(b), "addLabelsToLabelable") {
					mutated = true
					_, _ = w.Write([]byte(`{"data":{"addLabelsToLabelable":{"labelable":{"__typename":"Issue"}}}}`))
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.nodes))
			}))
			defer srv.Close()
			svc := NewIssueService(NewClientWithURL("ghp_test", srv.URL+"/graphql"))
			err := svc.AddLabels(context.Background(), "I_1", []string{"LA_1", "LA_2"})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if mutated != tc.mutation {
				t.Fatalf("mutation sent = %v, want %v", mutated, tc.mutation)
			}
		})
	}
}

// An issue number outside GraphQL's 32-bit Int is refused before any request.
func TestTrustForge_IssueSignalsRejectsAnOutOfRangeNumber(t *testing.T) {
	called := false
	f := trustServer(t, func(http.ResponseWriter, *http.Request, string) { called = true })
	for _, n := range []int{0, -1, math.MaxInt32 + 1} {
		if _, err := f.IssueSignals(context.Background(), "o", "r", n); err == nil {
			t.Errorf("number %d was accepted", n)
		}
	}
	if called {
		t.Error("an out-of-range number reached GitHub")
	}
}
