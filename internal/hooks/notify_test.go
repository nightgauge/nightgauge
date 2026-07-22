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
		EventIdle,
		EventAuthSuccess,
		EventDialogElicit,
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
