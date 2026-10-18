package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// A conversation turn that starts work dispatches a run through this one
// turn-bound route (#1894, ADR-017 Decision 12), on the turn's execution
// lease, and keeps only the handle it returns. The hosted service checks the
// thread's run bound, the workspace throttle and the poster's own GitHub
// write permission; it refuses a turn whose poster is not the host's
// operator; and it addresses the trigger to this same host, which hands it to
// its executor through the path every trigger takes. The request names no
// actor: the hosted service records the turn's poster.

// TurnRunRequest is the run a turn asks for.
type TurnRunRequest struct {
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
}

// TurnRunHandle is a dispatched run's handle: all a conversation keeps of it.
type TurnRunHandle struct {
	RunID        string `json:"run_id"`
	CommandID    string `json:"command_id"`
	RepoFullName string `json:"repo_full_name"`
	IssueNumber  int    `json:"issue_number"`
}

// TurnContextRun is one run handle a context read carries, with the last
// status the hosted service relayed to the thread and, for a failed run, the
// pointer to its evidence. It never carries the run's transcript, stage
// output or diffs.
type TurnContextRun struct {
	RunID        string               `json:"run_id"`
	RepoFullName string               `json:"repo_full_name"`
	IssueNumber  *int                 `json:"issue_number"`
	Status       *string              `json:"status"`
	Evidence     *TurnContextEvidence `json:"evidence"`
}

// TurnContextEvidence points at a failed run's evidence.
type TurnContextEvidence struct {
	RunID string `json:"run_id"`
	Href  string `json:"href"`
}

var (
	// turnRepoPart is one GitHub owner or repository name.
	turnRepoPart = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	// turnRunIDPattern is a run id the reply may carry: one plain token.
	turnRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$`)
)

// maxTurnIssueNumber bounds an issue number a turn may dispatch.
const maxTurnIssueNumber = 1 << 30

// turnRunBodyLimit bounds the dispatch reply a daemon decodes.
const turnRunBodyLimit = 64 << 10

// DispatchTurnRun asks the hosted service for a run of owner/repo#issue from
// the turn the lease holds. A refusal is a *TurnRequestError whose Code is
// the hosted service's (OPERATOR_REQUIRED, THREAD_RUN_LIMIT,
// WORKSPACE_THROTTLED, GITHUB_PERMISSION_DENIED, ...). A reply that names
// another repository or issue, or no usable run id, is refused: the handle
// is kept only when it is the run the turn asked for.
func (c *Client) DispatchTurnRun(ctx context.Context, lease TurnLease, run TurnRunRequest) (TurnRunHandle, error) {
	if !lease.Valid() {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: %w", ErrTurnLeaseInvalid)
	}
	if !turnRepoPart.MatchString(run.Owner) || !turnRepoPart.MatchString(run.Repo) ||
		run.IssueNumber < 1 || run.IssueNumber > maxTurnIssueNumber {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: owner, repo and a positive issue number are required")
	}
	body, err := json.Marshal(run)
	if err != nil {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: %w", err)
	}
	req, err := c.newRequest(ctx, requestSpec{
		Op:       api.OpAgentsDispatchTurnRun,
		PathArgs: []string{lease.agentID, lease.turnID},
		Body:     body,
		Headers: map[string]string{
			"Content-Type":  "application/json",
			"Accept":        "application/json",
			TurnLeaseHeader: lease.secret,
		},
	})
	if err != nil {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: %w", err)
	}
	resp, err := turnHTTPClient.Do(req)
	if err != nil {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return TurnRunHandle{}, turnRefusal("dispatch turn run", resp)
	}
	var out TurnRunHandle
	if err := json.NewDecoder(io.LimitReader(resp.Body, turnRunBodyLimit)).Decode(&out); err != nil {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: decode: %w", err)
	}
	if !turnRunIDPattern.MatchString(out.RunID) || out.IssueNumber != run.IssueNumber ||
		!strings.EqualFold(out.RepoFullName, run.Owner+"/"+run.Repo) {
		return TurnRunHandle{}, fmt.Errorf("dispatch turn run: the reply names another run")
	}
	return out, nil
}
