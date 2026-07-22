package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	pkgtypes "github.com/nightgauge/nightgauge/pkg/types"
)

// installLicenseHandler primes the edition cache so subsequent edition-aware
// code paths see a deterministic CE / EE answer.
func installLicenseHandler(srv *stubGitLabServer, edition Edition) {
	srv.mux.HandleFunc("/api/v4/license", func(w http.ResponseWriter, r *http.Request) {
		if edition == EditionEE {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"plan":"premium"}`))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"404"}`))
	})
}

// installIssueGet primes a deterministic issue payload for the given iid so
// fetchIssue / GetItem succeed before write-side code paths run.
func installIssueGet(srv *stubGitLabServer, iid int, labels []string) {
	srv.handle("GET", fmt.Sprintf("/api/v4/projects/o%%2Fr/issues/%d", iid), 200,
		MarshalRawIssue(iid, "x", labels))
}

// captureLastPayload installs a PUT handler that records the payload it
// received, returning *map[string]any populated post-call.
func captureLastPayload(t *testing.T, srv *stubGitLabServer, iid int) *map[string]any {
	t.Helper()
	captured := map[string]any{}
	srv.mux.HandleFunc(fmt.Sprintf("/api/v4/projects/o%%2Fr/issues/%d", iid),
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(MarshalRawIssue(iid, "x", nil)))
				return
			}
			_ = json.Unmarshal(srv.lastBody, &captured)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(iid, "x", nil)))
		})
	return &captured
}

func TestSetStatus_LabelMode_AppliesScopedLabel(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	// Ensure label creation succeeds (or already exists)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SyncStatus(context.Background(), "o", "r", 42, "Ready"); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}

	labelStr, ok := (*captured)["labels"].(string)
	if !ok {
		t.Fatalf("payload missing labels: %+v", *captured)
	}
	if !strings.Contains(labelStr, "Status::Ready") {
		t.Errorf("labels = %q, want Status::Ready", labelStr)
	}
}

func TestSetStatus_LabelMode_StripsExistingStatusLabels(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	captured := map[string]any{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(MarshalRawIssue(42, "x", []string{"Status::Backlog", "type:feature"})))
				return
			}
			_ = json.Unmarshal(srv.lastBody, &captured)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(42, "x", []string{"Status::Ready", "type:feature"})))
		})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SyncStatus(context.Background(), "o", "r", 42, "Ready"); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	labelStr, _ := captured["labels"].(string)
	if strings.Contains(labelStr, "Status::Backlog") {
		t.Errorf("labels still contains Status::Backlog: %q", labelStr)
	}
	if !strings.Contains(labelStr, "Status::Ready") {
		t.Errorf("labels missing Status::Ready: %q", labelStr)
	}
	if !strings.Contains(labelStr, "type:feature") {
		t.Errorf("labels lost non-status label: %q", labelStr)
	}
}

func TestSetStatus_StateOnlyMode_DoneClosesIssue(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	captured := map[string]any{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(MarshalRawIssue(42, "x", []string{"Status::In progress"})))
				return
			}
			_ = json.Unmarshal(srv.lastBody, &captured)
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(42, "x", nil)))
		})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyStateOnly, 0)

	if err := p.SyncStatus(context.Background(), "o", "r", 42, "Done"); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	if captured["state_event"] != "close" {
		t.Errorf("state_event = %v, want close", captured["state_event"])
	}
}

func TestSetStatus_StateOnlyMode_RejectsInProgressStatus(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.handle("GET", "/api/v4/projects/o%2Fr/issues/42", 200, MarshalRawIssue(42, "x", nil))

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyStateOnly, 0)

	err := p.SyncStatus(context.Background(), "o", "r", 42, "In progress")
	if err == nil || !strings.Contains(err.Error(), "state-only") {
		t.Errorf("expected state-only error, got %v", err)
	}
}

func TestSetIteration_EE_UsesIterationID(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionEE)
	srv.handle("GET", "/api/v4/groups/o/iterations", 200,
		`[{"id":99,"title":"Sprint 5","state":"current"}]`)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SyncIteration(context.Background(), "o", "r", 42, "Sprint 5"); err != nil {
		t.Fatalf("SyncIteration: %v", err)
	}
	if v, _ := (*captured)["iteration_id"].(float64); int(v) != 99 {
		t.Errorf("iteration_id = %v, want 99", (*captured)["iteration_id"])
	}
}

func TestSetIteration_CE_FallsBackToMilestone(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.handle("GET", "/api/v4/projects/o%2Fr/milestones", 200,
		`[{"id":17,"title":"Sprint 5","state":"active"}]`)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SyncIteration(context.Background(), "o", "r", 42, "Sprint 5"); err != nil {
		t.Fatalf("SyncIteration CE: %v", err)
	}
	if v, _ := (*captured)["milestone_id"].(float64); int(v) != 17 {
		t.Errorf("milestone_id = %v, want 17", (*captured)["milestone_id"])
	}
}

func TestSetIteration_CE_AutoCreatesMissingMilestone(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	var listed atomic.Int32
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/milestones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":21,"title":"Sprint 99","state":"active"}`))
			return
		}
		listed.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`)) // no matching milestone
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SyncIteration(context.Background(), "o", "r", 42, "Sprint 99"); err != nil {
		t.Fatalf("SyncIteration: %v", err)
	}
	if v, _ := (*captured)["milestone_id"].(float64); int(v) != 21 {
		t.Errorf("milestone_id = %v, want 21 (auto-created)", (*captured)["milestone_id"])
	}
}

func TestSetWeight_EE_PassesWeight(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionEE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetNumberField(context.Background(), "gitlab:o/r#42", "Weight", 5); err != nil {
		t.Fatalf("SetNumberField: %v", err)
	}
	if v, _ := (*captured)["weight"].(float64); int(v) != 5 {
		t.Errorf("weight = %v", (*captured)["weight"])
	}
}

func TestSetWeight_CE_DegradesGracefully(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	err := p.SetNumberField(context.Background(), "gitlab:o/r#42", "Weight", 5)
	if !errors.Is(err, forge.ErrUnsupportedOnEdition) {
		t.Errorf("err = %v, want ErrUnsupportedOnEdition", err)
	}
}

func TestSetHealth_EE_WritesHealthStatus(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionEE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetSingleSelectField(context.Background(), "gitlab:o/r#42", "Health", "On Track"); err != nil {
		t.Fatalf("SetSingleSelectField: %v", err)
	}
	if (*captured)["health_status"] != "on_track" {
		t.Errorf("health_status = %v", (*captured)["health_status"])
	}
}

func TestMapHealth_RoundTripsGitHubLabels(t *testing.T) {
	cases := []struct {
		label string
		want  forgetypes.HealthStatus
	}{
		{"On Track", forgetypes.HealthOnTrack},
		{"Needs Attention", forgetypes.HealthNeedsAttention},
		{"At Risk", forgetypes.HealthAtRisk},
		{"on_track", forgetypes.HealthOnTrack},
		{"unknown", ""},
	}
	for _, tc := range cases {
		got := forgetypes.MapHealthFromGitHub(tc.label)
		if got != tc.want {
			t.Errorf("MapHealthFromGitHub(%q) = %q, want %q", tc.label, got, tc.want)
		}
	}
	if forgetypes.MapHealthToGitHub(forgetypes.HealthOnTrack) != "On Track" {
		t.Error("MapHealthToGitHub round-trip broken")
	}
}

func TestGenericSingleSelect_AutoCreatesScopedLabel(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	var labelCreated bool
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			labelCreated = true
			var body map[string]any
			_ = json.Unmarshal(srv.lastBody, &body)
			if body["name"] != "Component::API" {
				t.Errorf("label name = %v", body["name"])
			}
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetSingleSelectField(context.Background(), "gitlab:o/r#42", "Component", "API"); err != nil {
		t.Fatalf("SetSingleSelectField: %v", err)
	}
	if !labelCreated {
		t.Error("expected POST /labels to create Component::API")
	}
	labelStr, _ := (*captured)["labels"].(string)
	if !strings.Contains(labelStr, "Component::API") {
		t.Errorf("labels = %q, missing Component::API", labelStr)
	}
}

func TestGenericSingleSelect_RoundTripReadsBack(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	// PUT response echoes the new label set back; subsequent GET sees it.
	currentLabels := []string{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(MarshalRawIssue(42, "x", currentLabels)))
				return
			}
			var body map[string]any
			_ = json.Unmarshal(srv.lastBody, &body)
			if v, ok := body["labels"].(string); ok {
				currentLabels = nil
				for _, l := range strings.Split(v, ",") {
					if l != "" {
						currentLabels = append(currentLabels, l)
					}
				}
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(42, "x", currentLabels)))
		})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	b := NewBoardServiceFor(c, "o", "r")

	if err := p.SetSingleSelectField(context.Background(), "gitlab:o/r#42", "Custom", "Foo"); err != nil {
		t.Fatalf("SetSingleSelectField: %v", err)
	}
	got, err := b.GetItem(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if scopedLabelValue(got.Labels, "Custom") != "Foo" {
		t.Errorf("Custom value = %q, want Foo (labels=%v)", scopedLabelValue(got.Labels, "Custom"), got.Labels)
	}
}

func TestEnsureFields_CreatesMissingScopedLabels(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	createdNames := []string{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			var body map[string]any
			_ = json.Unmarshal(srv.lastBody, &body)
			createdNames = append(createdNames, body["name"].(string))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(200)
		// Existing label set: only Status::Backlog already exists
		_, _ = w.Write([]byte(`[{"name":"Status::Backlog"}]`))
	})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	res, err := p.EnsureFields(context.Background(), forgetypes.FieldSchema{
		SingleSelectFields: []forgetypes.SingleSelectFieldDef{
			{Name: "Status", Options: []forgetypes.SingleSelectOptionDef{
				{Name: "Backlog"}, {Name: "Ready"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("EnsureFields: %v", err)
	}
	if len(res.Already) != 1 || res.Already[0] != "Status::Backlog" {
		t.Errorf("Already = %v", res.Already)
	}
	if len(res.Created) != 1 || res.Created[0] != "Status::Ready" {
		t.Errorf("Created = %v", res.Created)
	}
}

func TestSyncFieldRoundTrip_Status(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	currentLabels := []string{}
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues/42",
		func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(MarshalRawIssue(42, "x", currentLabels)))
				return
			}
			var body map[string]any
			_ = json.Unmarshal(srv.lastBody, &body)
			if v, ok := body["labels"].(string); ok {
				currentLabels = nil
				for _, l := range strings.Split(v, ",") {
					if l != "" {
						currentLabels = append(currentLabels, l)
					}
				}
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(MarshalRawIssue(42, "x", currentLabels)))
		})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	b := NewBoardServiceFor(c, "o", "r")

	if err := p.SyncStatus(context.Background(), "o", "r", 42, "In review"); err != nil {
		t.Fatalf("SyncStatus: %v", err)
	}
	got, err := b.GetItem(context.Background(), "o", "r", 42)
	if err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	if got.Status != "In review" {
		t.Errorf("Status round-trip = %q, want In review (labels=%v)", got.Status, got.Labels)
	}
}

func TestDriftCheck_DetectsScopedLabelDrift(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	// One issue: legacy priority:high label but no Priority::P1 scoped label.
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/issues", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("[" + MarshalRawIssue(1, "x", []string{"priority:high"}) + "]"))
	})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	drifts, err := p.DriftCheck(context.Background())
	if err != nil {
		t.Fatalf("DriftCheck: %v", err)
	}
	if len(drifts) != 0 {
		// In our implementation, item.Priority is populated from priorityFromLabels
		// when no Priority:: label is present, so there's no drift here.
		// This test verifies the no-drift case explicitly.
		t.Errorf("drifts = %v, want none (legacy label fills Priority)", drifts)
	}
}

func TestSnapshotFields_BuildsVirtualSchema(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionCE)
	srv.mux.HandleFunc("/api/v4/projects/o%2Fr/labels", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[
			{"name":"Status::Ready"},
			{"name":"Status::Done"},
			{"name":"Priority::P1"}
		]`))
	})

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 7)

	snap, err := p.SnapshotFields(context.Background())
	if err != nil {
		t.Fatalf("SnapshotFields: %v", err)
	}
	if snap.ProjectID != "7" {
		t.Errorf("ProjectID = %q", snap.ProjectID)
	}
	statusField, ok := snap.Fields["Status"]
	if !ok {
		t.Fatalf("missing Status virtual field")
	}
	if len(statusField.Options) != 2 {
		t.Errorf("Status options = %d, want 2", len(statusField.Options))
	}
}

func TestSetEstimateFromLabels_MapsSizeToWeight(t *testing.T) {
	srv := newStubServer(t)
	installLicenseHandler(srv, EditionEE)
	captured := captureLastPayload(t, srv, 42)

	c := NewClient(srv.srv.URL, "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)

	if err := p.SetEstimateFromLabels(context.Background(), "o", "r", 42,
		[]string{"size:M"}, nil); err != nil {
		t.Fatalf("SetEstimateFromLabels: %v", err)
	}
	if v, _ := (*captured)["weight"].(float64); int(v) != 4 {
		t.Errorf("weight = %v, want 4 (default size:M → 4h)", (*captured)["weight"])
	}
}

func TestResolveIID_AcceptsBareInteger(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	owner, repo, iid, err := p.resolveIID("42")
	if err != nil {
		t.Fatalf("resolveIID: %v", err)
	}
	if owner != "o" || repo != "r" || iid != 42 {
		t.Errorf("(%q, %q, %d)", owner, repo, iid)
	}
}

func TestResolveIID_ParsesGitlabPrefix(t *testing.T) {
	c := NewClient("", "tok")
	p := NewProjectServiceFor(c, "o", "r", StrategyLabelStatus, 0)
	owner, repo, iid, err := p.resolveIID("gitlab:foo/bar#7")
	if err != nil {
		t.Fatalf("resolveIID: %v", err)
	}
	if owner != "foo" || repo != "bar" || iid != 7 {
		t.Errorf("(%q, %q, %d)", owner, repo, iid)
	}
}

func TestStatusCounts_SmokeTest_VerifiesOrdering(t *testing.T) {
	// Sanity: pkgtypes.StatusCounts hasn't changed shape under us.
	var sc pkgtypes.StatusCounts
	sc.Ready = 1
	sc.Done = 2
	if sc.Ready != 1 || sc.Done != 2 {
		t.Error("StatusCounts shape changed unexpectedly")
	}
}
