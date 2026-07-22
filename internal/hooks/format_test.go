package hooks

import (
	"testing"
)

func TestFormatterForExt(t *testing.T) {
	tests := []struct {
		ext  string
		want string
	}{
		{".ts", "npx prettier"},
		{".tsx", "npx prettier"},
		{".js", "npx prettier"},
		{".json", "npx prettier"},
		{".md", "npx prettier"},
		{".go", "gofmt"},
		{".sh", "shfmt"},
		{".rs", "rustfmt"},
		{".unknown", ""},
		{".html", ""},
	}

	for _, tt := range tests {
		got := FormatterForExt(tt.ext)
		if got != tt.want {
			t.Errorf("FormatterForExt(%q) = %q, want %q", tt.ext, got, tt.want)
		}
	}
}

func TestSupportedExtensions(t *testing.T) {
	exts := SupportedExtensions()
	if len(exts) == 0 {
		t.Error("expected at least one supported extension")
	}

	// Verify .go is in the list
	found := false
	for _, ext := range exts {
		if ext == ".go" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected .go in supported extensions")
	}
}

func TestEvaluateFormatUnsupportedExt(t *testing.T) {
	result := EvaluateFormat("/tmp/file.xyz")
	if result.Formatted {
		t.Error("expected Formatted=false for unsupported extension")
	}
}

func TestEvaluateFormatFromHookNonEditTool(t *testing.T) {
	input := []byte(`{"tool_name":"Bash","tool_input":{"command":"ls"}}`)
	result := EvaluateFormatFromHook(input)
	if result.Formatted {
		t.Error("expected Formatted=false for Bash tool")
	}
}

func TestEvaluateFormatFromHookInvalidJSON(t *testing.T) {
	result := EvaluateFormatFromHook([]byte("not json"))
	if result.Formatted {
		t.Error("expected Formatted=false for invalid JSON")
	}
}

func TestValidateFilePathRejectsTraversal(t *testing.T) {
	malicious := []string{
		"../../etc/passwd",
		"../secret",
		"foo/../../etc/shadow",
		"a/b/../../../etc/passwd",
	}
	for _, p := range malicious {
		if err := ValidateFilePath(p); err == nil {
			t.Errorf("ValidateFilePath(%q): expected error, got nil", p)
		}
	}
}

func TestValidateFilePathRejectsAbsolutePaths(t *testing.T) {
	absolute := []string{
		"/etc/passwd",
		"/tmp/file.go",
		"/home/user/file.ts",
	}
	for _, p := range absolute {
		if err := ValidateFilePath(p); err == nil {
			t.Errorf("ValidateFilePath(%q): expected error, got nil", p)
		}
	}
}

func TestValidateFilePathAcceptsRelativePaths(t *testing.T) {
	valid := []string{
		"file.go",
		"./file.go",
		"src/main.go",
		"packages/foo/bar.ts",
	}
	for _, p := range valid {
		if err := ValidateFilePath(p); err != nil {
			t.Errorf("ValidateFilePath(%q): unexpected error: %v", p, err)
		}
	}
}

func TestEvaluateFormatRejectsPathTraversal(t *testing.T) {
	result := EvaluateFormat("../../etc/passwd")
	if result.Formatted {
		t.Error("expected Formatted=false for traversal path")
	}
	if result.Error == "" {
		t.Error("expected non-empty Error for traversal path")
	}
}
