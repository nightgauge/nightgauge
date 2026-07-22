package hooks

import (
	"encoding/json"
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
	EventPermissionPrompt NotifyEvent = "permission_prompt"
	EventIdle             NotifyEvent = "idle"
	EventAuthSuccess      NotifyEvent = "auth_success"
	EventDialogElicit     NotifyEvent = "dialog_elicit"
	EventPipelineComplete NotifyEvent = "pipeline_complete"
	EventPipelineError    NotifyEvent = "pipeline_error"
)

// defaultTitles maps events to notification titles.
var defaultTitles = map[NotifyEvent]string{
	EventPermissionPrompt: "Nightgauge - Permission Required",
	EventIdle:             "Nightgauge - Idle",
	EventAuthSuccess:      "Nightgauge - Authenticated",
	EventDialogElicit:     "Nightgauge - Input Needed",
	EventPipelineComplete: "Nightgauge - Pipeline Complete",
	EventPipelineError:    "Nightgauge - Pipeline Error",
}

// EvaluateNotify sends a desktop notification.
func EvaluateNotify(event NotifyEvent, message string) NotifyResult {
	title := defaultTitles[event]
	if title == "" {
		title = "Nightgauge"
	}

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
