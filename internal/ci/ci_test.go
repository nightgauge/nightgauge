package ci

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDiscoverCommands_NoWorkflow_NodeFallback(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"scripts":{"format:check":"prettier --check .","lint":"eslint .","build":"tsc","test":"vitest"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverCommands(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Framework != "node" {
		t.Errorf("expected framework=node, got %s", result.Framework)
	}
	if len(result.Commands) == 0 {
		t.Error("expected non-empty commands from fallback")
	}
	// Verify expected commands are present
	cmdMap := make(map[string]bool)
	for _, c := range result.Commands {
		cmdMap[c] = true
	}
	for _, expected := range []string{"npm run format:check", "npm run lint", "npm run build", "npm run test"} {
		if !cmdMap[expected] {
			t.Errorf("expected command %q in fallback, got %v", expected, result.Commands)
		}
	}
}

func TestDiscoverCommands_NoFiles_EmptyFallback(t *testing.T) {
	dir := t.TempDir()
	result, err := DiscoverCommands(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Framework != "unknown" {
		t.Errorf("expected framework=unknown, got %s", result.Framework)
	}
	if result.WorkflowPath != "" {
		t.Errorf("expected empty workflow_path, got %s", result.WorkflowPath)
	}
}

func TestDiscoverCommands_GoMod_Fallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DiscoverCommands(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Framework != "go" {
		t.Errorf("expected framework=go, got %s", result.Framework)
	}
	cmdMap := make(map[string]bool)
	for _, c := range result.Commands {
		cmdMap[c] = true
	}
	if !cmdMap["go build ./..."] {
		t.Errorf("expected 'go build ./...' in fallback commands, got %v", result.Commands)
	}
	if !cmdMap["go test ./..."] {
		t.Errorf("expected 'go test ./...' in fallback commands, got %v", result.Commands)
	}
}

func TestDiscoverCommands_WorkflowParsing(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}

	workflowYAML := `
jobs:
  build:
    steps:
      - name: Check out code
        uses: actions/checkout@v4
      - name: Install deps
        run: npm ci
      - name: Lint
        run: npm run lint
      - name: Build
        run: npm run build
      - name: Test
        run: npm run test -- --run
`
	wfPath := filepath.Join(wfDir, "ci.yml")
	if err := os.WriteFile(wfPath, []byte(workflowYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := DiscoverCommands(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.WorkflowPath != wfPath {
		t.Errorf("expected workflow_path=%s, got %s", wfPath, result.WorkflowPath)
	}
	// npm ci should be filtered; lint, build, test should remain
	cmdMap := make(map[string]bool)
	for _, c := range result.Commands {
		cmdMap[c] = true
	}
	if cmdMap["npm ci"] {
		t.Error("npm ci should have been filtered out")
	}
	if !cmdMap["npm run lint"] {
		t.Errorf("expected npm run lint, got %v", result.Commands)
	}
	if !cmdMap["npm run build"] {
		t.Errorf("expected npm run build, got %v", result.Commands)
	}
}

// TestSplitLogicalCommands covers backslash-continuation joining (#194): the
// bowlsheet#233 incident split a continued `flutter test` invocation into
// three fragments and exec'd each separately, producing phantom hard-gate
// failures (`--tags=e2e` executed as a program).
func TestSplitLogicalCommands(t *testing.T) {
	cases := []struct {
		name string
		run  string
		want []string
	}{
		{
			name: "backslash continuation joins into one command",
			run: "flutter test integration_test/sync_e2e \\\n" +
				"  --tags=e2e \\\n" +
				"  --dart-define=API_URL=http://localhost:8080\n",
			want: []string{
				"flutter test integration_test/sync_e2e --tags=e2e --dart-define=API_URL=http://localhost:8080",
			},
		},
		{
			name: "trailing whitespace after the backslash still continues",
			run:  "npm run build \\ \n  --workspace app\n",
			want: []string{"npm run build --workspace app"},
		},
		{
			name: "comments interleaved terminate a pending continuation",
			run: "npm run lint \\\n" +
				"# a stray comment\n" +
				"npm run test\n",
			want: []string{"npm run lint", "# a stray comment", "npm run test"},
		},
		{
			name: "plain multi-line block splits per line",
			run:  "npm run lint\nnpm run test\n",
			want: []string{"npm run lint", "npm run test"},
		},
		{
			name: "trailing continuation at end of block flushes",
			run:  "npm run lint \\",
			want: []string{"npm run lint"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := splitLogicalCommands(tc.run)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d commands %v, want %d %v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("command[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestDiscoverCommands_BackslashContinuation(t *testing.T) {
	dir := t.TempDir()
	wfDir := filepath.Join(dir, ".github", "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}

	workflowYAML := `
jobs:
  integration:
    steps:
      - name: E2E
        run: |
          flutter test integration_test/sync_e2e \
            --tags=e2e \
            --dart-define=API_URL=http://localhost:8080
          flutter analyze
`
	wfPath := filepath.Join(wfDir, "ci.yml")
	if err := os.WriteFile(wfPath, []byte(workflowYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := DiscoverCommands(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"flutter test integration_test/sync_e2e --tags=e2e --dart-define=API_URL=http://localhost:8080",
		"flutter analyze",
	}
	if len(result.Commands) != len(want) {
		t.Fatalf("commands = %v, want %v", result.Commands, want)
	}
	for i := range want {
		if result.Commands[i] != want[i] {
			t.Errorf("commands[%d] = %q, want %q", i, result.Commands[i], want[i])
		}
	}
}

func TestDiscoverCommands_ExplicitWorkflowPath(t *testing.T) {
	dir := t.TempDir()
	wfContent := `
jobs:
  ci:
    steps:
      - name: Run checks
        run: npm run format:check
      - name: Setup
        run: npm install
`
	wfPath := filepath.Join(dir, "custom.yml")
	if err := os.WriteFile(wfPath, []byte(wfContent), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := DiscoverCommands(context.Background(), dir, wfPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.WorkflowPath != wfPath {
		t.Errorf("expected workflow_path=%s, got %s", wfPath, result.WorkflowPath)
	}
	// npm install filtered, format:check kept
	if len(result.Commands) != 1 || result.Commands[0] != "npm run format:check" {
		t.Errorf("expected [npm run format:check], got %v", result.Commands)
	}
}

func TestCheckParity_AllPass(t *testing.T) {
	dir := t.TempDir()
	commands := []string{"echo pass1", "echo pass2"}
	result, err := CheckParity(context.Background(), dir, commands)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Errorf("expected Passed=true, got failures: %v", result.Failures)
	}
	if len(result.Failures) != 0 {
		t.Errorf("expected 0 failures, got %d", len(result.Failures))
	}
}

func TestCheckParity_OneFails(t *testing.T) {
	dir := t.TempDir()
	commands := []string{"echo pass", "false"}
	result, err := CheckParity(context.Background(), dir, commands)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Passed {
		t.Error("expected Passed=false")
	}
	if len(result.Failures) != 1 {
		t.Errorf("expected 1 failure, got %d", len(result.Failures))
	}
	if result.Failures[0].Command != "false" {
		t.Errorf("expected failed command=false, got %s", result.Failures[0].Command)
	}
}

func TestCheckParity_EmptyCommands(t *testing.T) {
	dir := t.TempDir()
	result, err := CheckParity(context.Background(), dir, []string{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Passed {
		t.Error("expected Passed=true for empty commands")
	}
}

func TestRunShellCmd_LeadingEnvironmentAssignments(t *testing.T) {
	out, exitCode, err := runShellCmd(t.TempDir(),
		`NIGHTGAUGE_CI_ASSIGNMENT=present NIGHTGAUGE_SECOND="two words" sh -c 'printf "%s|%s" "$NIGHTGAUGE_CI_ASSIGNMENT" "$NIGHTGAUGE_SECOND"'`)
	if err != nil || exitCode != 0 {
		t.Fatalf("runShellCmd() error = %v, exitCode = %d, output = %q", err, exitCode, out)
	}
	if out != "present|two words" {
		t.Fatalf("output = %q, want %q", out, "present|two words")
	}
}

func TestRunShellCmd_AssignmentWithoutExecutableFails(t *testing.T) {
	_, exitCode, err := runShellCmd(t.TempDir(), "GOFLAGS=-p=2")
	if err == nil || exitCode == 0 {
		t.Fatalf("runShellCmd() error = %v, exitCode = %d; want a failure", err, exitCode)
	}
}

func TestClassifyFailureType(t *testing.T) {
	cases := []struct {
		cmd  string
		want string
	}{
		{"npm run format:check", "format"},
		{"npx prettier --check .", "format"},
		{"npm run lint", "lint"},
		{"eslint src", "lint"},
		{"npm run typecheck", "typecheck"},
		{"tsc --noEmit", "typecheck"},
		{"npm run build", "build"},
		{"go build ./...", "build"},
		{"npm run test", "test"},
		{"go test ./...", "test"},
		{"npx vitest run", "test"},
		{"something-else", "unknown"},
	}
	for _, c := range cases {
		got := classifyFailureType(c.cmd)
		if got != c.want {
			t.Errorf("classifyFailureType(%q) = %q, want %q", c.cmd, got, c.want)
		}
	}
}

func TestShouldSkip(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"npm ci", true},
		{"npm install", true},
		{"actions/checkout@v4", true},
		{"setup-node --node-version 20", true},
		{"upload-artifact -name dist", true},
		{"npm run lint", false},
		{"go build ./...", false},
		{"dart analyze", false},
	}
	for _, c := range cases {
		got := shouldSkip(c.line)
		if got != c.want {
			t.Errorf("shouldSkip(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestDetectFramework(t *testing.T) {
	t.Run("node", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectFramework(dir); got != "node" {
			t.Errorf("expected node, got %s", got)
		}
	})
	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\ngo 1.21\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectFramework(dir); got != "go" {
			t.Errorf("expected go, got %s", got)
		}
	})
	t.Run("flutter", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "pubspec.yaml"), []byte("name: app"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := detectFramework(dir); got != "flutter" {
			t.Errorf("expected flutter, got %s", got)
		}
	})
	t.Run("unknown", func(t *testing.T) {
		dir := t.TempDir()
		if got := detectFramework(dir); got != "unknown" {
			t.Errorf("expected unknown, got %s", got)
		}
	})
}

func TestTokenizeCommand(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"simple", "npm run build", []string{"npm", "run", "build"}},
		{"empty", "", nil},
		{"whitespace only", "   \t  ", nil},
		{"extra spaces collapse", "go   test    ./...", []string{"go", "test", "./..."}},
		{"double-quoted arg with space", `prettier --write "src/a b.ts"`, []string{"prettier", "--write", "src/a b.ts"}},
		{"single-quoted arg with space", `sh -c 'echo hi there'`, []string{"sh", "-c", "echo hi there"}},
		{"escaped space outside quotes", `cmd a\ b`, []string{"cmd", "a b"}},
		{"double-quote escaped quote", `echo "a\"b"`, []string{"echo", `a"b`}},
		{"empty quoted arg", `cmd ""`, []string{"cmd", ""}},
		{"adjacent quoted concatenation", `cmd "a"'b'c`, []string{"cmd", "abc"}},
		{"flags with equals and quotes", `npx vitest run --reporter="json file"`, []string{"npx", "vitest", "run", `--reporter=json file`}},
		// Shell metacharacters are NOT interpreted — exec.Command runs the binary
		// directly (no `sh -c`), so these stay literal argument text. This locks in
		// that the tokenizer must never grow shell-expansion behavior.
		{"semicolon stays literal", "echo a;b", []string{"echo", "a;b"}},
		{"pipe stays literal", "echo a|b", []string{"echo", "a|b"}},
		{"command-substitution stays literal", "echo $(whoami)", []string{"echo", "$(whoami)"}},
		{"single-quotes preserve backslash literally", `cmd 'a\b'`, []string{"cmd", `a\b`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tokenizeCommand(tc.in)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tokenizeCommand(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}

func TestTokenizeCommand_UnterminatedQuote(t *testing.T) {
	for _, in := range []string{`echo "unterminated`, `echo 'unterminated`, `prettier --write "a b`} {
		if _, err := tokenizeCommand(in); err == nil {
			t.Errorf("tokenizeCommand(%q) expected an unterminated-quote error, got nil", in)
		}
	}
}
