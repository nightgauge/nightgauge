package adapters

import "strings"

// copilotWriteTools are the Claude tools that modify files. Copilot gates all
// of them behind one `write` tool spec.
var copilotWriteTools = map[string]bool{
	"Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true,
}

// copilotUngatedTools are the Claude read-only tools. Copilot has no tool spec
// for reading files inside the working directory, so they need no flag and are
// not reported as dropped.
var copilotUngatedTools = map[string]bool{
	"Read": true, "Grep": true, "Glob": true, "LS": true,
}

// copilotToolPolicy translates a stage's Claude-style allowed-tools list into
// Copilot CLI permission flags (#1). An empty list keeps `--allow-all-tools`,
// the autonomous default. A non-empty list never emits `--allow-all-tools`:
// each entry maps to a narrower flag, and an entry with no Copilot equivalent
// is denied and returned in dropped, so access is never broadened.
//
// Tool-spec syntax (`shell`, `shell(cmd)`, `write`, `Server(tool)`, one value
// per repeated --allow-tool) is from
// https://docs.github.com/en/copilot/concepts/agents/about-copilot-cli
// ("Allowed tools"), read 2026-10-28; not verified against a live CLI.
//
// The shared fixture internal/execution/testdata/copilot-tool-policy.json
// pins this mapping for both this function and the SDK's copilotTools.ts.
func copilotToolPolicy(allowed []string) (args []string, dropped []string) {
	var specs, urls []string
	allURLs := false
	seen := map[string]bool{}
	addSpec := func(s string) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	addURL := func(u string) {
		if !seen["url:"+u] {
			seen["url:"+u] = true
			urls = append(urls, u)
		}
	}
	nonEmpty := false
	for _, raw := range allowed {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		nonEmpty = true
		name := codexBaseToolName(entry)
		scope := copilotToolScope(entry)
		switch {
		case name == "Bash":
			if scope == "" {
				addSpec("shell")
			} else {
				addSpec("shell(" + scope + ")")
			}
		case copilotWriteTools[name]:
			addSpec("write")
		case copilotUngatedTools[name]:
		case name == "WebFetch" || name == "WebSearch":
			if d, ok := strings.CutPrefix(scope, "domain:"); ok && d != "" {
				addURL(d)
			} else {
				allURLs = true
			}
		case strings.HasPrefix(name, "mcp__"):
			parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
			if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				addSpec(parts[0] + "(" + parts[1] + ")")
			} else if len(parts) == 1 && parts[0] != "" {
				addSpec(parts[0])
			} else {
				dropped = append(dropped, entry)
			}
		default:
			dropped = append(dropped, entry)
		}
	}
	if !nonEmpty {
		return []string{"--allow-all-tools"}, nil
	}
	args = []string{}
	for _, s := range specs {
		args = append(args, "--allow-tool", s)
	}
	if allURLs {
		args = append(args, "--allow-all-urls")
	} else {
		for _, u := range urls {
			args = append(args, "--allow-url", u)
		}
	}
	return args, dropped
}

// copilotToolScope returns the parenthesised scope of an allowed-tools entry
// with Claude's trailing wildcard (`:*` or ` *`) removed: `Bash(git:*)` → `git`.
func copilotToolScope(entry string) string {
	open := strings.Index(entry, "(")
	if open == -1 || !strings.HasSuffix(entry, ")") {
		return ""
	}
	s := strings.TrimSpace(entry[open+1 : len(entry)-1])
	s = strings.TrimSuffix(s, ":*")
	s = strings.TrimSuffix(s, " *")
	if s == "*" {
		return ""
	}
	return strings.TrimSpace(s)
}
