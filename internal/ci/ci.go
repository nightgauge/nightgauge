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
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/detect"
)

// DiscoverResult is the output of DiscoverCommands.
type DiscoverResult struct {
	Commands     []string `json:"commands"`
	WorkflowPath string   `json:"workflow_path"`
	Framework    string   `json:"framework"` // "node" | "go" | "flutter" | "unknown"
	Timestamp    string   `json:"timestamp"`
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
	Jobs map[string]jobYAML `yaml:"jobs"`
}

type jobYAML struct {
	Steps []stepYAML `yaml:"steps"`
}

type stepYAML struct {
	Run string `yaml:"run"`
}

// skipPatterns are substrings that identify install/setup steps to exclude.
var skipPatterns = []string{
	"npm ci", "npm install", "checkout", "setup-node", "setup-go",
	"upload-artifact", "download-artifact", "actions/",
}

// DiscoverCommands parses the CI workflow to extract run-step commands.
// When workflowPath is empty, it auto-discovers .github/workflows/ci.yml or ci.yaml.
// Falls back to standard commands derived from project type when no workflow is found.
func DiscoverCommands(_ context.Context, workdir, workflowPath string) (DiscoverResult, error) {
	ts := time.Now().UTC().Format(time.RFC3339)

	wfPath := workflowPath
	if wfPath == "" {
		for _, candidate := range []string{
			filepath.Join(workdir, ".github", "workflows", "ci.yml"),
			filepath.Join(workdir, ".github", "workflows", "ci.yaml"),
		} {
			if fileExists(candidate) {
				wfPath = candidate
				break
			}
		}
	}

	var commands []string
	if wfPath != "" {
		commands = parseWorkflowCommands(wfPath)
	}

	framework := detectFramework(workdir)

	if len(commands) == 0 {
		commands = fallbackCommands(workdir, framework)
	}

	return DiscoverResult{
		Commands:     commands,
		WorkflowPath: wfPath,
		Framework:    framework,
		Timestamp:    ts,
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

func parseWorkflowCommands(wfPath string) []string {
	data, err := os.ReadFile(wfPath)
	if err != nil {
		return nil
	}

	var wf workflowYAML
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil
	}

	var commands []string
	seen := make(map[string]bool)

	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Run == "" {
				continue
			}
			// Each "run:" block may contain multiple logical commands; split
			// on newlines AFTER joining backslash continuations — splitting a
			// continued command per physical line executed each fragment
			// separately and produced phantom hard-gate failures (#194).
			for _, line := range splitLogicalCommands(step.Run) {
				if line == "" || line[0] == '#' {
					continue
				}
				if shouldSkip(line) {
					continue
				}
				if !seen[line] {
					seen[line] = true
					commands = append(commands, line)
				}
			}
		}
	}

	return commands
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
