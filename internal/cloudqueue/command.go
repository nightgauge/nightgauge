// Package cloudqueue is the daemon's half of a queue item added in the cloud
// (#2440, ADR-018): the hosted service carries an issue added with
// `POST /v1/queue/add` to an agent covering its repository as a `queue_add`
// command on the agent command stream. The daemon adds the issue to its own
// local queue, then acknowledges the command:
//
//   - `applied` once the item is in the local queue;
//   - `already_resolved` when the issue is already queued or running here;
//   - `rejected`, with a reason, when this daemon does not cover the
//     repository or cannot queue the issue.
//
// The local queue then runs it like any other item, and the queue sync that
// adding it starts (`PUT /v1/queue/sync`) lists it as this machine's row,
// which replaces the cloud row.
//
// docs/GO_BINARY.md § "The daemon's platform agent" states this side of the
// contract.
package cloudqueue

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Priorities a cloud queue item may carry, the hosted service's enum.
var priorities = map[string]bool{"critical": true, "high": true, "medium": true, "low": true}

// defaultPriority is the hosted service's default for an item added without
// one.
const defaultPriority = "medium"

// Item is a validated `queue_add` payload: the issue to queue and the
// priority it was added with.
type Item struct {
	Owner       string
	Repo        string
	IssueNumber int
	Priority    string
}

// FullName is "owner/repo".
func (i Item) FullName() string { return i.Owner + "/" + i.Repo }

// String is "owner/repo#N".
func (i Item) String() string { return fmt.Sprintf("%s#%d", i.FullName(), i.IssueNumber) }

// payload is the wire form (QueueAddCommandPayload). workspaceId and
// queueItemId are the hosted service's: it routes by the first and settles
// the cloud row by the command itself, so neither is read here.
type payload struct {
	Type        string `json:"type"`
	Owner       string `json:"owner"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	Priority    string `json:"priority"`
}

// namePattern is the hosted service's pattern for an owner or repository
// name. Nothing outside it (a slash, a line break, an escape) reaches a queue
// key, a path, a log line or an acknowledgement's detail.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validName(v string) bool {
	return namePattern.MatchString(v) && v != "." && v != ".."
}

// ParseItem decodes and validates a `queue_add` payload. The error names the
// field that is wrong and never echoes its value.
func ParseItem(raw json.RawMessage) (Item, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Item{}, fmt.Errorf("the payload is not a queue_add payload")
	}
	if !validName(p.Owner) {
		return Item{}, fmt.Errorf("owner must be 1 to 100 of [A-Za-z0-9_.-]")
	}
	if !validName(p.Repo) {
		return Item{}, fmt.Errorf("repo must be 1 to 100 of [A-Za-z0-9_.-]")
	}
	if p.IssueNumber < 1 {
		return Item{}, fmt.Errorf("issueNumber must be a positive integer")
	}
	if p.Priority == "" {
		p.Priority = defaultPriority
	}
	if !priorities[p.Priority] {
		return Item{}, fmt.Errorf("priority must be critical, high, medium or low")
	}
	return Item{Owner: p.Owner, Repo: p.Repo, IssueNumber: p.IssueNumber, Priority: p.Priority}, nil
}

// idPattern is what an agent or command id may be: both reach a URL path and
// log lines, so nothing that could change either gets through.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validID(v string) bool { return idPattern.MatchString(v) }
