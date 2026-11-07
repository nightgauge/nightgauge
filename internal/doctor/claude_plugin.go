package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// A Nightgauge plugin Claude Code cannot load (#2719).
//
// When a directory marketplace's source disappears, `claude plugin list --json`
// still says `"enabled": true` and only its `errors` array tells the truth, so
// every skill and hook is gone while the plugin looks installed. This is a
// warning, not a blocker: skills halt on doctor exit 2, and stages render their
// prompts without the plugin. A machine with no Claude CLI, or without the
// plugin, has nothing to report.

const (
	claudePluginCheck = "claude_plugin_health"
	claudePluginCode  = "NGD051"
)

// claudeLookPath and claudePluginList are the check's reach into the machine.
// Variables so a test can supply them.
var (
	claudeLookPath   = exec.LookPath
	claudePluginList = func(ctx context.Context, cwd string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, catalogProbeTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "claude", "plugin", "list", "--json")
		cmd.Dir = cwd
		return cmd.Output()
	}
)

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: claudePluginCheck, Title: "Claude plugin", Group: "adapters", Code: claudePluginCode,
		Run: func(ctx context.Context, env *Env) []Finding {
			fs, detail := claudePluginFindings(ctx, env.Cwd)
			env.SetDetail(claudePluginCheck, detail)
			return fs
		},
	})
}

// claudePluginEntry is the part of a `claude plugin list --json` entry doctor
// reads. A healthy entry has no `errors` key.
type claudePluginEntry struct {
	ID     string   `json:"id"`
	Scope  string   `json:"scope"`
	Errors []string `json:"errors"`
}

func claudePluginFindings(ctx context.Context, cwd string) ([]Finding, string) {
	if _, err := claudeLookPath("claude"); err != nil {
		return nil, "skipped: no claude CLI on PATH"
	}
	out, err := claudePluginList(ctx, cwd)
	if err != nil {
		reason := "`claude plugin list --json` failed: " + err.Error()
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			reason += ": " + strings.TrimSpace(string(exit.Stderr))
		}
		return claudePluginUnverifiable(reason)
	}
	var entries []claudePluginEntry
	if err := json.Unmarshal(out, &entries); err != nil {
		return claudePluginUnverifiable("`claude plugin list --json` did not print the expected JSON: " + err.Error())
	}

	var findings []Finding
	installed := 0
	for _, e := range entries {
		if !strings.HasPrefix(e.ID, "nightgauge@") {
			continue
		}
		installed++
		if len(e.Errors) == 0 {
			continue
		}
		claudeError := strings.Join(e.Errors, "; ")
		findings = append(findings, newFinding(claudePluginCheck, claudePluginCode, SeverityWarning,
			fmt.Sprintf("Claude plugin %s failed to load", e.ID),
			"Claude Code reports: "+claudeError+". Its skills and hooks are unavailable, even if the plugin still shows as enabled",
			map[string]string{"plugin": e.ID, "scope": e.Scope, "error": claudeError},
			[]string{e.ID, e.Scope},
			manualRemedy("repoint", "Point the marketplace at a directory that exists", claudePluginCheck,
				"Reload the VS Code window: the extension repoints the marketplace at its own stable copy",
				"Or, from the primary checkout, run `bash scripts/install-agent-skills.sh --claude-only`")))
	}
	if installed == 0 {
		return nil, "Nightgauge plugin not installed"
	}
	return findings, fmt.Sprintf("%d Nightgauge plugin install(s), %d failed", installed, len(findings))
}

func claudePluginUnverifiable(reason string) ([]Finding, string) {
	return []Finding{unverifiableFinding(claudePluginCheck, claudePluginCode, SeverityWarning,
		"Claude plugin health", reason)}, "could not read the Claude plugin list"
}
