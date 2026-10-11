package platform

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A `queue_add` goes to the queue-add handler and never to the relay (#2440):
// the daemon adds the item to its own queue and acknowledges it. With no
// handler it is left alone, never relayed and never acknowledged.
func TestAttentionConsumer_QueueAddIsNeverRelayed(t *testing.T) {
	var handled, relayed []string
	acks := 0
	ack := func(context.Context, string, string) (string, error) { acks++; return "", nil }
	c := NewAttentionCommandConsumer(nil, ack, "agent-1").
		WithRelay(func(_ string, cmd PendingCommand) { relayed = append(relayed, cmd.Type) }).
		WithQueueAddHandler(func(_ context.Context, agentID string, cmd PendingCommand) {
			handled = append(handled, agentID+"/"+cmd.ID)
		})
	for _, typ := range []string{QueueAddCommandType, "trigger"} {
		_ = c.Execute(context.Background(), PendingCommand{ID: "c-" + typ, Type: typ, Frame: json.RawMessage(`{}`)})
	}
	if strings.Join(handled, ",") != "agent-1/c-queue_add" {
		t.Errorf("handled = %v", handled)
	}
	if strings.Join(relayed, ",") != "trigger" {
		t.Errorf("relayed = %v, want only trigger", relayed)
	}

	relayed = nil
	bare := NewAttentionCommandConsumer(nil, ack, "agent-1").
		WithRelay(func(_ string, cmd PendingCommand) { relayed = append(relayed, cmd.Type) })
	_ = bare.Execute(context.Background(), PendingCommand{ID: "q", Type: QueueAddCommandType, Frame: json.RawMessage(`{}`)})
	if len(relayed) != 0 || acks != 0 {
		t.Errorf("with no queue-add handler: relayed %v, acks %d; want neither", relayed, acks)
	}
}

// A 409 ALREADY_ACKED on the ack route is told apart from every other
// refusal, and the error reads as it did before.
func TestCommandService_AlreadyAckedIsTyped(t *testing.T) {
	status, body := http.StatusConflict, `{"code":"ALREADY_ACKED","message":"Pipeline command has already been acknowledged."}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := NewClient(Config{BaseURL: srv.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewCommandService(c)

	err = svc.ApplyAgentCommand(context.Background(), "agent-1", "cmd-1", "")
	if !errors.Is(err, ErrAgentCommandAlreadyAcked) {
		t.Fatalf("err = %v, want ErrAgentCommandAlreadyAcked", err)
	}
	if want := "acknowledge agent command: HTTP 409: " + body; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}

	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusConflict, `{"code":"TURN_ENDED"}`},
		{http.StatusNotFound, `{"code":"NOT_FOUND"}`},
		{http.StatusInternalServerError, `oops`},
	} {
		status, body = tc.status, tc.body
		err = svc.RejectAgentCommand(context.Background(), "agent-1", "cmd-1", "no")
		var ackErr *AgentCommandAckError
		if err == nil || errors.Is(err, ErrAgentCommandAlreadyAcked) || !errors.As(err, &ackErr) || ackErr.Status != tc.status {
			t.Errorf("%d %s: err = %v, want an AgentCommandAckError that is not ALREADY_ACKED", tc.status, tc.body, err)
		}
	}
}
