package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// turnServer answers one route with a fixed status and body, and records the
// request it got.
type turnServer struct {
	mu     sync.Mutex
	method string
	path   string
	lease  string
	body   []byte
}

func newTurnServer(t *testing.T, status int, resp string) (*turnServer, *Client) {
	t.Helper()
	ts := &turnServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		ts.mu.Lock()
		ts.method, ts.path, ts.lease, ts.body = r.Method, r.URL.Path, r.Header.Get(TurnLeaseHeader), raw
		ts.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return ts, onlineClient(t, srv.URL)
}

func testLease() TurnLease {
	return TurnLease{agentID: "agent-1", turnID: "turn-1", secret: "s3cret-lease"}
}

// The claiming acknowledgement: an empty body, and a lease bound to the agent
// and the turn the command named.
func TestClaimTurn_ReturnsALeaseBoundToTheTurn(t *testing.T) {
	ts, c := newTurnServer(t, http.StatusOK,
		`{"runId":"turn-1","turn_id":"turn-1","state":"running","failure_reason":null,"lease":"s3cret-lease","lease_header":"X-Nightgauge-Turn-Lease","deadline_at":"2026-10-11T12:10:00.000Z"}`)
	claim, err := NewCommandService(c).ClaimTurn(context.Background(), "agent-1", "cmd-1", "turn-1")
	if err != nil {
		t.Fatalf("ClaimTurn: %v", err)
	}
	if ts.method != http.MethodPost || ts.path != "/v1/agents/agent-1/commands/cmd-1/ack" || string(ts.body) != `{}` {
		t.Errorf("claim sent %s %s %s, want POST .../commands/cmd-1/ack {}", ts.method, ts.path, ts.body)
	}
	if claim.Lease.AgentID() != "agent-1" || claim.Lease.TurnID() != "turn-1" || claim.Lease.secret != "s3cret-lease" {
		t.Errorf("lease = %#v (%s), want bound to agent-1/turn-1", claim.Lease, claim.Lease.secret)
	}
	if claim.State != "running" || claim.DeadlineAt.IsZero() {
		t.Errorf("claim = %+v", claim)
	}
	for _, printed := range []string{claim.Lease.String(), fmt.Sprintf("%v %+v %#v", claim.Lease, claim, claim)} {
		if strings.Contains(printed, "s3cret-lease") {
			t.Errorf("a printed lease carries its secret: %s", printed)
		}
	}
}

// Only a 200 with a usable lease for the named turn authorizes running it.
func TestClaimTurn_RefusalsNeverAuthorizeExecution(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		want     error
		terminal bool
	}{
		{"redelivery", http.StatusConflict, `{"code":"ALREADY_ACKED","message":"x"}`, ErrTurnAlreadyAcked, true},
		{"ended", http.StatusConflict, `{"code":"TURN_ENDED","message":"x"}`, ErrTurnEnded, true},
		{"not ours", http.StatusNotFound, `{"code":"NOT_FOUND","message":"x"}`, ErrTurnNotFound, true},
		{"no lease", http.StatusOK, `{"runId":"turn-1","turn_id":"turn-1","state":"running"}`, ErrTurnClaimMalformed, false},
		{"another turn's lease", http.StatusOK, `{"turn_id":"turn-2","lease":"x"}`, ErrTurnClaimMalformed, false},
		{"a lease no header can carry", http.StatusOK, `{"turn_id":"turn-1","lease":"a b"}`, ErrTurnClaimMalformed, false},
		{"outage", http.StatusInternalServerError, `{}`, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := newTurnServer(t, tc.status, tc.body)
			_, err := NewCommandService(c).ClaimTurn(context.Background(), "agent-1", "cmd-1", "turn-1")
			if err == nil {
				t.Fatal("ClaimTurn succeeded")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			var refused *TurnRequestError
			if got := errors.As(err, &refused) && refused.Terminal(); got != tc.terminal {
				t.Errorf("terminal refusal = %v, want %v (%v)", got, tc.terminal, err)
			}
		})
	}
}

// The context read presents the lease on the lease's own turn route, and a
// reply for another turn is refused.
func TestReadTurnContext_PresentsTheLeaseOnItsOwnTurn(t *testing.T) {
	ts, c := newTurnServer(t, http.StatusOK,
		`{"conversation_id":"conv-1","turn_id":"turn-1","messages":[{"id":"m1","turn_id":"turn-1","position":1,"role":"user","content":"hi","redacted":false,"actor_account_id":"a","target_workspace_id":"w","retention_class":"durable","unfinished_turn_state":null}],"omitted_by_class":0}`)
	got, err := c.ReadTurnContext(context.Background(), testLease())
	if err != nil {
		t.Fatalf("ReadTurnContext: %v", err)
	}
	if ts.method != http.MethodGet || ts.path != "/v1/agents/agent-1/turns/turn-1/context" || ts.lease != "s3cret-lease" {
		t.Errorf("read sent %s %s lease=%q", ts.method, ts.path, ts.lease)
	}
	if len(got.Messages) != 1 || *got.Messages[0].Content != "hi" {
		t.Errorf("context = %+v", got)
	}

	_, other := newTurnServer(t, http.StatusOK, `{"conversation_id":"conv-1","turn_id":"turn-2","messages":[],"omitted_by_class":0}`)
	if _, err := other.ReadTurnContext(context.Background(), testLease()); err == nil {
		t.Error("a context read naming another turn was accepted")
	}
	_, refused := newTurnServer(t, http.StatusForbidden, `{"error":{"code":"LEASE_INVALID","message":"x"}}`)
	if _, err := refused.ReadTurnContext(context.Background(), testLease()); !errors.Is(err, ErrTurnLeaseInvalid) {
		t.Errorf("403 err = %v, want ErrTurnLeaseInvalid", err)
	}
	if _, err := c.ReadTurnContext(context.Background(), TurnLease{}); !errors.Is(err, ErrTurnLeaseInvalid) {
		t.Errorf("an empty lease err = %v", err)
	}
}

// The three frames the daemon may write, exactly as the hosted service's
// ingestion schema takes them: no other field, and the optional ones left out
// when unset.
func TestPostTurnFrames_WritesOnlyTheThreeFrameShapes(t *testing.T) {
	ts, c := newTurnServer(t, http.StatusOK,
		`{"accepted":3,"duplicates":0,"dropped":0,"turn":{"id":"turn-1","state":"completed","failure_reason":null,"delta_count":1}}`)
	res, err := c.PostTurnFrames(context.Background(), testLease(), []TurnFrame{
		TurnDelta{Seq: 1, Text: "hello"},
		TurnReference{Kind: "issue", ObjectID: "o1", WorkspaceID: "w1"},
		TurnEnd{State: TurnStateCompleted,
			Profile:        &TurnExecutionProfile{Adapter: "claude-headless", AdapterDisplayName: "Claude Headless", PerformanceMode: "elevated", Effort: ""},
			Usage:          &TurnUsage{InputTokens: 10, OutputTokens: 2},
			RetentionClass: TurnRetentionDurable},
	})
	if err != nil {
		t.Fatalf("PostTurnFrames: %v", err)
	}
	if ts.path != "/v1/agents/agent-1/turns/turn-1/frames" || ts.lease != "s3cret-lease" {
		t.Errorf("post sent %s lease=%q", ts.path, ts.lease)
	}
	want := `{"frames":[{"type":"turn.delta","seq":1,"text":"hello"},` +
		`{"type":"turn.reference","kind":"issue","object_id":"o1","workspace_id":"w1"},` +
		`{"type":"turn.end","state":"completed","execution_profile":{"adapter":"claude-headless","adapter_display_name":"Claude Headless","performance_mode":"elevated","effort":""},"usage":{"input_tokens":10,"output_tokens":2},"retention_class":"durable"}]}`
	if string(ts.body) != want {
		t.Errorf("body =\n%s\nwant\n%s", ts.body, want)
	}
	if res.Accepted != 3 || res.Turn.State != "completed" {
		t.Errorf("result = %+v", res)
	}

	failed, _ := json.Marshal(TurnEnd{State: TurnStateFailed, FailureReason: TurnFailureTimeout})
	if string(failed) != `{"type":"turn.end","state":"failed","failure_reason":"timeout"}` {
		t.Errorf("a bare failed end = %s", failed)
	}
}

// Frame batches past the hosted service's bounds are refused before anything
// is sent, and a credential refusal is told apart.
func TestPostTurnFrames_Bounds(t *testing.T) {
	_, c := newTurnServer(t, http.StatusOK, `{}`)
	many := make([]TurnFrame, TurnMaxFramesPerRequest+1)
	for i := range many {
		many[i] = TurnDelta{Seq: i + 1, Text: "x"}
	}
	if _, err := c.PostTurnFrames(context.Background(), testLease(), many); err == nil {
		t.Error("51 frames were sent")
	}
	if _, err := c.PostTurnFrames(context.Background(), testLease(), nil); err == nil {
		t.Error("no frames were sent")
	}
	big := TurnDelta{Seq: 1, Text: strings.Repeat("x", TurnMaxDeltaBytes+1)}
	if _, err := c.PostTurnFrames(context.Background(), testLease(), []TurnFrame{big}); err == nil {
		t.Error("an over-cap delta was sent")
	}
	_, secret := newTurnServer(t, http.StatusUnprocessableEntity, `{"error":{"code":"SECRET_DETECTED","message":"x","details":{"shape":"github_token"}}}`)
	_, err := secret.PostTurnFrames(context.Background(), testLease(), []TurnFrame{TurnDelta{Seq: 1, Text: "x"}})
	if !errors.Is(err, ErrTurnSecretDetected) {
		t.Errorf("422 err = %v, want ErrTurnSecretDetected", err)
	}
}

// The capability list travels on every heartbeat (#1569): a switch to an
// adapter that cannot host a turn drops `conversation` from the next beat,
// and a profile that does not resolve advertises none.
func TestHeartbeat_CarriesTheCapabilityList(t *testing.T) {
	srv, body, _ := captureServer(t, http.StatusOK, `{}`)
	conversation := true
	var resolveErr error
	reg := NewAgentRegistrationService(onlineClient(t, srv.URL), "").
		WithExecutionProfile(func() (ExecutionProfile, bool, error) { return testProfile(), conversation, resolveErr })
	beat := func() []string {
		t.Helper()
		if err := reg.Heartbeat(context.Background(), "agent-1"); err != nil {
			t.Fatal(err)
		}
		var got agentHeartbeatBody
		if err := json.Unmarshal(*body, &got); err != nil {
			t.Fatal(err)
		}
		return got.Capabilities
	}
	if got := strings.Join(beat(), ","); got != "attention_resolve,conversation" {
		t.Errorf("viable adapter: capabilities = %s", got)
	}
	conversation = false
	if got := strings.Join(beat(), ","); got != "attention_resolve" {
		t.Errorf("after switching to a non-viable adapter: capabilities = %s", got)
	}
	conversation, resolveErr = true, errors.New("config unreadable")
	if got := strings.Join(beat(), ","); got != "attention_resolve" {
		t.Errorf("unresolved profile: capabilities = %s", got)
	}
}

// The two turn command types go to the turn handler and never to the relay:
// whichever acknowledgement arrives first decides a turn, so the extension
// must never see one. With no handler they are left alone entirely.
func TestAttentionConsumer_TurnCommandsAreNeverRelayed(t *testing.T) {
	var handled, relayed []string
	acks := 0
	ack := func(context.Context, string, string) (string, error) { acks++; return "", nil }
	c := NewAttentionCommandConsumer(nil, ack, "agent-1").
		WithRelay(func(_ string, cmd PendingCommand) { relayed = append(relayed, cmd.Type) }).
		WithTurnHandler(func(_ context.Context, agentID string, cmd PendingCommand) {
			handled = append(handled, agentID+"/"+cmd.Type)
		})
	for _, typ := range []string{ConversationTurnCommandType, ConversationTurnCancelCommandType, "pause"} {
		_ = c.Execute(context.Background(), PendingCommand{ID: "c-" + typ, Type: typ, Frame: json.RawMessage(`{}`)})
	}
	if strings.Join(handled, ",") != "agent-1/conversation_turn,agent-1/conversation_turn_cancel" {
		t.Errorf("handled = %v", handled)
	}
	if strings.Join(relayed, ",") != "pause" {
		t.Errorf("relayed = %v, want only pause", relayed)
	}

	relayed = nil
	bare := NewAttentionCommandConsumer(nil, ack, "agent-1").
		WithRelay(func(_ string, cmd PendingCommand) { relayed = append(relayed, cmd.Type) })
	_ = bare.Execute(context.Background(), PendingCommand{ID: "t", Type: ConversationTurnCommandType, Frame: json.RawMessage(`{}`)})
	if len(relayed) != 0 || acks != 0 {
		t.Errorf("with no turn handler: relayed %v, acks %d; want neither", relayed, acks)
	}
}
