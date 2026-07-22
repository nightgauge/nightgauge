package format

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FormatResult is the structured output of a project-level format run.
// Distinct from internal/hooks/format.go which operates per-file.
type FormatResult struct {
	Ran       bool   `json:"ran"`
	Formatter string `json:"formatter"`
	Output    string `json:"output"`
	Timestamp string `json:"timestamp"`
}

// RunFormat detects the project formatter in workdir and runs it.
// Detection order: npm format → .prettierrc* → dprint.json → go.mod → pubspec.yaml.
func RunFormat(ctx context.Context, workdir string) (FormatResult, error) {
	switch {
	case hasPkgScript(workdir, "format"):
		return run(ctx, workdir, "npm run format", "npm", "run", "format")

	case hasPrettierConfig(workdir):
		return run(ctx, workdir, "npx prettier", "npx", "prettier", "--write", ".")

	case fileExists(filepath.Join(workdir, "dprint.json")):
		return run(ctx, workdir, "dprint", "npx", "dprint", "fmt")

	case fileExists(filepath.Join(workdir, "go.mod")):
		return run(ctx, workdir, "go fmt", "go", "fmt", "./...")

	case fileExists(filepath.Join(workdir, "pubspec.yaml")):
		return runMulti(ctx, workdir, "dart format",
			[][]string{{"dart", "fix", "--apply"}, {"dart", "format", "."}},
		)
	}

	return FormatResult{
		Ran:       false,
		Formatter: "",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// RunFormatJSON is a convenience wrapper returning JSON bytes.
func RunFormatJSON(ctx context.Context, workdir string) ([]byte, error) {
	result, err := RunFormat(ctx, workdir)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

func run(ctx context.Context, workdir, formatterName string, name string, args ...string) (FormatResult, error) {
	out, err := runCmd(ctx, workdir, name, args...)
	ts := time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		return FormatResult{Ran: true, Formatter: formatterName, Output: out, Timestamp: ts}, err
	}
	return FormatResult{Ran: true, Formatter: formatterName, Output: out, Timestamp: ts}, nil
}

func runMulti(ctx context.Context, workdir, formatterName string, cmds [][]string) (FormatResult, error) {
	var combined strings.Builder
	ts := time.Now().UTC().Format(time.RFC3339)
	for _, c := range cmds {
		out, err := runCmd(ctx, workdir, c[0], c[1:]...)
		combined.WriteString(out)
		if err != nil {
			return FormatResult{Ran: true, Formatter: formatterName, Output: combined.String(), Timestamp: ts}, err
		}
	}
	return FormatResult{Ran: true, Formatter: formatterName, Output: combined.String(), Timestamp: ts}, nil
}

func hasPkgScript(workdir, script string) bool {
	data, err := os.ReadFile(filepath.Join(workdir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return strings.Contains(string(data), `"`+script+`"`)
	}
	_, ok := pkg.Scripts[script]
	return ok
}

func hasPrettierConfig(workdir string) bool {
	patterns := []string{
		".prettierrc", ".prettierrc.json", ".prettierrc.js", ".prettierrc.cjs",
		".prettierrc.yaml", ".prettierrc.yml", ".prettierrc.toml",
		"prettier.config.js", "prettier.config.cjs", "prettier.config.mjs",
	}
	for _, p := range patterns {
		if fileExists(filepath.Join(workdir, p)) {
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCmd(ctx context.Context, workdir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workdir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
