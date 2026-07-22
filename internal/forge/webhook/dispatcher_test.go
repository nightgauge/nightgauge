package webhook_test

import (
	"context"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge/webhook"
)

func TestNewIPCEventDispatcher_EmitsCorrectEvent(t *testing.T) {
	var gotEvent string
	var gotData interface{}

	emit := webhook.EmitFunc(func(event string, data interface{}) {
		gotEvent = event
		gotData = data
	})

	disp := webhook.NewIPCEventDispatcher(emit)

	evt := webhook.ForgeWebhookEvent{
		Source:     "gitlab",
		EventType:  "gitlab.pipeline",
		DeliveryID: "test-delivery-1",
		OccurredAt: time.Now(),
		Payload:    map[string]string{"key": "value"},
	}

	if err := disp.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("Dispatch returned unexpected error: %v", err)
	}

	if gotEvent != "gitlab.pipeline" {
		t.Errorf("emitted event = %q; want %q", gotEvent, "gitlab.pipeline")
	}
	if gotData == nil {
		t.Error("emitted data is nil; want non-nil")
	}
}

func TestNewIPCEventDispatcher_NilEmit_NoError(t *testing.T) {
	disp := webhook.NewIPCEventDispatcher(nil)
	evt := webhook.ForgeWebhookEvent{EventType: "gitlab.pipeline"}
	if err := disp.Dispatch(context.Background(), evt); err != nil {
		t.Fatalf("Dispatch with nil emit returned error: %v", err)
	}
}

func TestNewIPCEventDispatcher_EventTypePassthrough(t *testing.T) {
	cases := []string{"gitlab.pipeline", "gitlab.mr", "gitlab.note", "gitlab.push"}
	for _, et := range cases {
		t.Run(et, func(t *testing.T) {
			var got string
			disp := webhook.NewIPCEventDispatcher(func(event string, _ interface{}) { got = event })
			_ = disp.Dispatch(context.Background(), webhook.ForgeWebhookEvent{EventType: et})
			if got != et {
				t.Errorf("event type = %q; want %q", got, et)
			}
		})
	}
}
