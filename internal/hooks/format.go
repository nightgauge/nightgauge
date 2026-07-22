package hooks

import (
	"encoding/json"
	"fmt"
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

// EvaluateFormat runs the appropriate formatter for a file.
// Returns immediately if no formatter is configured for the file type.
func EvaluateFormat(filePath string) FormatResult {
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
func EvaluateFormatFromHook(inputJSON []byte) FormatResult {
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

	return EvaluateFormat(toolInput.FilePath)
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
