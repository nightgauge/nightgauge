package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FormatResult is the output of the format-on-save hook.
type FormatResult struct {
	Formatted bool   `json:"formatted"`
	Formatter string `json:"formatter,omitempty"`
	Error     string `json:"error,omitempty"`
}

// formatterConfig maps file extensions to formatter commands.
var formatterConfig = map[string][]string{
	".ts":   {"npx", "prettier", "--write"},
	".tsx":  {"npx", "prettier", "--write"},
	".js":   {"npx", "prettier", "--write"},
	".jsx":  {"npx", "prettier", "--write"},
	".json": {"npx", "prettier", "--write"},
	".md":   {"npx", "prettier", "--write"},
	".py":   nil, // resolved dynamically: ruff or black
	".sh":   {"shfmt", "-w"},
	".bash": {"shfmt", "-w"},
	".go":   {"gofmt", "-w"},
	".rs":   {"rustfmt"},
}

// ValidateFilePath rejects paths with directory traversal or absolute components.
func ValidateFilePath(filePath string) error {
	cleanPath := filepath.Clean(filePath)
	if filepath.IsAbs(cleanPath) {
		return fmt.Errorf("absolute paths not allowed: %s", filePath)
	}
	if strings.Contains(cleanPath, "..") {
		return fmt.Errorf("path traversal attempted: %s", filePath)
	}
	return nil
}

// EvaluateFormat runs the appropriate formatter for a file, resolved against
// the process's working directory.
// Returns immediately if no formatter is configured for the file type.
func EvaluateFormat(filePath string) FormatResult {
	return evaluateFormatIn(filePath, "")
}

// evaluateFormatIn runs the formatter for filePath, interpreting it relative to
// workdir. An empty workdir inherits the process's directory.
//
// The hook path resolves an absolute payload path against a workdir, so the
// formatter has to run in that same directory — otherwise the relative path it
// produced is resolved against an unrelated cwd and the formatter fails on a
// file that does exist.
func evaluateFormatIn(filePath, workdir string) FormatResult {
	if err := ValidateFilePath(filePath); err != nil {
		return FormatResult{Formatted: false, Error: err.Error()}
	}

	ext := strings.ToLower(filepath.Ext(filePath))

	args, ok := formatterConfig[ext]
	if !ok {
		return FormatResult{Formatted: false}
	}

	// Python: resolve to ruff or black
	if ext == ".py" {
		args = resolvePythonFormatter()
		if args == nil {
			return FormatResult{Formatted: false}
		}
	}

	// Check if formatter binary exists
	if _, err := exec.LookPath(args[0]); err != nil {
		return FormatResult{Formatted: false}
	}

	// Run formatter
	cmdArgs := append(args, filePath)
	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
	cmd.Dir = workdir
	if err := cmd.Run(); err != nil {
		return FormatResult{
			Formatted: false,
			Formatter: args[0],
			Error:     err.Error(),
		}
	}

	return FormatResult{
		Formatted: true,
		Formatter: args[0],
	}
}

// EvaluateFormatJSON returns the format result as JSON bytes.
func EvaluateFormatJSON(filePath string) ([]byte, error) {
	result := EvaluateFormat(filePath)
	return json.Marshal(result)
}

// resolvePythonFormatter returns the formatter command for Python files.
// Prefers ruff over black.
func resolvePythonFormatter() []string {
	if _, err := exec.LookPath("ruff"); err == nil {
		return []string{"ruff", "format"}
	}
	if _, err := exec.LookPath("black"); err == nil {
		return []string{"black"}
	}
	return nil
}

// FormattersAvailable returns a list of formatters and their availability.
func FormattersAvailable() map[string]bool {
	formatters := []string{"prettier", "npx", "black", "ruff", "shfmt", "gofmt", "rustfmt"}
	result := make(map[string]bool, len(formatters))
	for _, f := range formatters {
		_, err := exec.LookPath(f)
		result[f] = err == nil
	}
	return result
}

// FormatToolInput is the parsed tool_input for format hook.
type FormatToolInput struct {
	FilePath string `json:"file_path"`
}

// EvaluateFormatFromHook processes a PostToolUse hook input for Edit/Write.
//
// PostToolUse hooks are invoked with the payload on stdin and no argv, so this
// is the path format-on-save.sh actually exercises — the --file flag it used to
// require was never supplied by the harness (#354).
func EvaluateFormatFromHook(inputJSON []byte) FormatResult {
	workdir, err := os.Getwd()
	if err != nil {
		workdir = ""
	}
	return evaluateFormatFromHook(inputJSON, workdir)
}

func evaluateFormatFromHook(inputJSON []byte, workdir string) FormatResult {
	var input struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		return FormatResult{Formatted: false}
	}

	if input.ToolName != "Edit" && input.ToolName != "Write" {
		return FormatResult{Formatted: false}
	}

	var toolInput FormatToolInput
	if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil || toolInput.FilePath == "" {
		return FormatResult{Formatted: false}
	}

	// Claude Code reports Write/Edit file_path as an ABSOLUTE path, which
	// ValidateFilePath rejects outright. Without this step the hook parses its
	// payload correctly and then refuses every real file it is handed, so
	// format-on-save stays a no-op. Relativizing against the working directory
	// keeps the containment property that made the check worth having: a path
	// that resolves outside the project is still refused.
	relPath, err := relativizeHookPath(toolInput.FilePath, workdir)
	if err != nil {
		return FormatResult{Formatted: false, Error: err.Error()}
	}

	return evaluateFormatIn(relPath, workdir)
}

// relativizeHookPath converts a hook-supplied absolute path into one relative
// to workdir. Relative paths pass through untouched; paths that escape workdir
// are rejected rather than silently formatted.
//
// Symlinked roots are resolved on both sides before comparing because macOS
// hands out /tmp for what os.Getwd() reports as /private/tmp — comparing the
// two unresolved would read as an escape and refuse a legitimate file.
func relativizeHookPath(filePath, workdir string) (string, error) {
	if !filepath.IsAbs(filePath) {
		return filePath, nil
	}
	if workdir == "" {
		return "", fmt.Errorf("absolute path requires a working directory: %s", filePath)
	}

	workdirs := []string{workdir}
	if resolved, err := filepath.EvalSymlinks(workdir); err == nil && resolved != workdir {
		workdirs = append(workdirs, resolved)
	}

	filePaths := []string{filePath}
	if resolvedDir, err := filepath.EvalSymlinks(filepath.Dir(filePath)); err == nil {
		if resolved := filepath.Join(resolvedDir, filepath.Base(filePath)); resolved != filePath {
			filePaths = append(filePaths, resolved)
		}
	}

	for _, wd := range workdirs {
		for _, fp := range filePaths {
			rel, err := filepath.Rel(wd, fp)
			if err != nil {
				continue
			}
			if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			return rel, nil
		}
	}

	return "", fmt.Errorf("path outside working directory: %s", filePath)
}

// SupportedExtensions returns the list of file extensions that have formatters.
func SupportedExtensions() []string {
	exts := make([]string, 0, len(formatterConfig))
	for ext := range formatterConfig {
		exts = append(exts, ext)
	}
	return exts
}

// FormatterForExt returns the formatter name for a given extension, or empty string.
func FormatterForExt(ext string) string {
	args, ok := formatterConfig[strings.ToLower(ext)]
	if !ok || args == nil {
		if strings.ToLower(ext) == ".py" {
			f := resolvePythonFormatter()
			if f != nil {
				return f[0]
			}
			return ""
		}
		return ""
	}
	// Return the actual formatter name (skip "npx" prefix)
	if args[0] == "npx" && len(args) > 1 {
		return fmt.Sprintf("npx %s", args[1])
	}
	return args[0]
}
