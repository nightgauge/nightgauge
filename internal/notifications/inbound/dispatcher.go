// Package inbound hosts the in-binary HTTP receiver that accepts
// Mattermost outgoing-webhook callbacks and dispatches verified
// commands to the rest of the system as IPC events.
//
// The package is intentionally free of any internal/ipc import — the
// dispatcher is a small interface that the cmd/ wiring layer satisfies
// with a closure over ipc.Server.Emit. This keeps the dependency
// direction one-way: cmd/ → both internal/ipc and
// internal/notifications/inbound; neither imports the other.
package inbound

import "context"

// MattermostCommand is the verified payload extracted from a Mattermost
// outgoing-webhook POST. Field names mirror the Mattermost spec
// (https://developers.mattermost.com/integrate/webhooks/outgoing/) so
// the TS-side dispatcher (#3376) can decode the IPC event without
// translation.
type MattermostCommand struct {
	TeamID      string `json:"team_id,omitempty"`
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelName string `json:"channel_name,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	UserName    string `json:"user_name,omitempty"`
	Command     string `json:"command,omitempty"`
	Text        string `json:"text,omitempty"`
	TriggerWord string `json:"trigger_word,omitempty"`
	TriggerID   string `json:"trigger_id,omitempty"`
	// ResponseURL is the Mattermost-supplied URL for async follow-up
	// posts. Slash commands always carry this field; outgoing webhooks
	// may omit it. Empty value means "no async reply path available".
	ResponseURL string `json:"response_url,omitempty"`
}

// MattermostSlashEvent is the IPC event payload emitted by the
// inbound dispatcher. It embeds the raw webhook fields and adds a
// parsed PipelineCommand so the TypeScript dispatcher can route
// without re-parsing the text.
type MattermostSlashEvent struct {
	MattermostCommand
	ParsedCommand PipelineCommand `json:"parsed_command"`
}

// CommandDispatcher receives verified Mattermost commands. The handler
// calls Dispatch synchronously after token + replay-window checks pass.
// Implementations must be goroutine-safe — the HTTP server may invoke
// Dispatch concurrently from multiple request goroutines.
type CommandDispatcher interface {
	Dispatch(ctx context.Context, cmd MattermostCommand) error
}

// EmitFunc is the signature of ipc.Server.Emit. The IPC dispatcher
// adapter takes a function value rather than a *ipc.Server pointer so
// this package never imports internal/ipc.
type EmitFunc func(event string, data interface{})

// NewIPCDispatcher returns a CommandDispatcher that emits each verified
// command as a `mattermost.command` IPC event. The TS-side listener
// (#3376) decodes the event payload into the same MattermostCommand
// shape and routes it to a slash-command handler.
func NewIPCDispatcher(emit EmitFunc) CommandDispatcher {
	return &ipcDispatcher{emit: emit}
}

type ipcDispatcher struct {
	emit EmitFunc
}

func (d *ipcDispatcher) Dispatch(_ context.Context, cmd MattermostCommand) error {
	if d.emit == nil {
		return nil
	}
	d.emit("mattermost.command", MattermostSlashEvent{
		MattermostCommand: cmd,
		ParsedCommand:     Parse(cmd.Text),
	})
	return nil
}
