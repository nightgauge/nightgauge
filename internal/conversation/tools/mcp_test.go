package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// mcpClient is the turn's adapter as the server sees it: the config file it
// was handed, and JSON-RPC over HTTP.
type mcpClient struct {
	t     *testing.T
	url   string
	token string
	id    int
}

func startServer(t *testing.T, s *Toolset) (*Server, *mcpClient) {
	t.Helper()
	srv, err := Start(s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := srv.WriteConfig(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config file mode %v (%v), want 0600", info.Mode(), err)
	}
	raw, _ := os.ReadFile(path)
	var cfg struct {
		MCPServers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	entry, ok := cfg.MCPServers[ServerName]
	if !ok || len(cfg.MCPServers) != 1 || entry.Type != "http" || !strings.HasPrefix(entry.URL, "http://127.0.0.1:") {
		t.Fatalf("config = %s", raw)
	}
	return srv, &mcpClient{t: t, url: entry.URL, token: strings.TrimPrefix(entry.Headers["Authorization"], "Bearer ")}
}

func (c *mcpClient) post(body []byte, mutate func(*http.Request)) (*http.Response, []byte) {
	c.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

func (c *mcpClient) rpc(method string, params any) (result map[string]any, rpcErr map[string]any) {
	c.t.Helper()
	c.id++
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.id, "method": method, "params": params})
	resp, out := c.post(body, nil)
	if resp.StatusCode != http.StatusOK {
		c.t.Fatalf("%s: HTTP %d %s", method, resp.StatusCode, out)
	}
	var r struct {
		ID     int            `json:"id"`
		Result map[string]any `json:"result"`
		Error  map[string]any `json:"error"`
	}
	if err := json.Unmarshal(out, &r); err != nil || r.ID != c.id {
		c.t.Fatalf("%s: reply %s (%v)", method, out, err)
	}
	return r.Result, r.Error
}

// The server speaks MCP to the turn's adapter: it initializes, lists exactly
// the registered tools with their read-only hints, answers a call, refuses
// an unknown tool, and accepts notifications.
func TestServer_SpeaksMCPAndOffersExactlyTheTools(t *testing.T) {
	_, c := startServer(t, toolset(t, gitRepo(t), newFakeForge(), operatorTurn("hi")))
	init, rerr := c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "test", "version": "1"}})
	if rerr != nil || init["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %v %v", init, rerr)
	}
	if init, _ := c.rpc("initialize", map[string]any{"protocolVersion": "1999-01-01"}); init["protocolVersion"] != protocolVersions[0] {
		t.Errorf("an unknown revision is answered with %v", init["protocolVersion"])
	}
	resp, _ := c.post([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification: HTTP %d", resp.StatusCode)
	}

	list, rerr := c.rpc("tools/list", map[string]any{})
	if rerr != nil {
		t.Fatal(rerr)
	}
	var names []string
	for _, raw := range list["tools"].([]any) {
		tool := raw.(map[string]any)
		name := tool["name"].(string)
		names = append(names, name)
		readOnly := tool["annotations"].(map[string]any)["readOnlyHint"].(bool)
		if readOnly == IsMutating(name) {
			t.Errorf("%s: readOnlyHint %v", name, readOnly)
		}
		if tool["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("%s: schema %v", name, tool["inputSchema"])
		}
	}
	if !slices.Equal(names, Names()) {
		t.Errorf("tools/list = %v, want %v", names, Names())
	}

	res, rerr := c.rpc("tools/call", map[string]any{"name": ToolReadFile, "arguments": map[string]any{"path": "README.md"}})
	if rerr != nil || res["isError"] != false || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "The API service.") {
		t.Errorf("read_file = %v %v", res, rerr)
	}
	res, _ = c.rpc("tools/call", map[string]any{"name": ToolReadFile, "arguments": map[string]any{"path": "../x"}})
	if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), CodeOutsideWorkspace) {
		t.Errorf("a refused call = %v", res)
	}
	for _, name := range []string{"write_file", "Bash", "git_push", "mcp__nightgauge__read_file"} {
		if _, rerr := c.rpc("tools/call", map[string]any{"name": name, "arguments": map[string]any{}}); rerr == nil || rerr["code"] != float64(-32602) {
			t.Errorf("unknown tool %s: %v", name, rerr)
		}
	}
	if _, rerr := c.rpc("resources/list", map[string]any{}); rerr == nil || rerr["code"] != float64(-32601) {
		t.Errorf("an unoffered method: %v", rerr)
	}
}

// Nothing but the turn's own adapter reaches the server: a request without
// the turn's token, with another token, from a browser origin, or addressed
// to another host name is refused before it is read.
func TestServer_RefusesAnyoneButItsTurn(t *testing.T) {
	forge := newFakeForge()
	srv, c := startServer(t, toolset(t, gitRepo(t), forge, operatorTurn("Please file an issue for this.")))
	call := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":` +
		`{"owner":"acme","repo":"api","title":"t","body":"b","request_quote":"file an issue for this"}}}`)
	cases := map[string]struct {
		mutate func(*http.Request)
		status int
	}{
		"no token":      {func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		"another token": {func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("0", 64)) }, http.StatusUnauthorized},
		"a browser":     {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		"rebound DNS":   {func(r *http.Request) { r.Host = "evil.example:80" }, http.StatusForbidden},
		"a GET":         {func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
	}
	for name, tc := range cases {
		if resp, _ := c.post(call, tc.mutate); resp.StatusCode != tc.status {
			t.Errorf("%s: HTTP %d, want %d", name, resp.StatusCode, tc.status)
		}
	}
	if resp, _ := c.post([]byte(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`), nil); resp.StatusCode != http.StatusOK {
		t.Errorf("batch: HTTP %d", resp.StatusCode)
	}
	if len(forge.createdIssues()) != 0 {
		t.Fatal("a refused request reached the tool")
	}
	srv.Close()
	if _, err := http.Post(c.url, "application/json", bytes.NewReader(call)); err == nil {
		t.Error("the server still answers after Close")
	}
}

// tools/call names one tool, once: params with two names, or a name in
// another case, are refused, so the tool that runs is never read one way by
// the adapter and another by the server (#1587, security review).
func TestServer_RefusesAmbiguousToolsCallParams(t *testing.T) {
	att := &fakeAttention{poster: "acct-op"}
	root := gitRepo(t)
	s := NewToolset(Env{Root: root, RepoRoots: map[string]string{"acme/api": root}, Forge: newFakeForge(), Attention: att},
		operatorTurn("Mute dr_1."))
	t.Cleanup(s.Close)
	_, c := startServer(t, s)
	args := `"arguments":{"card_id":"dr_1","verb":"mute","request_quote":"Mute dr_1"}`
	for _, params := range []string{
		`{"name":"read_file","name":"act_on_attention_card",` + args + `}`,
		`{"Name":"act_on_attention_card","name":"read_file",` + args + `}`,
		`{"name":"act_on_attention_card",` + args + `,"arguments":{}}`,
		`{"name":"act_on_attention_card",` + args + `,"extra":1}`,
	} {
		_, body := c.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":`+params+`}`), nil)
		if !strings.Contains(string(body), "-32602") {
			t.Errorf("%s: %s, want invalid params", params, body)
		}
	}
	if got := att.made(); len(got) != 0 {
		t.Errorf("an ambiguous call acted: %+v", got)
	}
	_, body := c.post([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"act_on_attention_card",`+
		args+`,"_meta":{"progressToken":1}}}`), nil)
	if strings.Contains(string(body), "-32602") || len(att.made()) != 1 {
		t.Errorf("a well-formed call: %s, acts %+v", body, att.made())
	}
}
