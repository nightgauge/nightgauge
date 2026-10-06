// Tests that a run started private (#2400) carries `visibility: private` on
// its first stage event and on every later upload of the run, and that a team
// run carries no visibility at all. The live events and the completion record
// are captured off a mock platform, so removing the field from either wire
// fails a test here.
package ipc

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/platform"
)

type capturedUpload struct {
	path string
	body map[string]interface{}
}

// visibilityPlatform is a mock platform that records every pipeline event and
// completion record it receives, one decoded object per upload.
type visibilityPlatform struct {
	mu      sync.Mutex
	uploads []capturedUpload
	arrived chan struct{}
	// status answers GET /v1/pipelines/{issue}/status; nil answers 404.
	status map[string]interface{}
}

func newVisibilityPlatform(t *testing.T) (*visibilityPlatform, *httptest.Server) {
	t.Helper()
	vp := &visibilityPlatform{arrived: make(chan struct{}, 64)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v1/pipelines/events" && r.Method == http.MethodPost:
			vp.record(t, r, false)
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/v1/telemetry/pipeline-run" && r.Method == http.MethodPost:
			vp.record(t, r, true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"accepted":1,"rejected":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/pipelines/912/status":
			vp.mu.Lock()
			status := vp.status
			vp.mu.Unlock()
			if status == nil {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"RUN_NOT_FOUND"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(status)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return vp, srv
}

func (vp *visibilityPlatform) record(t *testing.T, r *http.Request, isArray bool) {
	t.Helper()
	raw, _ := io.ReadAll(r.Body)
	var objs []map[string]interface{}
	if isArray {
		if err := json.Unmarshal(raw, &objs); err != nil {
			t.Errorf("%s body is not a record array: %v", r.URL.Path, err)
			return
		}
	} else {
		var one map[string]interface{}
		if err := json.Unmarshal(raw, &one); err != nil {
			t.Errorf("%s body is not an object: %v", r.URL.Path, err)
			return
		}
		objs = append(objs, one)
	}
	vp.mu.Lock()
	for _, o := range objs {
		vp.uploads = append(vp.uploads, capturedUpload{path: r.URL.Path, body: o})
	}
	vp.mu.Unlock()
	vp.arrived <- struct{}{}
}

// waitFor waits until want uploads matching match have arrived.
func (vp *visibilityPlatform) waitFor(t *testing.T, what string, match func(capturedUpload) bool) capturedUpload {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		vp.mu.Lock()
		for _, u := range vp.uploads {
			if match(u) {
				vp.mu.Unlock()
				return u
			}
		}
		vp.mu.Unlock()
		select {
		case <-vp.arrived:
		case <-deadline:
			t.Fatalf("no %s reached the platform within 5s", what)
		}
	}
}

func eventOfType(eventType string) func(capturedUpload) bool {
	return func(u capturedUpload) bool {
		return u.path == "/v1/pipelines/events" && u.body["type"] == eventType
	}
}

func completionRecord(u capturedUpload) bool {
	return u.path == "/v1/telemetry/pipeline-run"
}

func newVisibilityServer(t *testing.T, platformURL string) *Server {
	t.Helper()
	pc, err := platform.NewClient(platform.Config{BaseURL: platformURL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	pc.StartHealthPolling(context.Background())
	t.Cleanup(pc.StopHealthPolling)
	if !pc.IsOnline() {
		t.Fatal("client should be online after the initial health check")
	}
	return NewServer(nil, WithPlatformClient(pc), WithWorkspaceRoot(t.TempDir()), WithTelemetryPolicy(true, true))
}

func callVisibilityMethod(t *testing.T, s *Server, method, params string) interface{} {
	t.Helper()
	out, err := s.methods[method](t.Context(), []byte(params))
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	return out
}

const privateRunID = "01900309-0000-7000-8000-000000000912"

// TestPrivateRun_EveryUploadCarriesVisibility drives a run the extension
// started private through the daemon: the first transition names the
// visibility, the later calls do not, and every upload still says private.
func TestPrivateRun_EveryUploadCarriesVisibility(t *testing.T) {
	vp, srv := newVisibilityPlatform(t)
	s := newVisibilityServer(t, srv.URL)

	// The run's first transition: the slot names the visibility, and the
	// stage start is the run's first platform event.
	callVisibilityMethod(t, s, "pipeline.notifyStageTransition",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","status":"running","runId":"`+privateRunID+`","visibility":"private"}`)
	started := vp.waitFor(t, "stage_started", eventOfType("stage_started"))
	if got := started.body["visibility"]; got != "private" {
		t.Errorf("first stage_started visibility = %v, want private", got)
	}

	// Later calls without the field: the run stays private.
	callVisibilityMethod(t, s, "pipeline.notifyStageProgress",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","inputTokens":10,"runId":"`+privateRunID+`"}`)
	progress := vp.waitFor(t, "stage_progress", eventOfType("stage_progress"))
	if got := progress.body["visibility"]; got != "private" {
		t.Errorf("stage_progress visibility = %v, want private", got)
	}
	callVisibilityMethod(t, s, "pipeline.notifyStageTransition",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","status":"complete","runId":"`+privateRunID+`"}`)
	completed := vp.waitFor(t, "stage_completed", eventOfType("stage_completed"))
	if got := completed.body["visibility"]; got != "private" {
		t.Errorf("stage_completed visibility = %v, want private", got)
	}

	callVisibilityMethod(t, s, "pipeline.notifyComplete",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"success":true,"totalDurationMs":1000,"runId":"`+privateRunID+`"}`)
	done := vp.waitFor(t, "pipeline_done", eventOfType("pipeline_done"))
	if got := done.body["visibility"]; got != "private" {
		t.Errorf("pipeline_done visibility = %v, want private", got)
	}
	record := vp.waitFor(t, "completion record", completionRecord)
	if got := record.body["visibility"]; got != "private" {
		t.Errorf("completion record visibility = %v, want private", got)
	}
}

// TestTeamRun_UploadsCarryNoVisibility: a run nobody marked private sends no
// visibility, which the service reads as team.
func TestTeamRun_UploadsCarryNoVisibility(t *testing.T) {
	vp, srv := newVisibilityPlatform(t)
	s := newVisibilityServer(t, srv.URL)

	callVisibilityMethod(t, s, "pipeline.notifyStageTransition",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","status":"running","runId":"`+privateRunID+`"}`)
	callVisibilityMethod(t, s, "pipeline.notifyComplete",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"success":true,"totalDurationMs":1000,"runId":"`+privateRunID+`"}`)
	vp.waitFor(t, "stage_started", eventOfType("stage_started"))
	vp.waitFor(t, "pipeline_done", eventOfType("pipeline_done"))
	vp.waitFor(t, "completion record", completionRecord)

	vp.mu.Lock()
	defer vp.mu.Unlock()
	for _, u := range vp.uploads {
		if v, ok := u.body["visibility"]; ok {
			t.Errorf("%s %v carries visibility %v; a team run must omit it", u.path, u.body["type"], v)
		}
	}
}

// TestPrivateRun_VisibilityIsNeverLowered: a later transition naming team
// (or anything else) cannot lower a private run.
func TestPrivateRun_VisibilityIsNeverLowered(t *testing.T) {
	vp, srv := newVisibilityPlatform(t)
	s := newVisibilityServer(t, srv.URL)

	callVisibilityMethod(t, s, "pipeline.notifyStageTransition",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","status":"initialized","runId":"`+privateRunID+`","visibility":"private"}`)
	callVisibilityMethod(t, s, "pipeline.notifyStageTransition",
		`{"repo":"nightgauge/acmeapp","issueNumber":912,"stage":"issue-pickup","status":"running","runId":"`+privateRunID+`","visibility":"team"}`)
	started := vp.waitFor(t, "stage_started", eventOfType("stage_started"))
	if got := started.body["visibility"]; got != "private" {
		t.Errorf("stage_started visibility = %v, want private (a team transition must not lower it)", got)
	}
}

// TestGetRunVisibility answers from GET /v1/pipelines/{issue}/status only for
// the asked run.
func TestGetRunVisibility(t *testing.T) {
	vp, srv := newVisibilityPlatform(t)
	s := newVisibilityServer(t, srv.URL)
	params := `{"issueNumber":912,"runId":"` + privateRunID + `"}`

	cases := []struct {
		name   string
		status map[string]interface{}
		want   platform.RunVisibilityResult
	}{
		{"no run yet (404)", nil, platform.RunVisibilityResult{}},
		{"another run of the issue", map[string]interface{}{"runId": "01900309-0000-7000-8000-000000000001", "visibility": "private"}, platform.RunVisibilityResult{}},
		{"the run, private", map[string]interface{}{"runId": privateRunID, "visibility": "private"}, platform.RunVisibilityResult{Found: true, Visibility: "private"}},
		{"the run, team", map[string]interface{}{"runId": privateRunID, "visibility": "team"}, platform.RunVisibilityResult{Found: true, Visibility: "team"}},
		{"the run, no visibility", map[string]interface{}{"runId": privateRunID}, platform.RunVisibilityResult{Found: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vp.mu.Lock()
			vp.status = tc.status
			vp.mu.Unlock()
			got, ok := callVisibilityMethod(t, s, "platform.getRunVisibility", params).(platform.RunVisibilityResult)
			if !ok {
				t.Fatalf("platform.getRunVisibility returned a %T", got)
			}
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestQueueAdd_RefusesUnknownVisibility: the local socket is unauthenticated,
// so a visibility other than team or private is refused at the boundary.
func TestQueueAdd_RefusesUnknownVisibility(t *testing.T) {
	_, srv := newVisibilityPlatform(t)
	s := newVisibilityServer(t, srv.URL)
	_, err := s.methods["queue.add"](t.Context(), []byte(`{"owner":"nightgauge","repo":"acmeapp","issueNumber":912,"visibility":"public"}`))
	if err == nil || !strings.Contains(err.Error(), "visibility") {
		t.Fatalf("queue.add with visibility \"public\": err = %v, want a visibility refusal", err)
	}
}
