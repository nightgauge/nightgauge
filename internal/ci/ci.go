package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/detect"
)

// DiscoverResult is the output of DiscoverCommands.
type DiscoverResult struct {
	Commands []string `json:"commands"`
	// Checks carries the same commands as Commands, in order, with the
	// workflow and step each came from so a caller can report what it ran.
	Checks       []Check `json:"checks"`
	WorkflowPath string  `json:"workflow_path"`
	// WorkflowPaths lists every workflow that contributed commands.
	WorkflowPaths []string `json:"workflow_paths"`
	Framework     string   `json:"framework"` // "node" | "go" | "flutter" | "unknown"
	Timestamp     string   `json:"timestamp"`
}

// Check is one locally runnable command and where it came from.
type Check struct {
	Command  string `json:"command"`
	Workflow string `json:"workflow"`
	Step     string `json:"step"`
}

// ParityResult is the output of CheckParity.
type ParityResult struct {
	Passed      bool         `json:"passed"`
	CommandsRun []string     `json:"commands_run"`
	Failures    []ParityFail `json:"failures"`
	Timestamp   string       `json:"timestamp"`
}

// ParityFail describes a single CI parity check failure.
type ParityFail struct {
	Command     string `json:"command"`
	FailureType string `json:"failure_type"` // "format"|"lint"|"typecheck"|"test"|"build"
	Output      string `json:"output"`
	ExitCode    int    `json:"exit_code"`
}

// workflowYAML is used for YAML unmarshalling of GitHub Actions workflow files.
type workflowYAML struct {
	On   yaml.Node          `yaml:"on"`
	Jobs map[string]jobYAML `yaml:"jobs"`
}

type jobYAML struct {
	Steps []stepYAML `yaml:"steps"`
}

type stepYAML struct {
	Name string `yaml:"name"`
	Run  string `yaml:"run"`
}

// unsafePatterns identify runner setup that cannot or must not run on a
// developer machine. A run: block containing any of them is dropped whole.
var unsafePatterns = []string{
	"$github_", "${github_", "${{", "$runner_temp", "${runner_temp}",
	"::error", "::warning", "::notice", "::group", "::endgroup",
	"sudo ", "apt-get", "playwright install", "go install", "pip install",
	"pip3 install", "git config --global", "git config --system",
}

// skipPatterns are substrings that identify install/setup steps to exclude.
var skipPatterns = []string{
	"npm ci", "npm install", "checkout", "setup-node", "setup-go",
	"upload-artifact", "download-artifact", "actions/",
}

// DiscoverCommands parses CI workflows to extract locally runnable check
// commands. When workflowPath is empty, it scans every workflow under
// .github/workflows/ that runs on pull_request or merge_group; an explicit
// path is parsed regardless of its triggers. Falls back to standard commands
// derived from project type when no workflow yields a command.
func DiscoverCommands(_ context.Context, workdir, workflowPath string) (DiscoverResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)

	var paths []string
	if workflowPath != "" {
		paths = []string{workflowPath}
	} else {
		for _, pattern := range []string{"*.yml", "*.yaml"} {
			matches, _ := filepath.Glob(filepath.Join(workdir, ".github", "workflows", pattern))
			paths = append(paths, matches...)
		}
		sort.Strings(paths)
	}

	var checks []Check
	var used []string
	seen := make(map[string]bool)
	for _, p := range paths {
		added := false
		for _, c := range parseWorkflowChecks(p, workflowPath == "") {
			if seen[c.Command] {
				continue
			}
			seen[c.Command] = true
			checks = append(checks, c)
			added = true
		}
		if added {
			used = append(used, p)
		}
	}

	framework := detectFramework(workdir)

	var commands []string
	for _, c := range checks {
		commands = append(commands, c.Command)
	}
	if len(commands) == 0 {
		commands = fallbackCommands(workdir, framework)
	}

	first := ""
	if len(used) > 0 {
		first = used[0]
	} else if workflowPath != "" {
		first = workflowPath
	}
	if checks == nil {
		checks = []Check{}
	}
	if used == nil {
		used = []string{}
	}

	return DiscoverResult{
		Commands:      commands,
		Checks:        checks,
		WorkflowPath:  first,
		WorkflowPaths: used,
		Framework:     framework,
		Timestamp:     ts,
	}, nil
}

// DiscoverCommandsJSON is a convenience wrapper returning JSON bytes.
func DiscoverCommandsJSON(ctx context.Context, workdir, workflowPath string) ([]byte, error) {
	result, err := DiscoverCommands(ctx, workdir, workflowPath)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// CheckParity runs each command in workdir and returns structured failure information.
func CheckParity(_ context.Context, workdir string, commands []string) (ParityResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	result := ParityResult{
		Passed:      true,
		CommandsRun: commands,
		Failures:    []ParityFail{},
		Timestamp:   ts,
	}

	for _, cmdStr := range commands {
		out, exitCode, err := runShellCmd(workdir, cmdStr)
		if err != nil || exitCode != 0 {
			result.Passed = false
			result.Failures = append(result.Failures, ParityFail{
				Command:     cmdStr,
				FailureType: classifyFailureType(cmdStr),
				Output:      out,
				ExitCode:    exitCode,
			})
		}
	}

	return result, nil
}

// CheckParityJSON is a convenience wrapper returning JSON bytes.
func CheckParityJSON(ctx context.Context, workdir string, commands []string) ([]byte, error) {
	result, err := CheckParity(ctx, workdir, commands)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// triggersOnPR reports whether the workflow's `on:` runs on pull_request or
// merge_group, in string, list or map form.
func triggersOnPR(on *yaml.Node) bool {
	wanted := func(v string) bool { return v == "pull_request" || v == "merge_group" }
	switch on.Kind {
	case yaml.ScalarNode:
		return wanted(on.Value)
	case yaml.SequenceNode:
		for _, n := range on.Content {
			if n.Kind == yaml.ScalarNode && wanted(n.Value) {
				return true
			}
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(on.Content); i += 2 {
			if wanted(on.Content[i].Value) {
				return true
			}
		}
	}
	return false
}

// parseWorkflowChecks returns the safe-to-run checks of one workflow. When
// requirePR is set a workflow that does not run on pull_request/merge_group
// yields nothing. The unit of filtering is the whole run: block.
func parseWorkflowChecks(wfPath string, requirePR bool) []Check {
	data, err := os.ReadFile(wfPath)
	if err != nil {
		return nil
	}

	var wf workflowYAML
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil
	}
	if requirePR && !triggersOnPR(&wf.On) {
		return nil
	}

	workflow := filepath.Base(wfPath)
	var checks []Check
	seen := make(map[string]bool)

	jobNames := make([]string, 0, len(wf.Jobs))
	for name := range wf.Jobs {
		jobNames = append(jobNames, name)
	}
	sort.Strings(jobNames)

	for _, jobName := range jobNames {
		for i, step := range wf.Jobs[jobName].Steps {
			if strings.TrimSpace(step.Run) == "" {
				continue
			}
			stepName := step.Name
			if stepName == "" {
				stepName = fmt.Sprintf("%s step %d", jobName, i+1)
			}
			for _, cmd := range blockCommands(step.Run) {
				if !seen[cmd] {
					seen[cmd] = true
					checks = append(checks, Check{Command: cmd, Workflow: workflow, Step: stepName})
				}
			}
		}
	}
	return checks
}

// blockCommands turns one run: block into commands. A block with any unsafe
// or setup line is dropped whole. A block of several logical lines is returned
// whole as one newline-joined command, which CheckParity runs through a shell:
// its lines (if/fi, loops, `rc=$?`, `wait`) are fragments on their own. A
// single-line block is one command. Backslash continuations are joined (#194).
func blockCommands(run string) []string {
	var lines []string
	for _, line := range splitLogicalCommands(run) {
		if line == "" || line[0] == '#' {
			continue
		}
		if isUnsafe(line) || shouldSkip(line) {
			return nil
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return nil
	}
	return []string{strings.Join(lines, "\n")}
}

func isUnsafe(line string) bool {
	lower := strings.ToLower(line)
	for _, p := range unsafePatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// splitLogicalCommands splits a run: block into logical commands, joining
// backslash-continued physical lines into one. A trailing `\` (optionally
// followed by spaces/tabs, which YAML block scalars can leave behind) means
// the next line continues the command. Comment lines interleaved between
// continuations terminate the pending command — a `#` fragment inside a
// continuation would change shell semantics, and real workflows put comments
// between commands, not inside them.
func splitLogicalCommands(run string) []string {
	var logical []string
	var pending strings.Builder

	flush := func() {
		if pending.Len() > 0 {
			logical = append(logical, strings.TrimSpace(pending.String()))
			pending.Reset()
		}
	}

	for _, raw := range strings.Split(run, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || line[0] == '#' {
			// Blank/comment lines end any pending continuation; emit comment
			// lines as-is so the caller's existing filters see them.
			flush()
			if line != "" {
				logical = append(logical, line)
			}
			continue
		}

		if cont, ok := strings.CutSuffix(line, `\`); ok {
			pending.WriteString(strings.TrimSpace(cont))
			pending.WriteString(" ")
			continue
		}

		pending.WriteString(line)
		flush()
	}
	flush()

	return logical
}

func shouldSkip(line string) bool {
	lower := strings.ToLower(line)
	for _, pattern := range skipPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

// detectFramework delegates to the shared detector so internal/ci and
// internal/build cannot disagree about the same project (#195).
func detectFramework(workdir string) string {
	return detect.Framework(workdir)
}

func fallbackCommands(workdir, framework string) []string {
	switch framework {
	case "node":
		return nodeCommands(workdir)
	case "go":
		return []string{"go build ./...", "go test ./..."}
	case "flutter":
		return []string{"dart fix --apply", "dart analyze", "flutter test"}
	default:
		return []string{}
	}
}

func nodeCommands(workdir string) []string {
	data, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	var commands []string
	for _, script := range []string{"format:check", "lint", "typecheck", "build", "test"} {
		if _, ok := pkg.Scripts[script]; ok {
			commands = append(commands, "npm run "+script)
		}
	}
	return commands
}

func classifyFailureType(cmd string) string {
	lower := strings.ToLower(cmd)
	switch {
	case strings.Contains(lower, "format") || strings.Contains(lower, "prettier") ||
		strings.Contains(lower, "black") || strings.Contains(lower, "dprint"):
		return "format"
	case strings.Contains(lower, "lint") || strings.Contains(lower, "eslint") ||
		strings.Contains(lower, "pylint"):
		return "lint"
	case strings.Contains(lower, "typecheck") || strings.Contains(lower, "tsc") ||
		strings.Contains(lower, "mypy"):
		return "typecheck"
	case strings.Contains(lower, "build") || strings.Contains(lower, "compile"):
		return "build"
	case strings.Contains(lower, "test") || strings.Contains(lower, "vitest") ||
		strings.Contains(lower, "jest") || strings.Contains(lower, "pytest"):
		return "test"
	default:
		return "unknown"
	}
}

// tokenizeCommand splits a command string into an argv, honoring single/double
// quotes and backslash escapes (POSIX-ish), so a command with a quoted argument
// that contains spaces — e.g. `prettier --write "src/a b.ts"` — tokenizes into
// the intended 3 args instead of being naively whitespace-split by
// strings.Fields into 4 broken fragments. Returns an error on an unterminated
// quote rather than silently mis-splitting.
//
// SECURITY NOTE: this is argv tokenization ONLY. runShellCmd execs the binary
// directly via exec.Command (there is no `sh -c`), so shell metacharacters
// (`;` `|` `&` `$()` backticks) are never interpreted — they remain literal
// argument text. This function must NOT grow any shell-expansion behavior.
func tokenizeCommand(cmdStr string) ([]string, error) {
	const (
		none = iota
		single
		double
	)
	var (
		tokens  []string
		cur     strings.Builder
		inToken bool
		quote   = none
		runes   = []rune(cmdStr)
	)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch quote {
		case single:
			if c == '\'' {
				quote = none
			} else {
				cur.WriteRune(c)
			}
		case double:
			switch {
			case c == '"':
				quote = none
			case c == '\\' && i+1 < len(runes) &&
				(runes[i+1] == '"' || runes[i+1] == '\\' || runes[i+1] == '$' || runes[i+1] == '`'):
				i++
				cur.WriteRune(runes[i])
			default:
				cur.WriteRune(c)
			}
		default: // none
			switch {
			case c == '\'':
				inToken = true
				quote = single
			case c == '"':
				inToken = true
				quote = double
			case c == '\\' && i+1 < len(runes):
				i++
				cur.WriteRune(runes[i])
				inToken = true
			case c == ' ' || c == '\t' || c == '\n' || c == '\r':
				if inToken {
					tokens = append(tokens, cur.String())
					cur.Reset()
					inToken = false
				}
			default:
				cur.WriteRune(c)
				inToken = true
			}
		}
	}
	if quote != none {
		kind := "double"
		if quote == single {
			kind = "single"
		}
		return nil, fmt.Errorf("unterminated %s quote in command: %q", kind, cmdStr)
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

func runShellCmd(workdir, cmdStr string) (string, int, error) {
	// A compound block (if/fi, loops, heredocs) is only valid whole and needs
	// a real shell; single-line commands keep the shell-free exec path below.
	if strings.Contains(cmdStr, "\n") {
		cmd := exec.Command("sh", "-c", cmdStr)
		cmd.Dir = workdir
		var buf bytes.Buffer
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return buf.String(), exitErr.ExitCode(), err
			}
			return buf.String(), 1, err
		}
		return buf.String(), 0, nil
	}
	parts, err := tokenizeCommand(cmdStr)
	if err != nil {
		return "", 1, err
	}
	if len(parts) == 0 {
		return "", 0, nil
	}
	// GitHub Actions commonly prefixes commands with one or more environment
	// assignments (for example `GOFLAGS=-p=2 go test ./...`). These are shell
	// syntax, not executable names. Preserve their semantics without invoking a
	// shell by moving validated leading assignments into exec.Cmd.Env.
	var assignments []string
	for len(parts) > 0 && isEnvironmentAssignment(parts[0]) {
		assignments = append(assignments, parts[0])
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", 1, fmt.Errorf("CI command contains environment assignments but no executable: %q", cmdStr)
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), assignments...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return buf.String(), exitErr.ExitCode(), err
		}
		return buf.String(), 1, err
	}
	return buf.String(), 0, nil
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isEnvironmentAssignment(token string) bool {
	name, _, ok := strings.Cut(token, "=")
	return ok && environmentName.MatchString(name)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
