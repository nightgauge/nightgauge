package codexprovision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- ReadPipelineMcpServers ---

func TestReadPipelineMcpServers_McpJsonWinsOverSettings(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".claude", "settings.json"), `{
		"mcpServers": {
			"shared": {"command": "from-settings"},
			"only-settings": {"command": "s-cmd"}
		}
	}`)
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{
		"mcpServers": {
			"shared": {"command": "from-mcp-json"},
			"only-mcp": {"command": "m-cmd"}
		}
	}`)

	got := ReadPipelineMcpServers(dir)
	if len(got) != 3 {
		t.Fatalf("want 3 servers, got %d: %v", len(got), got)
	}
	if got["shared"].Command != "from-mcp-json" {
		t.Errorf(".mcp.json must win on name clash: shared.command = %q", got["shared"].Command)
	}
	if got["only-settings"].Command != "s-cmd" || got["only-mcp"].Command != "m-cmd" {
		t.Errorf("merge dropped a server: %+v", got)
	}
}

func TestReadPipelineMcpServers_MissingFiles(t *testing.T) {
	got := ReadPipelineMcpServers(t.TempDir())
	if len(got) != 0 {
		t.Errorf("want empty for no config files, got %v", got)
	}
}

func TestReadPipelineMcpServers_MalformedJsonTolerated(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{ this is not json `)
	got := ReadPipelineMcpServers(dir)
	if len(got) != 0 {
		t.Errorf("malformed JSON should yield no servers, got %v", got)
	}
}

func TestReadPipelineMcpServers_CoercesNonStringEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{
		"mcpServers": {
			"srv": {
				"command": "x",
				"args": [1, "ok", true, null],
				"env": {"PORT": 8080, "FLAG": true, "NAME": "v", "GONE": null}
			}
		}
	}`)
	got := ReadPipelineMcpServers(dir)["srv"]
	// Non-string args are dropped; only "ok" survives.
	if len(got.Args) != 1 || got.Args[0] != "ok" {
		t.Errorf("args coercion = %v, want [ok]", got.Args)
	}
	if got.Env["PORT"] != "8080" || got.Env["FLAG"] != "true" || got.Env["NAME"] != "v" {
		t.Errorf("env coercion = %v", got.Env)
	}
	if _, ok := got.Env["GONE"]; ok {
		t.Errorf("null env value should be dropped: %v", got.Env)
	}
}

// --- ComputeNextCodexConfig: provisioning into a fresh file ---

func TestComputeNextCodexConfig_FreshStdioServer(t *testing.T) {
	servers := map[string]PipelineMcpServer{
		"fs": {Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"}, Env: map[string]string{"DEBUG": "1"}},
	}
	next, provisioned, skipped := ComputeNextCodexConfig("", false, servers)

	assertContains(t, next, mcpManagedBegin)
	assertContains(t, next, mcpManagedEnd)
	assertContains(t, next, "[mcp_servers.fs]")
	assertContains(t, next, `command = "npx"`)
	assertContains(t, next, `args = ["-y", "@modelcontextprotocol/server-filesystem", "/tmp"]`)
	assertContains(t, next, `env = { DEBUG = "1" }`)
	if len(provisioned) != 1 || provisioned[0] != "fs" {
		t.Errorf("provisioned = %v, want [fs]", provisioned)
	}
	if len(skipped) != 0 {
		t.Errorf("skipped = %v, want none", skipped)
	}
}

func TestComputeNextCodexConfig_HttpServerWithBearer(t *testing.T) {
	servers := map[string]PipelineMcpServer{
		"api": {
			Type:    "http",
			URL:     "https://example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${API_TOKEN}", "X-Trace": "on"},
		},
	}
	next, _, _ := ComputeNextCodexConfig("", false, servers)
	assertContains(t, next, `url = "https://example.com/mcp"`)
	assertContains(t, next, `bearer_token_env_var = "API_TOKEN"`)
	// The Authorization header is consumed into bearer_token_env_var; the rest stay.
	assertContains(t, next, `http_headers = { X-Trace = "on" }`)
	if strings.Contains(next, "Authorization") {
		t.Errorf("Authorization header should be lifted into bearer_token_env_var, got:\n%s", next)
	}
}

// --- Parity with the TS reference (#4041): type-unspecified→http, header
// coercion, and sorted inline-table keys. ---

func TestComputeNextCodexConfig_UrlWithCommandIsHttp(t *testing.T) {
	// A server with both url and command but no type is HTTP (url wins) — the
	// same result the TS path produces for an absent/null type.
	servers := map[string]PipelineMcpServer{
		"srv": {URL: "https://u/mcp", Command: "should-be-ignored"},
	}
	next, _, _ := ComputeNextCodexConfig("", false, servers)
	assertContains(t, next, `url = "https://u/mcp"`)
	if strings.Contains(next, "should-be-ignored") {
		t.Errorf("url-bearing server must be emitted as HTTP, dropping command:\n%s", next)
	}
}

func TestReadAndComputeCoercesNonStringHeaders(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{
		"mcpServers": {
			"api": {"type": "http", "url": "https://x/mcp", "headers": {"X-Version": 2, "X-Enabled": true, "X-Gone": null}}
		}
	}`)
	servers := ReadPipelineMcpServers(dir)
	next, _, _ := ComputeNextCodexConfig("", false, servers)
	// Non-string header values are coerced to strings (not dropped); null dropped;
	// keys sorted (X-Enabled before X-Version).
	assertContains(t, next, `http_headers = { X-Enabled = "true", X-Version = "2" }`)
	if strings.Contains(next, "X-Gone") {
		t.Errorf("null header value should be dropped:\n%s", next)
	}
}

func TestComputeNextCodexConfig_SortsInlineTableKeys(t *testing.T) {
	servers := map[string]PipelineMcpServer{
		"srv": {Command: "c", Env: map[string]string{"ZZ_VAR": "1", "AA_VAR": "2", "MM_VAR": "3"}},
	}
	next, _, _ := ComputeNextCodexConfig("", false, servers)
	assertContains(t, next, `env = { AA_VAR = "2", MM_VAR = "3", ZZ_VAR = "1" }`)
}

// --- Idempotency ---

func TestComputeNextCodexConfig_Idempotent(t *testing.T) {
	servers := map[string]PipelineMcpServer{
		"b": {Command: "b-cmd"},
		"a": {Command: "a-cmd"},
	}
	first, _, _ := ComputeNextCodexConfig("", false, servers)
	second, _, _ := ComputeNextCodexConfig(first, true, servers)
	if first != second {
		t.Errorf("not idempotent:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	// Deterministic order: server a before server b.
	if strings.Index(second, "[mcp_servers.a]") > strings.Index(second, "[mcp_servers.b]") {
		t.Errorf("server tables not sorted deterministically:\n%s", second)
	}
}

// --- User content preservation + collision (user wins) ---

func TestComputeNextCodexConfig_PreservesUserContentAndModel(t *testing.T) {
	existing := "model = \"gpt-5-codex\"\napproval_policy = \"on-request\"\n"
	next, _, _ := ComputeNextCodexConfig(existing, true, map[string]PipelineMcpServer{
		"fs": {Command: "npx"},
	})
	assertContains(t, next, `model = "gpt-5-codex"`)
	assertContains(t, next, `approval_policy = "on-request"`)
	assertContains(t, next, "[mcp_servers.fs]")
}

func TestComputeNextCodexConfig_UserDefinedServerWins(t *testing.T) {
	existing := "[mcp_servers.fs]\ncommand = \"my-own-fs\"\n"
	next, provisioned, skipped := ComputeNextCodexConfig(existing, true, map[string]PipelineMcpServer{
		"fs":    {Command: "pipeline-fs"},
		"extra": {Command: "extra-cmd"},
	})
	if len(skipped) != 1 || skipped[0] != "fs" {
		t.Errorf("skipped = %v, want [fs]", skipped)
	}
	if len(provisioned) != 1 || provisioned[0] != "extra" {
		t.Errorf("provisioned = %v, want [extra]", provisioned)
	}
	assertContains(t, next, `command = "my-own-fs"`)
	if strings.Contains(next, "pipeline-fs") {
		t.Errorf("pipeline must not overwrite the user's fs server:\n%s", next)
	}
	assertContains(t, next, "[mcp_servers.extra]")
}

func TestComputeNextCodexConfig_DottedKeyCollisionDetected(t *testing.T) {
	existing := `mcp_servers.fs = { command = "user-inline" }` + "\n"
	_, provisioned, skipped := ComputeNextCodexConfig(existing, true, map[string]PipelineMcpServer{
		"fs": {Command: "pipeline-fs"},
	})
	if len(skipped) != 1 || skipped[0] != "fs" {
		t.Errorf("dotted-key user server should collide: skipped=%v provisioned=%v", skipped, provisioned)
	}
}

// --- TOML escaping of control characters ---

func TestComputeNextCodexConfig_EscapesControlChars(t *testing.T) {
	servers := map[string]PipelineMcpServer{
		"x": {Command: "c", Env: map[string]string{"K": "line1\nline2\ttab\x00null"}},
	}
	next, _, _ := ComputeNextCodexConfig("", false, servers)
	assertContains(t, next, `\n`)
	assertContains(t, next, `\t`)
	assertContains(t, next, `\u0000`)
	// No raw newline must appear inside the emitted env value (would break TOML).
	if strings.Contains(next, "line1\nline2") {
		t.Errorf("raw newline leaked into TOML basic string:\n%s", next)
	}
}

// --- Empty servers strips an existing managed block ---

func TestComputeNextCodexConfig_EmptyServersStripsBlock(t *testing.T) {
	withBlock, _, _ := ComputeNextCodexConfig("model = \"x\"\n", true, map[string]PipelineMcpServer{
		"fs": {Command: "npx"},
	})
	assertContains(t, withBlock, mcpManagedBegin)

	stripped, provisioned, _ := ComputeNextCodexConfig(withBlock, true, map[string]PipelineMcpServer{})
	if strings.Contains(stripped, mcpManagedBegin) || strings.Contains(stripped, mcpManagedEnd) {
		t.Errorf("empty servers must strip the managed block:\n%s", stripped)
	}
	assertContains(t, stripped, `model = "x"`)
	if len(provisioned) != 0 {
		t.Errorf("provisioned = %v, want none", provisioned)
	}
}

// --- Missing END marker heals to EOF on next write ---

func TestComputeNextCodexConfig_MissingEndHealsToEOF(t *testing.T) {
	truncated := "user = \"keep\"\n\n" + mcpManagedBegin + "\n[mcp_servers.stale]\ncommand = \"old\"\n"
	next, _, _ := ComputeNextCodexConfig(truncated, true, map[string]PipelineMcpServer{
		"fresh": {Command: "new"},
	})
	assertContains(t, next, `user = "keep"`)
	assertContains(t, next, "[mcp_servers.fresh]")
	if strings.Contains(next, "stale") {
		t.Errorf("truncated (missing END) block should be healed/replaced, got:\n%s", next)
	}
	// Exactly one BEGIN and one END after healing.
	if strings.Count(next, mcpManagedBegin) != 1 || strings.Count(next, mcpManagedEnd) != 1 {
		t.Errorf("want exactly one well-formed managed block:\n%s", next)
	}
}

// --- helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected to contain %q, got:\n%s", needle, haystack)
	}
}
