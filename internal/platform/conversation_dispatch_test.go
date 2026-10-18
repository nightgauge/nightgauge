package platform

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A turn dispatches on the turn-bound route, on its lease, with a body that
// names the run and nothing else: no actor, no account, no credential
// choice (#1894).
func TestDispatchTurnRun_SendsOnlyTheRunOnTheLease(t *testing.T) {
	reply := `{"run_id":"run-1","command_id":"cmd-1","repo_full_name":"acme/api","issue_number":12}`
	cs, c := newCardServer(t, http.StatusCreated, reply)
	handle, err := c.DispatchTurnRun(context.Background(), testLease(), TurnRunRequest{Owner: "acme", Repo: "api", IssueNumber: 12})
	if err != nil {
		t.Fatalf("DispatchTurnRun: %v", err)
	}
	if handle != (TurnRunHandle{RunID: "run-1", CommandID: "cmd-1", RepoFullName: "acme/api", IssueNumber: 12}) {
		t.Errorf("handle = %+v", handle)
	}
	if len(cs.reqs) != 1 {
		t.Fatalf("requests = %+v", cs.reqs)
	}
	got := cs.reqs[0]
	if got.method != http.MethodPost || got.path != "/v1/agents/agent-1/turns/turn-1/runs" || got.lease != "s3cret-lease" {
		t.Errorf("request = %+v", got)
	}
	if len(got.body) != 3 || got.body["owner"] != "acme" || got.body["repo"] != "api" || got.body["issue_number"] != float64(12) {
		t.Errorf("body = %v, want exactly owner, repo and issue_number", got.body)
	}
}

// A refusal carries the hosted service's code; a reply that names another
// run is refused, and nothing is sent without a usable lease or target.
func TestDispatchTurnRun_Refusals(t *testing.T) {
	_, c := newCardServer(t, http.StatusForbidden, `{"error":{"code":"OPERATOR_REQUIRED","message":"x"}}`)
	_, err := c.DispatchTurnRun(context.Background(), testLease(), TurnRunRequest{Owner: "acme", Repo: "api", IssueNumber: 12})
	var refused *TurnRequestError
	if !errors.As(err, &refused) || refused.Code != "OPERATOR_REQUIRED" || refused.Status != http.StatusForbidden {
		t.Fatalf("refusal = %v", err)
	}

	for _, reply := range []string{
		`{"run_id":"run-1","repo_full_name":"acme/web","issue_number":12}`,
		`{"run_id":"run-1","repo_full_name":"acme/api","issue_number":13}`,
		`{"run_id":"../x","repo_full_name":"acme/api","issue_number":12}`,
		`{"repo_full_name":"acme/api","issue_number":12}`,
	} {
		_, c := newCardServer(t, http.StatusCreated, reply)
		_, err := c.DispatchTurnRun(context.Background(), testLease(), TurnRunRequest{Owner: "acme", Repo: "api", IssueNumber: 12})
		if err == nil || !strings.Contains(err.Error(), "another run") {
			t.Errorf("reply %s: %v, want refused", reply, err)
		}
	}

	cs, c := newCardServer(t, http.StatusCreated, `{}`)
	for _, run := range []TurnRunRequest{
		{Owner: "acme", Repo: "api"},
		{Owner: "acme/x", Repo: "api", IssueNumber: 1},
		{Owner: "acme", Repo: "../api", IssueNumber: 1},
	} {
		if _, err := c.DispatchTurnRun(context.Background(), testLease(), run); err == nil {
			t.Errorf("%+v was sent", run)
		}
	}
	if _, err := c.DispatchTurnRun(context.Background(), TurnLease{}, TurnRunRequest{Owner: "acme", Repo: "api", IssueNumber: 1}); !errors.Is(err, ErrTurnLeaseInvalid) {
		t.Errorf("no lease: %v", err)
	}
	if len(cs.reqs) != 0 {
		t.Errorf("requests = %+v, want none", cs.reqs)
	}
}

// The context read carries the thread's run handles and statuses, and a
// field the contract does not name is dropped on decode.
func TestReadTurnContext_CarriesRunHandles(t *testing.T) {
	reply := `{"conversation_id":"conv-1","turn_id":"turn-1","messages":[],"omitted_by_class":0,` +
		`"runs":[{"run_id":"run-1","repo_full_name":"acme/api","issue_number":12,"status":"failure",` +
		`"evidence":{"run_id":"run-1","href":"/v1/pipeline-runs/run-1"},"transcript":"SENTINEL-OUTPUT"}]}`
	_, c := newCardServer(t, http.StatusOK, reply)
	read, err := c.ReadTurnContext(context.Background(), testLease())
	if err != nil {
		t.Fatalf("ReadTurnContext: %v", err)
	}
	if len(read.Runs) != 1 {
		t.Fatalf("runs = %+v", read.Runs)
	}
	r := read.Runs[0]
	if r.RunID != "run-1" || r.RepoFullName != "acme/api" || r.IssueNumber == nil || *r.IssueNumber != 12 ||
		r.Status == nil || *r.Status != "failure" || r.Evidence == nil || r.Evidence.Href != "/v1/pipeline-runs/run-1" {
		t.Errorf("run = %+v", r)
	}
}
