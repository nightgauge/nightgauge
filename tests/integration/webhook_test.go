//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordedHook captures a single incoming webhook for assertion.
type recordedHook struct {
	EventHeader string
	Body        []byte
}

// hookReceiver is the in-process webhook target. Tests assert on the
// recorded events instead of polling GitLab — which avoids race conditions
// against GitLab's webhook delivery queue.
type hookReceiver struct {
	mu       sync.Mutex
	hooks    []recordedHook
	server   *httptest.Server
	listener net.Listener
}

func startHookReceiver(t *testing.T) *hookReceiver {
	t.Helper()
	r := &hookReceiver{}

	// Bind to 0.0.0.0 so the GitLab container can reach us. The reachable
	// host name varies by platform (see gitlabReachableHost).
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("hook receiver listen: %v", err)
	}
	r.listener = ln

	srv := &httptest.Server{
		Listener: ln,
		Config: &http.Server{
			Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
				_ = req.Body.Close()
				r.mu.Lock()
				r.hooks = append(r.hooks, recordedHook{
					EventHeader: req.Header.Get("X-Gitlab-Event"),
					Body:        body,
				})
				r.mu.Unlock()
				w.WriteHeader(http.StatusOK)
			}),
			ReadHeaderTimeout: 5 * time.Second,
		},
	}
	srv.Start()
	r.server = srv

	t.Cleanup(func() {
		srv.Close()
	})
	return r
}

// reachableURL is the URL the GitLab container should POST to.
func (r *hookReceiver) reachableURL() string {
	port := r.listener.Addr().(*net.TCPAddr).Port
	return "http://" + gitlabReachableHost(port)
}

// waitFor polls recorded hooks until pred returns true or timeout elapses.
func (r *hookReceiver) waitFor(timeout time.Duration, pred func([]recordedHook) bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		snap := append([]recordedHook(nil), r.hooks...)
		r.mu.Unlock()
		if pred(snap) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// registerProjectHook installs a webhook on the seeded project that targets
// the receiver. Returns the hook id so tests can clean it up.
func registerProjectHook(ctx context.Context, projectID int, url string) (int, error) {
	body := map[string]any{
		"url":                     url,
		"push_events":             true,
		"merge_requests_events":   true,
		"note_events":             true,
		"pipeline_events":         true,
		"enable_ssl_verification": false,
	}
	buf, _ := json.Marshal(body)
	endpoint := fmt.Sprintf("%s/api/v4/projects/%d/hooks", gitlabURL, projectID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
	if err != nil {
		return 0, err
	}
	req.Header.Set("PRIVATE-TOKEN", rootToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("register hook: %d: %s", resp.StatusCode, string(rb))
	}
	var created struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(rb, &created); err != nil {
		return 0, err
	}
	return created.ID, nil
}

func deleteProjectHook(ctx context.Context, projectID, hookID int) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%d/hooks/%d", gitlabURL, projectID, hookID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	req.Header.Set("PRIVATE-TOKEN", rootToken)
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// TestGitLabWebhook_NoteHook posts a comment to the seeded MR and verifies a
// Note Hook delivery. Of the three hook types in scope (pipeline, MR,
// note), the note hook has the most deterministic trigger path — pipeline
// hooks require GitLab CI to be enabled (not in scope here) and MR hooks
// require a state transition that depends on the repo content.
func TestGitLabWebhook_NoteHook(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	receiver := startHookReceiver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hookID, err := registerProjectHook(ctx, fixtures.ProjectID, receiver.reachableURL())
	if err != nil {
		t.Fatalf("register webhook: %v", err)
	}
	defer deleteProjectHook(ctx, fixtures.ProjectID, hookID)

	// Post a note to the MR.
	noteBody, _ := json.Marshal(map[string]string{"body": "ci-test comment from webhook test"})
	notesURL := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d/notes",
		gitlabURL, fixtures.ProjectID, fixtures.MRIID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, notesURL, bytes.NewReader(noteBody))
	req.Header.Set("PRIVATE-TOKEN", rootToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("create note: status %d", resp.StatusCode)
	}

	ok := receiver.waitFor(10*time.Second, func(h []recordedHook) bool {
		for _, hk := range h {
			if strings.EqualFold(hk.EventHeader, "Note Hook") {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("Note Hook not received within 10s (received=%d)", len(receiver.hooks))
	}
}

// TestGitLabWebhook_MRHook re-triggers an MR update (state change) and
// confirms the Merge Request hook fires. We touch the description as the
// cheapest state-touching mutation.
func TestGitLabWebhook_MRHook(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	receiver := startHookReceiver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hookID, err := registerProjectHook(ctx, fixtures.ProjectID, receiver.reachableURL())
	if err != nil {
		t.Fatalf("register webhook: %v", err)
	}
	defer deleteProjectHook(ctx, fixtures.ProjectID, hookID)

	mrURL := fmt.Sprintf("%s/api/v4/projects/%d/merge_requests/%d",
		gitlabURL, fixtures.ProjectID, fixtures.MRIID)
	body, _ := json.Marshal(map[string]string{"description": "webhook test " + strconv.FormatInt(time.Now().Unix(), 10)})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, mrURL, bytes.NewReader(body))
	req.Header.Set("PRIVATE-TOKEN", rootToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("update MR: %v", err)
	}
	_ = resp.Body.Close()

	ok := receiver.waitFor(10*time.Second, func(h []recordedHook) bool {
		for _, hk := range h {
			if strings.EqualFold(hk.EventHeader, "Merge Request Hook") {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("Merge Request Hook not received within 10s (received=%d)", len(receiver.hooks))
	}
}

// TestGitLabWebhook_PushHook covers the Push (Pipeline-like) hook path by
// committing a small file on a fresh branch. Push hooks are a stand-in for
// pipeline hooks when GitLab CI is not enabled — they exercise the same
// delivery codepath.
func TestGitLabWebhook_PushHook(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}
	receiver := startHookReceiver(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hookID, err := registerProjectHook(ctx, fixtures.ProjectID, receiver.reachableURL())
	if err != nil {
		t.Fatalf("register webhook: %v", err)
	}
	defer deleteProjectHook(ctx, fixtures.ProjectID, hookID)

	commitURL := fmt.Sprintf("%s/api/v4/projects/%d/repository/commits", gitlabURL, fixtures.ProjectID)
	body, _ := json.Marshal(map[string]any{
		"branch":         "main",
		"commit_message": "webhook test commit",
		"actions": []map[string]any{
			{
				"action":    "create",
				"file_path": fmt.Sprintf("webhook-test-%d.md", time.Now().UnixNano()),
				"content":   "webhook test\n",
			},
		},
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, commitURL, bytes.NewReader(body))
	req.Header.Set("PRIVATE-TOKEN", rootToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	_ = resp.Body.Close()

	ok := receiver.waitFor(10*time.Second, func(h []recordedHook) bool {
		for _, hk := range h {
			if strings.EqualFold(hk.EventHeader, "Push Hook") {
				return true
			}
		}
		return false
	})
	if !ok {
		t.Fatalf("Push Hook not received within 10s (received=%d)", len(receiver.hooks))
	}
}
