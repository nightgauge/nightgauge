package platform

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Action Center resolution consumption — platform → client (ADR 015 §E,
// nightgauge/nightgauge#330). A dashboard resolve does NOT mutate the pipeline
// directly: the platform validates the option server-side, then relays an
// `attention_resolve` command to the originating agent over the existing
// dashboard-trigger → remoteRunId agent-command bus (pipeline_commands + the
// agent command stream, #3557/#3551). This binary — the single authoritative
// writer — consumes that command, re-validates the option client-side (defense
// in depth), applies the resolution through the store's CAS (a request already
// resolved locally wins; the late command is a safe no-op), executes the bound
// verb via the closed registry, and acknowledges over the same
// POST /v1/agents/:agentId/commands/:commandId/ack path. The store's existing
// attention.event push then updates every surface live — no second emitter.

// AttentionResolveCommandType is the platform commandType relayed for a dashboard
// resolve (acme-platform attention-service.ts relayResolution).
const AttentionResolveCommandType = "attention_resolve"

// AttentionResolvePayload is the attention_resolve command payload the platform
// relays (attention-service.ts relayResolution): the resolved request id, the
// chosen option, its bound verb+args (advisory — the writer re-derives the verb
// from the persisted request, never trusting the relayed verb), and actor/steer.
type AttentionResolvePayload struct {
	RequestID string         `json:"requestId"`
	OptionID  string         `json:"optionId"`
	Verb      string         `json:"verb,omitempty"`
	Args      map[string]any `json:"args,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	SteerText string         `json:"steerText,omitempty"`
}

// AttentionResolveOutcome is the result of applying a relayed resolution.
type AttentionResolveOutcome struct {
	// Applied is true when a fresh resolution transitioned the request (CAS won).
	Applied bool
	// AlreadyResolved is true when the request was already terminal — the local
	// resolution won and this command is a safe no-op (ADR 015 §D).
	AlreadyResolved bool
	// VerbErr is set when the resolution applied but the bound verb's side effect
	// failed; the resolution itself is durable and audited (never rolled back).
	VerbErr error
}

// AttentionResolver applies a relayed resolution through the attention store's
// single authoritative writer (CAS) and executes the bound verb via the verb
// registry. Implemented by the IPC server (which holds the store and IS the verb
// executor). Declared here so this package depends on no higher layer.
type AttentionResolver interface {
	ApplyRelayedResolve(ctx context.Context, requestID, optionID, actor, steerText string) (AttentionResolveOutcome, error)
}

// AgentCommandAcker acknowledges an agent-scoped command
// (POST /v1/agents/:agentId/commands/:commandId/ack). CommandService satisfies it
// via AcknowledgeAgentCommand.
type AgentCommandAcker func(ctx context.Context, agentID, commandID string) (string, error)

// AttentionCommandConsumer consumes attention_resolve agent-commands and applies
// them locally. It ALWAYS acknowledges a well-formed command exactly once —
// whether the resolution applied, was already-resolved, or was rejected — so a
// consumed command is never redelivered forever; the distinction is logged and
// returned in the outcome.
type AttentionCommandConsumer struct {
	resolver AttentionResolver
	ack      AgentCommandAcker
	agentID  string
}

// NewAttentionCommandConsumer builds a consumer bound to the resolver (the single
// writer), the agent-command ack, and this binary's agent id.
func NewAttentionCommandConsumer(resolver AttentionResolver, ack AgentCommandAcker, agentID string) *AttentionCommandConsumer {
	return &AttentionCommandConsumer{resolver: resolver, ack: ack, agentID: agentID}
}

// Consume applies one command and acknowledges it. It returns a non-nil error
// ONLY for a malformed command (a parse/validation failure of the envelope
// itself) — an apply rejection (unknown option / unregistered verb) is a valid,
// acknowledged outcome, not a transport error, so the poller does not retry it.
func (c *AttentionCommandConsumer) Consume(ctx context.Context, cmd PendingCommand) (AttentionResolveOutcome, error) {
	if cmd.Type != AttentionResolveCommandType {
		return AttentionResolveOutcome{}, nil // not ours — leave for another consumer
	}
	var p AttentionResolvePayload
	if err := json.Unmarshal(cmd.Payload, &p); err != nil {
		c.acknowledge(ctx, cmd.ID) // consume the poison command so it is not redelivered
		return AttentionResolveOutcome{}, fmt.Errorf("attention_resolve: parse payload: %w", err)
	}
	if p.RequestID == "" || p.OptionID == "" {
		c.acknowledge(ctx, cmd.ID)
		return AttentionResolveOutcome{}, fmt.Errorf("attention_resolve: requestId and optionId are required")
	}

	outcome, err := c.resolver.ApplyRelayedResolve(ctx, p.RequestID, p.OptionID, p.Actor, p.SteerText)
	// Acknowledge in every case — the command is consumed exactly once.
	c.acknowledge(ctx, cmd.ID)
	if err != nil {
		// Rejected client-side (defense in depth): the platform should have
		// validated server-side, so a rejection here is logged and acked (as an
		// error outcome) rather than retried into a redelivery loop (ADR 015 §J).
		log.Printf("attention_resolve: rejected id=%s option=%s — acked as error: %v", p.RequestID, p.OptionID, err)
		return AttentionResolveOutcome{}, nil
	}
	switch {
	case outcome.AlreadyResolved:
		log.Printf("attention_resolve: id=%s already resolved locally — acked as already-resolved (local resolution wins)", p.RequestID)
	case outcome.VerbErr != nil:
		log.Printf("attention_resolve: id=%s applied + acked; verb side effect failed (audited, non-fatal): %v", p.RequestID, outcome.VerbErr)
	default:
		log.Printf("attention_resolve: id=%s option=%s applied + verb executed + acked", p.RequestID, p.OptionID)
	}
	return outcome, nil
}

// Execute satisfies platform.CommandExecutor so the consumer can drive the shared
// CommandPoller loop. The poller never treats a returned error as fatal; Consume
// already acknowledges and logs, so Execute always returns nil.
func (c *AttentionCommandConsumer) Execute(ctx context.Context, cmd PendingCommand) error {
	_, _ = c.Consume(ctx, cmd)
	return nil
}

func (c *AttentionCommandConsumer) acknowledge(ctx context.Context, commandID string) {
	if c.ack == nil || c.agentID == "" || commandID == "" {
		return
	}
	if _, err := c.ack(ctx, c.agentID, commandID); err != nil {
		log.Printf("attention_resolve: ack failed agentId=%s commandId=%s: %v", c.agentID, commandID, err)
	}
}

// --- Agent-command SSE transport (GET /v1/agents/:agentId/commands) ----------
//
// The platform delivers agent-scoped commands as a long-lived Server-Sent Events
// stream (acme-platform packages/api/src/routes/agents.ts): it subscribes
// to the redis channel `agent:<id>:commands` and writes each command as an
// `event: command` frame whose `data:` line is the JSON envelope, interspersed
// with `:ping` keepalive comments. There is NO one-shot poll endpoint — SSE is
// the only delivery path (#341 follow-up). This consumer holds the stream open,
// parses frames incrementally, dispatches each `command` event to the consumer,
// and reconnects with capped backoff. Duplicate delivery is safe — the store's
// CAS + idempotency make a re-applied resolution a no-op — which matters because
// the platform replays un-acked commands on reconnect.

// attentionStreamInitialBackoff / attentionStreamMaxBackoff bound the reconnect
// backoff: it doubles from the initial value, is capped at the max, and resets
// to the initial value after a frame is received on a healthy connection.
const (
	attentionStreamInitialBackoff = 1 * time.Second
	attentionStreamMaxBackoff     = 30 * time.Second
)

// attentionStreamHTTPClient has no request timeout — the command stream is
// long-lived, so a Client.Timeout would sever a healthy idle connection. Idle
// death is handled by the platform's `:ping` keepalive plus the reconnect loop.
var attentionStreamHTTPClient = &http.Client{Timeout: 0}

// agentCommandEnvelope is one SSE `data:` frame — the command the platform
// relays (attention-service.ts relayResolution). It tolerates both the
// id/commandId and type/commandType spellings the platform uses.
type agentCommandEnvelope struct {
	ID          string          `json:"id"`
	CommandID   string          `json:"commandId"`
	Type        string          `json:"type"`
	CommandType string          `json:"commandType"`
	Payload     json.RawMessage `json:"payload"`
	CreatedAt   time.Time       `json:"createdAt"`
}

func (e agentCommandEnvelope) toPending() PendingCommand {
	id := e.ID
	if id == "" {
		id = e.CommandID
	}
	typ := e.Type
	if typ == "" {
		typ = e.CommandType
	}
	return PendingCommand{ID: id, Type: typ, Payload: e.Payload, CreatedAt: e.CreatedAt}
}

// StartAttentionCommandStream opens the agent-command SSE stream and dispatches
// each attention_resolve command to the consumer, reconnecting with capped
// backoff until ctx is cancelled. On an HTTP 404 (the agent no longer exists —
// evicted after its TTL) it invokes onAgentGone once and returns, so the caller
// can re-register and restart the stream against the new agent id. onAgentGone
// may be nil.
func StartAttentionCommandStream(ctx context.Context, client *Client, consumer *AttentionCommandConsumer, agentID string, onAgentGone func()) {
	if client == nil || consumer == nil || agentID == "" {
		return
	}
	go runAttentionCommandStream(ctx, client, consumer, agentID, onAgentGone, attentionStreamInitialBackoff, attentionStreamMaxBackoff)
}

// runAttentionCommandStream is the reconnect loop. initialBackoff/maxBackoff are
// parameters (not the package consts) so tests can drive it with tiny delays.
func runAttentionCommandStream(ctx context.Context, client *Client, consumer *AttentionCommandConsumer, agentID string, onAgentGone func(), initialBackoff, maxBackoff time.Duration) {
	backoff := initialBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		gone, gotFrame, err := streamAgentCommands(ctx, client, consumer, agentID)
		if gone {
			// Only signal if we are still the live stream — a caller-driven
			// cancel (ctx done) races with a genuine 404 and must not re-fire.
			if ctx.Err() == nil && onAgentGone != nil {
				onAgentGone()
			}
			return
		}
		if gotFrame {
			backoff = initialBackoff // healthy connection — reset backoff
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("attention command stream: disconnected (agent=%s), reconnecting in %s: %v", agentID, backoff, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff = backoff * 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// streamAgentCommands opens ONE SSE connection and dispatches command frames
// until the stream ends, ctx is cancelled, or an error occurs. It returns
// (agentGone, gotFrame, err): agentGone is true on HTTP 404; gotFrame is true if
// at least one command frame was dispatched (used to reset the reconnect
// backoff). A ctx-cancelled read is not an error.
func streamAgentCommands(ctx context.Context, client *Client, consumer *AttentionCommandConsumer, agentID string) (agentGone bool, gotFrame bool, err error) {
	endpoint := client.base + "/v1/agents/" + url.PathEscape(agentID) + "/commands"
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if reqErr != nil {
		return false, false, fmt.Errorf("attention command stream: request: %w", reqErr)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if client.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+client.apiKey)
	}

	resp, doErr := attentionStreamHTTPClient.Do(req)
	if doErr != nil {
		if ctx.Err() != nil {
			return false, false, nil // cancelled during connect
		}
		return false, false, fmt.Errorf("attention command stream: connect: %w", doErr)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return true, false, nil
	}
	if resp.StatusCode >= 400 {
		return false, false, fmt.Errorf("attention command stream: server returned %d", resp.StatusCode)
	}

	reader := bufio.NewReader(resp.Body)
	var eventType string
	var data strings.Builder
	// dispatch delivers the accumulated frame at an event boundary (blank line)
	// and resets the frame accumulators.
	dispatch := func() {
		event := eventType
		payload := data.String()
		eventType = ""
		data.Reset()
		if event != "command" || payload == "" {
			return
		}
		var env agentCommandEnvelope
		if e := json.Unmarshal([]byte(payload), &env); e != nil {
			log.Printf("attention command stream: skipping unparseable frame: %v", e)
			return
		}
		cmd := env.toPending()
		if cmd.ID == "" || cmd.Type == "" {
			return
		}
		gotFrame = true
		_ = consumer.Execute(ctx, cmd)
	}

	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			applySSELine(strings.TrimRight(line, "\r\n"), &eventType, &data, dispatch)
		}
		if readErr != nil {
			if ctx.Err() != nil || readErr == io.EOF {
				// Clean close / cancel: do NOT dispatch a partial trailing frame.
				return false, gotFrame, nil
			}
			return false, gotFrame, fmt.Errorf("attention command stream: read: %w", readErr)
		}
	}
}

// applySSELine folds one SSE line into the in-progress frame: a blank line ends
// the event (dispatch), a ':'-prefixed line is a comment/keepalive (ignored),
// and a `field: value` line updates the event type or appends a data line.
func applySSELine(line string, eventType *string, data *strings.Builder, dispatch func()) {
	switch {
	case line == "":
		dispatch()
	case strings.HasPrefix(line, ":"):
		// comment / keepalive (":ping") — ignore.
	default:
		field, value := splitSSEField(line)
		switch field {
		case "event":
			*eventType = value
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		}
		// "id" / "retry" / unknown fields are ignored.
	}
}

// splitSSEField splits a `field: value` SSE line, stripping a single optional
// leading space from the value (per the SSE spec). A line with no colon is a
// field name with an empty value.
func splitSSEField(line string) (field, value string) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return line, ""
	}
	return line[:idx], strings.TrimPrefix(line[idx+1:], " ")
}
