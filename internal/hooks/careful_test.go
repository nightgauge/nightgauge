package hooks

import (
	"fmt"
	"testing"

	"github.com/nightgauge/nightgauge/internal/careful"
)

func TestCarefulGateOffAllowsEverything(t *testing.T) {
	root := t.TempDir() // careful not enabled
	input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":"docker compose down -v"}}`, root)
	if got := EvaluateCarefulGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("careful off must allow, got %q", got.Decision)
	}
}

func TestCarefulGateOnBlocksDestructive(t *testing.T) {
	root := t.TempDir()
	if err := careful.Enable(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":"docker compose down -v"}}`, root)
	got := EvaluateCarefulGate([]byte(input))
	if got.Decision != "block" {
		t.Fatalf("careful on must block docker compose down -v, got %q", got.Decision)
	}
	if got.Reason == "" {
		t.Fatal("block must carry a reason")
	}
}

func TestCarefulGateOnAllowsSafe(t *testing.T) {
	root := t.TempDir()
	if err := careful.Enable(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":"docker compose up -d"}}`, root)
	if got := EvaluateCarefulGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("careful on must allow safe commands, got %q", got.Decision)
	}
}

func TestCarefulGateIgnoresNonBash(t *testing.T) {
	root := t.TempDir()
	if err := careful.Enable(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	input := fmt.Sprintf(`{"tool_name":"Edit","cwd":%q,"tool_input":{"file_path":"x"}}`, root)
	if got := EvaluateCarefulGate([]byte(input)); got.Decision != "allow" {
		t.Fatalf("non-Bash must allow, got %q", got.Decision)
	}
}

func TestCarefulGateMalformedAllows(t *testing.T) {
	if got := EvaluateCarefulGate([]byte("nope")); got.Decision != "allow" {
		t.Fatalf("malformed must fail open, got %q", got.Decision)
	}
}

// TestCarefulGateAllowsProse locks in the #4069 fix end-to-end: with careful
// mode ON, a command whose only destructive content is quoted prose (echo,
// commit message, --body) is allowed because the real program is not a
// destructive op. A genuine destructive op is still blocked.
func TestCarefulGateAllowsProse(t *testing.T) {
	root := t.TempDir()
	if err := careful.Enable(root, 0, ""); err != nil {
		t.Fatal(err)
	}
	allowed := []string{
		`echo "remember: docker compose down -v wipes prod"`,
		`git commit -m "explain why kubectl delete is dangerous"`,
		`gh issue comment 9 --body "we used a non-destructive DROP-free migration"`,
	}
	for _, cmd := range allowed {
		input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":%q}}`, root, cmd)
		if got := EvaluateCarefulGate([]byte(input)); got.Decision != "allow" {
			t.Errorf("careful on must allow quoted prose %q, got block: %s", cmd, got.Reason)
		}
	}
	// Real destructive op piped into a SQL client is still blocked.
	input := fmt.Sprintf(`{"tool_name":"Bash","cwd":%q,"tool_input":{"command":"echo 'DROP TABLE x' | psql"}}`, root)
	if got := EvaluateCarefulGate([]byte(input)); got.Decision != "block" {
		t.Errorf("careful on must block `echo 'DROP TABLE x' | psql`, got allow")
	}
}
