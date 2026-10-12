package conversation

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/nightgauge/nightgauge/internal/platform"
)

// Retention (#1893, ADR-017 Decision 11). Every finished turn reports a
// retention class in its terminal report: `durable` keeps it in later turns'
// context, and `ephemeral` lets the hosted service's context read drop it
// once it falls out of the conversation's window. The class comes from what
// the turn did, its terminal state and its tool calls, never from the
// model's reply. docs/CONVERSATION_TURNS.md § Retention class documents the
// rule table; a test holds the two equal.

// ToolCall is one tool call a turn's execution made.
type ToolCall struct {
	Name     string
	Mutating bool
}

// TurnOutcome is what a classifier sees of a finished turn.
type TurnOutcome struct {
	// State is the terminal state the turn reports.
	State string
	// ToolCalls are the tool calls its stream recorded, in order.
	ToolCalls []ToolCall
}

// Mutated reports whether the turn called any mutating tool.
func (o TurnOutcome) Mutated() bool {
	return slices.ContainsFunc(o.ToolCalls, func(c ToolCall) bool { return c.Mutating })
}

// Classification is a turn's retention class and the rule that gave it.
type Classification struct {
	Class string
	// Rule is the id of the rule that decided the class: a retentionRules
	// id, ruleNoMatch, or ruleMutationFloor.
	Rule string
}

// Classifier classifies a finished turn. Its answer passes through the
// mutation floor (retain), which it cannot lower.
type Classifier func(TurnOutcome) Classification

// Tool records a retention rule matches.
const (
	toolsAny  = ""     // every tool record
	toolsNone = "none" // the turn called no tool
	toolsSome = "some" // the turn called at least one tool
)

// retentionRule is one row of the rule table: a turn whose terminal state
// and tool record match the row gets its class.
type retentionRule struct {
	id string
	// states are the terminal states the rule matches; empty matches all.
	states []string
	// tools is toolsAny, toolsNone or toolsSome.
	tools string
	class string
}

// retentionRules classify the turns that changed nothing, first match wins.
// Whether a call mutated is not theirs to read: retain applies the mutation
// floor after them. docs/CONVERSATION_TURNS.md § Retention class gives each
// rule's reason.
var retentionRules = []retentionRule{
	{id: "unfinished", states: []string{platform.TurnStateFailed, platform.TurnStateCancelled},
		tools: toolsAny, class: platform.TurnRetentionDurable},
	{id: "no-live-read", states: []string{platform.TurnStateCompleted},
		tools: toolsNone, class: platform.TurnRetentionDurable},
	{id: "live-read", states: []string{platform.TurnStateCompleted},
		tools: toolsSome, class: platform.TurnRetentionEphemeral},
}

const (
	// ruleNoMatch is the class of a turn no rule matched.
	ruleNoMatch = "no-match"
	// ruleMutationFloor is the class of a turn that called a mutating tool.
	ruleMutationFloor = "mutation-floor"
)

func (r retentionRule) matches(o TurnOutcome) bool {
	if len(r.states) > 0 && !slices.Contains(r.states, o.State) {
		return false
	}
	switch r.tools {
	case toolsNone:
		return len(o.ToolCalls) == 0
	case toolsSome:
		return len(o.ToolCalls) > 0
	}
	return true
}

// classifyByRules is the default classifier: the first matching row of
// retentionRules, and durable when none matches.
func classifyByRules(o TurnOutcome) Classification {
	for _, r := range retentionRules {
		if r.matches(o) {
			return Classification{Class: r.class, Rule: r.id}
		}
	}
	return Classification{Class: platform.TurnRetentionDurable, Rule: ruleNoMatch}
}

// retain is the class a finished turn reports. It runs classify, then the
// mutation floor over its answer: a turn that called any mutating tool is
// durable whatever classify said, and any answer but `ephemeral` is durable.
// The floor is code after the classifier, not an instruction to it, so no
// classifier, model-adjacent or not, can drop a state change from later
// turns' context.
func retain(classify Classifier, o TurnOutcome) Classification {
	c := classify(o)
	if o.Mutated() {
		return Classification{Class: platform.TurnRetentionDurable, Rule: ruleMutationFloor}
	}
	if c.Class != platform.TurnRetentionEphemeral {
		c.Class = platform.TurnRetentionDurable
	}
	return c
}

// toolRecord collects the tool calls a turn's stream records, each once.
type toolRecord struct {
	isMutating func(name string) bool
	seen       map[string]bool
	calls      []ToolCall
}

func newToolRecord(isMutating func(string) bool) *toolRecord {
	return &toolRecord{isMutating: isMutating, seen: map[string]bool{}}
}

// toolBlock is a content block that may be a tool call.
type toolBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// isCall reports a block that calls a tool: a client tool, a server tool or
// an MCP tool.
func (b toolBlock) isCall() bool {
	return b.Type == "tool_use" || b.Type == "server_tool_use" || b.Type == "mcp_tool_use"
}

// observe records the tool calls one line of a claude stream-json stream
// carries: the tool blocks of an `assistant` message, and the start of a
// tool block in a partial-message `stream_event`, so a call is recorded as
// soon as it begins even when the turn stops before its message completes.
// Both name the call's id, which records it once. A subagent's calls count
// as the turn's own.
func (r *toolRecord) observe(line string) {
	if !strings.Contains(line, "tool_use") {
		return // no tool block; most lines are text deltas
	}
	var ev struct {
		Type    string `json:"type"`
		Message *struct {
			Content []toolBlock `json:"content"`
		} `json:"message"`
		Event *struct {
			Type         string     `json:"type"`
			ContentBlock *toolBlock `json:"content_block"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil {
		return
	}
	switch {
	case ev.Type == "assistant" && ev.Message != nil:
		for _, b := range ev.Message.Content {
			r.add(b)
		}
	case ev.Type == "stream_event" && ev.Event != nil && ev.Event.Type == "content_block_start" && ev.Event.ContentBlock != nil:
		r.add(*ev.Event.ContentBlock)
	}
}

func (r *toolRecord) add(b toolBlock) {
	if !b.isCall() {
		return
	}
	if b.ID != "" {
		if r.seen[b.ID] {
			return
		}
		r.seen[b.ID] = true
	}
	r.calls = append(r.calls, ToolCall{Name: b.Name, Mutating: r.isMutating(b.Name)})
}
