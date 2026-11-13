package hooks

import (
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// NotifyResult is the output of the notification hook.
type NotifyResult struct {
	Sent     bool   `json:"sent"`
	Platform string `json:"platform,omitempty"`
	Error    string `json:"error,omitempty"`
}

// NotifyEvent represents a notification event type.
type NotifyEvent string

const (
	EventPermissionPrompt  NotifyEvent = "permission_prompt"
	EventIdlePrompt        NotifyEvent = "idle_prompt"
	EventAuthSuccess       NotifyEvent = "auth_success"
	EventElicitationDialog NotifyEvent = "elicitation_dialog"
	EventPipelineComplete  NotifyEvent = "pipeline_complete"
	EventPipelineError     NotifyEvent = "pipeline_error"
)

// defaultTitles maps events to notification titles.
var defaultTitles = map[NotifyEvent]string{
	EventPermissionPrompt:  "Nightgauge - Permission Required",
	EventIdlePrompt:        "Nightgauge - Idle",
	EventAuthSuccess:       "Nightgauge - Authenticated",
	EventElicitationDialog: "Nightgauge - Input Needed",
	EventPipelineComplete:  "Nightgauge - Pipeline Complete",
	EventPipelineError:     "Nightgauge - Pipeline Error",
}

// NotifyDisableEnv names the variable that switches desktop notifications off.
// Only the value "1" disables them; tests that spawn a real hook binary set it
// so a gate run never posts a banner to the operator's desktop (#3137).
const NotifyDisableEnv = "NIGHTGAUGE_NOTIFY_DISABLE"

// deliverNotification posts the notification. Tests replace it to count
// deliveries without starting osascript, notify-send or PowerShell.
var deliverNotification = deliverForPlatform

// NotifyTitle returns the banner title for event, or the generic "Nightgauge"
// title for an empty or unknown event.
func NotifyTitle(event NotifyEvent) string {
	if title := defaultTitles[event]; title != "" {
		return title
	}
	return "Nightgauge"
}

// EvaluateNotify sends a desktop notification unless NotifyDisableEnv is "1".
func EvaluateNotify(event NotifyEvent, message string) NotifyResult {
	title := NotifyTitle(event)
	if os.Getenv(NotifyDisableEnv) == "1" {
		return NotifyResult{Sent: false, Error: "notifications disabled (" + NotifyDisableEnv + "=1)"}
	}
	return deliverNotification(title, message)
}

func deliverForPlatform(title, message string) NotifyResult {
	switch runtime.GOOS {
	case "darwin":
		return notifyMacOS(title, message)
	case "linux":
		return notifyLinux(title, message)
	case "windows":
		return notifyWindows(title, message)
	default:
		return NotifyResult{Sent: false, Error: "unsupported platform: " + runtime.GOOS}
	}
}

// NotifyFromHook extracts the event and message from a Notification hook payload.
//
// Notification hooks are invoked with the payload on stdin and no argv (#354),
// so the event comes from Claude Code's notification_type. A type outside the
// known vocabulary, or none, yields "" and the generic title (#3137). An
// unparseable payload yields an empty message: nothing to announce.
func NotifyFromHook(inputJSON []byte) (NotifyEvent, string) {
	var input struct {
		Message          string `json:"message"`
		NotificationType string `json:"notification_type"`
	}
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return "", ""
	}
	event := NotifyEvent(input.NotificationType)
	if _, ok := defaultTitles[event]; !ok {
		event = ""
	}
	return event, input.Message
}

// EvaluateNotifyJSON returns the notification result as JSON bytes.
func EvaluateNotifyJSON(event NotifyEvent, message string) ([]byte, error) {
	result := EvaluateNotify(event, message)
	return json.Marshal(result)
}

// notifyMacOS sends a notification via osascript on macOS.
func notifyMacOS(title, message string) NotifyResult {
	// Escape for AppleScript
	safeTitle := escapeAppleScript(title)
	safeMsg := escapeAppleScript(message)

	script := `display notification "` + safeMsg + `" with title "` + safeTitle + `"`
	cmd := exec.Command("osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		return NotifyResult{Sent: false, Platform: "darwin", Error: err.Error()}
	}
	return NotifyResult{Sent: true, Platform: "darwin"}
}

// notifyLinux sends a notification via notify-send on Linux.
func notifyLinux(title, message string) NotifyResult {
	safeTitle := escapeLinuxNotifyCommand(title)
	safeMsg := escapeLinuxNotifyCommand(message)

	// Try notify-send first, then kdialog
	if _, err := exec.LookPath("notify-send"); err == nil {
		cmd := exec.Command("notify-send", safeTitle, safeMsg)
		if err := cmd.Run(); err != nil {
			return NotifyResult{Sent: false, Platform: "linux", Error: err.Error()}
		}
		return NotifyResult{Sent: true, Platform: "linux"}
	}

	if _, err := exec.LookPath("kdialog"); err == nil {
		cmd := exec.Command("kdialog", "--passivepopup", safeMsg, "5", "--title", safeTitle)
		if err := cmd.Run(); err != nil {
			return NotifyResult{Sent: false, Platform: "linux", Error: err.Error()}
		}
		return NotifyResult{Sent: true, Platform: "linux"}
	}

	return NotifyResult{Sent: false, Platform: "linux", Error: "no notification tool found (need notify-send or kdialog)"}
}

// notifyWindows sends a notification via PowerShell on Windows.
func notifyWindows(title, message string) NotifyResult {
	// Escape for PowerShell
	safeTitle := escapePowerShell(title)
	safeMsg := escapePowerShell(message)

	script := `
[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null
$template = [Windows.UI.Notifications.ToastNotificationManager]::GetTemplateContent([Windows.UI.Notifications.ToastTemplateType]::ToastText02)
$textNodes = $template.GetElementsByTagName('text')
$textNodes.Item(0).AppendChild($template.CreateTextNode('` + safeTitle + `')) > $null
$textNodes.Item(1).AppendChild($template.CreateTextNode('` + safeMsg + `')) > $null
$toast = [Windows.UI.Notifications.ToastNotification]::new($template)
[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('Nightgauge').Show($toast)
`
	cmd := exec.Command("powershell", "-NoProfile", "-Command", script)
	if err := cmd.Run(); err != nil {
		return NotifyResult{Sent: false, Platform: "windows", Error: err.Error()}
	}
	return NotifyResult{Sent: true, Platform: "windows"}
}

// escapeAppleScript escapes special characters for AppleScript strings.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return s
}

// escapeLinuxNotifyCommand escapes special characters for Linux notification commands.
func escapeLinuxNotifyCommand(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	s = strings.ReplaceAll(s, "`", "\\`")
	s = strings.ReplaceAll(s, "$", "\\$")
	return s
}

// escapePowerShell escapes special characters for PowerShell strings.
func escapePowerShell(s string) string {
	s = strings.ReplaceAll(s, `'`, `''`)
	s = strings.ReplaceAll(s, "`", "``")
	s = strings.ReplaceAll(s, "$", "`$")
	return s
}
