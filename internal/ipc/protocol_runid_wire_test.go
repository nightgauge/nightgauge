package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

// These are CHARACTERIZATION tests for ADR-017 step 2 ("the wire gains the
// identity; nothing requires it"). The runId/repo/stagePid fields are newly
// added, so "red" here means a compile failure (the field does not exist), not
// a runtime assertion failure. They pin the wire SHAPE only: the server accepts
// and IGNORES runId, resolution stays issue-keyed, and no verb refuses.
// See docs/decisions/017-runtime-identity-keying.md § Implementation tracking.

// TestProtocol_Step2_RunIDMarshalsOnAllFivePipelineParams verifies that every
// one of the five pipeline.* param types carries "runId" on the wire once set.
func TestProtocol_Step2_RunIDMarshalsOnAllFivePipelineParams(t *testing.T) {
	const runID = "run-2026-08-09-abc123"

	cases := []struct {
		name string
		v    interface{}
	}{
		{"setPaused", PipelineSetPausedParams{IssueNumber: 7, Paused: true, RunID: runID}},
		{"notifyStageTransition", PipelineNotifyStageTransitionParams{Repo: "o/r", IssueNumber: 7, Stage: "feature-dev", Status: "running", RunID: runID}},
		{"notifyStageProgress", PipelineNotifyStageProgressParams{Repo: "o/r", IssueNumber: 7, Stage: "feature-dev", RunID: runID}},
		{"notifyComplete", PipelineNotifyCompleteParams{Repo: "o/r", IssueNumber: 7, Success: true, RunID: runID}},
		{"notifyPhaseTransition", PipelineNotifyPhaseTransitionParams{Repo: "o/r", IssueNumber: 7, Stage: "feature-dev", Name: "plan", EventType: "start", RunID: runID}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			if !strings.Contains(string(data), `"runId":"`+runID+`"`) {
				t.Fatalf("%s wire JSON missing runId: %s", tc.name, data)
			}
		})
	}
}

// TestProtocol_Step2_RunIDOmittedWhenEmpty verifies the omitempty contract that
// keeps the generated pipelineSetPaused positional signature backward-compatible
// (the 2-arg Mattermost callers must keep compiling).
func TestProtocol_Step2_RunIDOmittedWhenEmpty(t *testing.T) {
	data, err := json.Marshal(PipelineSetPausedParams{IssueNumber: 7, Paused: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "runId") {
		t.Fatalf("empty RunID should be omitted, got: %s", s)
	}
	if strings.Contains(s, "repo") {
		t.Fatalf("empty Repo should be omitted, got: %s", s)
	}
}

// TestProtocol_Step2_SetPausedCarriesRepo verifies the multi-repo scoping field.
func TestProtocol_Step2_SetPausedCarriesRepo(t *testing.T) {
	data, err := json.Marshal(PipelineSetPausedParams{IssueNumber: 7, Paused: false, Repo: "nightgauge/nightgauge"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"repo":"nightgauge/nightgauge"`) {
		t.Fatalf("setPaused wire JSON missing repo: %s", data)
	}
}

// TestProtocol_Step2_NotifyStageTransitionCarriesStagePid verifies the advisory
// stagePid field (ADR-017 §7.2) rides notifyStageTransition.
func TestProtocol_Step2_NotifyStageTransitionCarriesStagePid(t *testing.T) {
	data, err := json.Marshal(PipelineNotifyStageTransitionParams{Repo: "o/r", IssueNumber: 7, Stage: "feature-dev", Status: "running", StagePid: 4242})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"stagePid":4242`) {
		t.Fatalf("notifyStageTransition wire JSON missing stagePid: %s", data)
	}
}

// TestProtocol_Step2_UnknownRunIDUnmarshalsWithoutError is the accept-and-ignore
// proof: a params payload carrying a now-known runId round-trips into the struct
// without error and preserves the pre-existing (issue-keyed) fields untouched.
// Resolution is still issue-keyed; the server keys on nothing new in step 2.
func TestProtocol_Step2_UnknownRunIDUnmarshalsWithoutError(t *testing.T) {
	raw := []byte(`{"issueNumber":7,"paused":true,"repo":"o/r","runId":"run-xyz"}`)
	var p PipelineSetPausedParams
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal setPaused with runId must not error: %v", err)
	}
	if p.IssueNumber != 7 || !p.Paused {
		t.Fatalf("issue-keyed fields corrupted by runId presence: %+v", p)
	}
	if p.RunID != "run-xyz" {
		t.Fatalf("runId not captured: %+v", p)
	}
}
