package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
	"github.com/nightgauge/nightgauge/internal/state"
)

// RunVisibilityResult is what the hosted service says about one run's
// visibility (#2400). Found is false when the service holds no run with the
// asked id for the issue yet: the most recent run it returns for the issue is
// another one, or it has none the caller may read. Visibility is "team" or
// "private" when Found; a surface that asked for private marks the run
// private only when it reads "private" here.
type RunVisibilityResult struct {
	Found      bool   `json:"found"`
	Visibility string `json:"visibility,omitempty"`
}

// runStatusWire is the part of GET /v1/pipelines/{issueNumber}/status this
// read needs: the run the answer describes and who reads it.
type runStatusWire struct {
	RunID      string `json:"runId"`
	Visibility string `json:"visibility"`
}

// GetRunVisibility reads the visibility the service recorded for run runID of
// issue issueNumber (#2400), from GET /v1/pipelines/{issueNumber}/status. That
// route answers with the most recent run for the issue that the caller's
// account owns, so the answer counts only when its run id is runID; any other
// run, or a 404, is Found false and the caller asks again later.
//
// A read, not telemetry: it sends nothing about the run, so it is not behind
// the send gate. An answer that names no visibility is reported as found with
// an empty visibility, which a caller treats as not confirmed.
func (s *AnalyticsService) GetRunVisibility(ctx context.Context, issueNumber int, runID string) (RunVisibilityResult, error) {
	if issueNumber <= 0 || runID == "" {
		return RunVisibilityResult{}, fmt.Errorf("issueNumber and runId are required")
	}
	if !s.client.IsOnline() {
		return RunVisibilityResult{}, fmt.Errorf("run visibility: platform client offline")
	}
	req, err := s.client.newRequest(ctx, requestSpec{
		Op:       api.OpPipelinesGetStatusByIssue,
		PathArgs: []string{strconv.Itoa(issueNumber)},
	})
	if err != nil {
		return RunVisibilityResult{}, fmt.Errorf("create run status request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return RunVisibilityResult{}, fmt.Errorf("run status GET: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return RunVisibilityResult{}, nil
	}
	if resp.StatusCode >= 400 {
		return RunVisibilityResult{}, fmt.Errorf("run status: server returned %d", resp.StatusCode)
	}
	var wire runStatusWire
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&wire); err != nil {
		return RunVisibilityResult{}, fmt.Errorf("decode run status: %w", err)
	}
	if wire.RunID != runID {
		return RunVisibilityResult{}, nil
	}
	visibility := ""
	switch wire.Visibility {
	case state.VisibilityTeam, state.VisibilityPrivate:
		visibility = wire.Visibility
	}
	return RunVisibilityResult{Found: true, Visibility: visibility}, nil
}
