package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nightgauge/nightgauge/internal/state"
)

const testRemoteRunID = "0190a1b2-c3d4-7e5f-8a9b-0c1d2e3f4a5b"

// remoteRunServer records the one body ReportRemoteRunTerminal posts.
func remoteRunServer(t *testing.T, status int) (*Client, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("body is not JSON: %s", raw)
		}
		body["_path"] = r.URL.Path
		bodies = append(bodies, body)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	c, err := NewClient(Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	c.setMode(ModeOnline)
	return c, &bodies
}

func TestReportRemoteRunTerminal_Body(t *testing.T) {
	for _, tc := range []struct {
		status      RemoteRunStatus
		wantSuccess any // nil means the key is absent
	}{
		{RemoteRunSuccess, true},
		{RemoteRunFailure, false},
		{RemoteRunCancelled, nil},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			c, bodies := remoteRunServer(t, http.StatusAccepted)
			if err := c.ReportRemoteRunTerminal(context.Background(), testRemoteRunID, tc.status, state.VisibilityPrivate); err != nil {
				t.Fatalf("ReportRemoteRunTerminal: %v", err)
			}
			if len(*bodies) != 1 {
				t.Fatalf("posts = %d, want 1", len(*bodies))
			}
			b := (*bodies)[0]
			if b["_path"] != "/v1/pipelines/events" {
				t.Errorf("path = %v, want /v1/pipelines/events", b["_path"])
			}
			if b["type"] != "pipeline_done" || b["runId"] != testRemoteRunID {
				t.Errorf("type/runId = %v/%v", b["type"], b["runId"])
			}
			if b["visibility"] != state.VisibilityPrivate {
				t.Errorf("visibility = %v, want private", b["visibility"])
			}
			got, present := b["success"]
			if tc.wantSuccess == nil {
				if present {
					t.Errorf("success = %v, want absent for cancelled", got)
				}
			} else if got != tc.wantSuccess {
				t.Errorf("success = %v, want %v", got, tc.wantSuccess)
			}
		})
	}
}

func TestReportRemoteRunTerminal_RejectsBadInput(t *testing.T) {
	c, bodies := remoteRunServer(t, http.StatusAccepted)
	for _, id := range []string{"", "run-1", "cmd-abc"} {
		if err := c.ReportRemoteRunTerminal(context.Background(), id, RemoteRunSuccess, ""); !errors.Is(err, ErrInvalidRemoteRunID) {
			t.Errorf("id %q: err = %v, want ErrInvalidRemoteRunID", id, err)
		}
	}
	if err := c.ReportRemoteRunTerminal(context.Background(), testRemoteRunID, "done", ""); err == nil {
		t.Error("unknown status: want an error")
	}
	if len(*bodies) != 0 {
		t.Errorf("posts = %d, want 0 for refused input", len(*bodies))
	}
}

func TestReportRemoteRunTerminal_ServerRefusalIsAnError(t *testing.T) {
	c, _ := remoteRunServer(t, http.StatusBadRequest)
	if err := c.ReportRemoteRunTerminal(context.Background(), testRemoteRunID, RemoteRunFailure, ""); err == nil {
		t.Error("a 400 must be returned as an error")
	}
}

func TestIsPermanentReportError(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{ErrInvalidRemoteRunID, true},
		{fmt.Errorf("x: %w", errUnknownRemoteRunStatus), true},
		{&eventRejectedError{status: 403}, true},
		{errors.New("503"), false},
	} {
		if got := IsPermanentReportError(tc.err); got != tc.want {
			t.Errorf("IsPermanentReportError(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestReportRemoteRunStart_Body(t *testing.T) {
	for _, vis := range []string{state.VisibilityPrivate, ""} {
		t.Run("vis="+vis, func(t *testing.T) {
			c, bodies := remoteRunServer(t, http.StatusAccepted)
			if err := c.ReportRemoteRunStart(context.Background(), testRemoteRunID, "Acme/Widgets", 7, vis); err != nil {
				t.Fatalf("ReportRemoteRunStart: %v", err)
			}
			if len(*bodies) != 1 {
				t.Fatalf("posts = %d, want 1", len(*bodies))
			}
			b := (*bodies)[0]
			want := map[string]any{
				"_path": "/v1/pipelines/events", "type": "stage_started", "runId": testRemoteRunID,
				"repo": "Acme/Widgets", "issueNumber": float64(7), "stage": "issue-pickup", "origin": "local_cli",
			}
			if ts, _ := b["timestamp"].(string); ts == "" {
				t.Error("timestamp is missing")
			}
			for k, v := range want {
				if b[k] != v {
					t.Errorf("%s = %v, want %v", k, b[k], v)
				}
			}
			got, present := b["visibility"]
			if vis == "" {
				if present {
					t.Errorf("visibility = %v, want absent for a team run", got)
				}
			} else if got != vis {
				t.Errorf("visibility = %v, want %v", got, vis)
			}
		})
	}
}

func TestReportRemoteRunStart_RejectsBadInput(t *testing.T) {
	c, bodies := remoteRunServer(t, http.StatusAccepted)
	for _, tc := range []struct {
		name, id, repo string
		issue          int
	}{
		{"non-uuid id", "run-1", "acme/widgets", 7},
		{"empty repo", testRemoteRunID, "", 7},
		{"no owner", testRemoteRunID, "/widgets", 7},
		{"no slash", testRemoteRunID, "widgets", 7},
		{"issue 0", testRemoteRunID, "acme/widgets", 0},
		{"negative issue", testRemoteRunID, "acme/widgets", -1},
		{"no name", testRemoteRunID, "acme/", 7},
		{"extra slash", testRemoteRunID, "acme/widgets/x", 7},
		{"blank owner", testRemoteRunID, "  /widgets", 7},
	} {
		err := c.ReportRemoteRunStart(context.Background(), tc.id, tc.repo, tc.issue, "")
		if err == nil || !IsPermanentReportError(err) {
			t.Errorf("%s: err = %v, want a permanent error", tc.name, err)
		}
	}
	if len(*bodies) != 0 {
		t.Errorf("posts = %d, want 0 for refused input", len(*bodies))
	}
}
