// Package orchestrator — dispatcher.go defines the Dispatcher interface and
// its two implementations: LocalDispatcher (wraps AutonomousScheduler's
// existing enqueueItem logic) and CloudDispatcher (HTTP POST to the platform
// /v1/pipeline/dispatch endpoint).
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Dispatcher sends a candidate item to an execution substrate.
// Dispatch is idempotent — callers may retry on transient errors.
// Returns a SlotID that can be used to track execution progress.
type Dispatcher interface {
	Dispatch(ctx context.Context, item CandidateItem) (slotID string, err error)
}

// LocalDispatcher implements Dispatcher by invoking the provided callbacks
// directly on the existing AutonomousScheduler infrastructure.
type LocalDispatcher struct {
	// onDispatch is called when the IPC bridge is available (extension mode).
	onDispatch func(owner, repo string, issueNumber int, title string)

	// onFallback is called when onDispatch is nil (CLI-only mode).
	// It should enqueue the item in the Go scheduler queue.
	onFallback func(ctx context.Context, item CandidateItem)
}

// NewLocalDispatcher constructs a LocalDispatcher with the given callbacks.
// onDispatch may be nil when running in CLI-only mode; onFallback handles
// that case by enqueuing via the Go scheduler.
func NewLocalDispatcher(
	onDispatch func(owner, repo string, issueNumber int, title string),
	onFallback func(ctx context.Context, item CandidateItem),
) *LocalDispatcher {
	return &LocalDispatcher{
		onDispatch: onDispatch,
		onFallback: onFallback,
	}
}

// Dispatch routes the item to the IPC bridge when available, otherwise
// enqueues it in the Go scheduler. Returns a deterministic SlotID derived
// from the repo and issue number.
func (d *LocalDispatcher) Dispatch(ctx context.Context, item CandidateItem) (string, error) {
	slotID := localSlotID(item)
	owner, repo := splitOwnerRepo(item.Repo)

	if d.onDispatch != nil {
		d.onDispatch(owner, repo, item.Number, item.Title)
	} else if d.onFallback != nil {
		d.onFallback(ctx, item)
	}

	return slotID, nil
}

// localSlotID returns a deterministic slot identifier for local dispatches.
// Format: local-{repo}-{issueNumber}, safe to use as a map key.
func localSlotID(item CandidateItem) string {
	repo := strings.ReplaceAll(item.Repo, "/", "-")
	return fmt.Sprintf("local-%s-%d", repo, item.Number)
}

// CloudDispatcher implements Dispatcher by POSTing dispatch requests to the
// platform's /v1/pipeline/dispatch endpoint. Intended for accounts with
// pipeline.executor: cloud in their config.
type CloudDispatcher struct {
	platformBaseURL string
	accountID       string
	token           string
	httpClient      *http.Client
}

// NewCloudDispatcher constructs a CloudDispatcher.
// platformBaseURL must not have a trailing slash.
func NewCloudDispatcher(platformBaseURL, accountID, token string) *CloudDispatcher {
	return &CloudDispatcher{
		platformBaseURL: strings.TrimRight(platformBaseURL, "/"),
		accountID:       accountID,
		token:           token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// dispatchRequest is the JSON body sent to the platform dispatch endpoint.
type dispatchRequest struct {
	IssueNumber int    `json:"issueNumber"`
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	AccountID   string `json:"accountId"`
	Executor    string `json:"executor"`
}

// dispatchResponse is the JSON body returned by the platform dispatch endpoint.
type dispatchResponse struct {
	SlotID string `json:"slotId"`
}

// Dispatch posts a dispatch request to the platform and returns the SlotID
// from the response. Retries once on 5xx errors before returning an error.
func (d *CloudDispatcher) Dispatch(ctx context.Context, item CandidateItem) (string, error) {
	body, err := json.Marshal(dispatchRequest{
		IssueNumber: item.Number,
		Repo:        item.Repo,
		Title:       item.Title,
		AccountID:   d.accountID,
		Executor:    "cloud",
	})
	if err != nil {
		return "", fmt.Errorf("dispatcher: marshal request: %w", err)
	}

	url := d.platformBaseURL + "/v1/pipeline/dispatch"

	const maxAttempts = 2
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		slotID, err := d.doRequest(ctx, url, body)
		if err == nil {
			return slotID, nil
		}
		lastErr = err
		if attempt < maxAttempts {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}
	return "", lastErr
}

func (d *CloudDispatcher) doRequest(ctx context.Context, url string, body []byte) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("dispatcher: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.token)

	resp, err := d.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dispatcher: http request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 500 {
		return "", fmt.Errorf("dispatcher: platform returned %d: %s", resp.StatusCode, string(respBody))
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("dispatcher: platform returned %d: %s", resp.StatusCode, string(respBody))
	}

	var dr dispatchResponse
	if err := json.Unmarshal(respBody, &dr); err != nil {
		return "", fmt.Errorf("dispatcher: unmarshal response: %w", err)
	}
	if dr.SlotID == "" {
		return "", fmt.Errorf("dispatcher: platform returned empty slotId")
	}
	return dr.SlotID, nil
}
