package hooks

import (
	"encoding/json"
	"testing"
)

func TestEscapeAppleScript(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{`has "quotes"`, `has \"quotes\"`},
		{`back\slash`, `back\\slash`},
	}

	for _, tt := range tests {
		got := escapeAppleScript(tt.input)
		if got != tt.want {
			t.Errorf("escapeAppleScript(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestEscapePowerShell(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"it's", "it''s"},
		{"$var", "`$var"},
		{"`backtick", "``backtick"},
	}

	for _, tt := range tests {
		got := escapePowerShell(tt.input)
		if got != tt.want {
			t.Errorf("escapePowerShell(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDefaultTitles(t *testing.T) {
	events := []NotifyEvent{
		EventPermissionPrompt,
		EventIdlePrompt,
		EventAuthSuccess,
		EventElicitationDialog,
		EventPipelineComplete,
		EventPipelineError,
	}

	for _, event := range events {
		title := defaultTitles[event]
		if title == "" {
			t.Errorf("no default title for event %q", event)
		}
	}
}

func TestEscapeLinuxNotifyCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple", "simple"},
		{"it's", "it''s"},
		{"$var", "\\$var"},
		{"`cmd`", "\\`cmd\\`"},
		{"$(inject)", "\\$(inject)"},
		{"multi '$1' `x`", "multi ''\\$1'' \\`x\\`"},
	}

	for _, tt := range tests {
		got := escapeLinuxNotifyCommand(tt.input)
		if got != tt.want {
			t.Errorf("escapeLinuxNotifyCommand(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestNotifyResultJSON(t *testing.T) {
	result := NotifyResult{Sent: true, Platform: "darwin"}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var parsed NotifyResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !parsed.Sent || parsed.Platform != "darwin" {
		t.Errorf("parsed = %+v, want Sent=true Platform=darwin", parsed)
	}
}

func stubDeliverer(t *testing.T) *int {
	t.Helper()
	calls := 0
	orig := deliverNotification
	deliverNotification = func(title, message string) NotifyResult {
		calls++
		return NotifyResult{Sent: true, Platform: "stub"}
	}
	t.Cleanup(func() { deliverNotification = orig })
	return &calls
}

func TestEvaluateNotifyDisabled(t *testing.T) {
	calls := stubDeliverer(t)
	t.Setenv(NotifyDisableEnv, "1")
	got := EvaluateNotify(EventPermissionPrompt, "x")
	if got.Sent || got.Error != "notifications disabled (NIGHTGAUGE_NOTIFY_DISABLE=1)" {
		t.Fatalf("disabled: got %+v", got)
	}
	if *calls != 0 {
		t.Fatalf("disabled: deliverer called %d times", *calls)
	}
}

func TestEvaluateNotifyEnabled(t *testing.T) {
	for _, v := range []string{"", "0", "true"} {
		calls := stubDeliverer(t)
		t.Setenv(NotifyDisableEnv, v)
		if got := EvaluateNotify(EventPermissionPrompt, "x"); !got.Sent {
			t.Fatalf("%s=%q: got %+v", NotifyDisableEnv, v, got)
		}
		if *calls != 1 {
			t.Fatalf("%s=%q: deliverer called %d times, want 1", NotifyDisableEnv, v, *calls)
		}
	}
}

func TestNotifyFromHookNotificationType(t *testing.T) {
	cases := []struct {
		payload   string
		wantEvent NotifyEvent
		wantTitle string
	}{
		{`{"message":"m","notification_type":"permission_prompt"}`, EventPermissionPrompt, "Nightgauge - Permission Required"},
		{`{"message":"m","notification_type":"idle_prompt"}`, EventIdlePrompt, "Nightgauge - Idle"},
		{`{"message":"m","notification_type":"auth_success"}`, EventAuthSuccess, "Nightgauge - Authenticated"},
		{`{"message":"m","notification_type":"elicitation_dialog"}`, EventElicitationDialog, "Nightgauge - Input Needed"},
		{`{"message":"m","notification_type":"something_new"}`, "", "Nightgauge"},
		{`{"message":"m"}`, "", "Nightgauge"},
	}
	for _, c := range cases {
		event, msg := NotifyFromHook([]byte(c.payload))
		if event != c.wantEvent || msg != "m" {
			t.Errorf("%s: got (%q,%q), want (%q,\"m\")", c.payload, event, msg, c.wantEvent)
		}
		title := NotifyTitle(event)
		if title != c.wantTitle || title == "Nightgauge - Pipeline Complete" {
			t.Errorf("%s: title %q, want %q", c.payload, title, c.wantTitle)
		}
	}
	if event, msg := NotifyFromHook([]byte("not json")); event != "" || msg != "" {
		t.Errorf("bad payload: got (%q,%q)", event, msg)
	}
}
