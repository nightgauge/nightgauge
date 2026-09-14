package codexprovision

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nightgauge/nightgauge/internal/gittest"
)

// openCodeRepo commits files as the base branch of a repository, publishes it
// as origin, and returns a clone of it: the worktree a stage runs in, whose
// origin/HEAD names origin/main, as a clone's does.
func openCodeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	wt, _ := openCodeRepoOn(t, "main", files)
	return wt
}

// openCodeRepoOn commits files on branch, publishes them as origin, whose
// HEAD names branch, and returns a clone of it and origin's path.
func openCodeRepoOn(t *testing.T, branch string, files map[string]string) (wt, origin string) {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", branch)
	for path, content := range files {
		writeFile(t, filepath.Join(seed, path), content)
	}
	if len(files) == 0 {
		writeFile(t, filepath.Join(seed, "README"), "fixture\n")
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "base")
	origin = filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "wt")
	return filepath.Join(parent, "wt"), origin
}

// provision runs ProvisionOpenCode on wt in the test's environment.
func provision(wt string) (OpenCodeProvision, error) {
	return ProvisionOpenCode(context.Background(), wt, os.LookupEnv)
}

func provisionOpenCode(t *testing.T, wt string) OpenCodeProvision {
	t.Helper()
	p, err := provision(wt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func mcpNames(m map[string]OpenCodeMcpServer) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// TestOpenCodeMcpFromBaseBranch: an OpenCode stage gets the MCP servers the
// base branch defines and no others. The fixture's base branch defines a;
// the stage's branch commits evil, moves the local main to that commit, and
// leaves evil2 in the working tree's .mcp.json and a changed a. The config
// gets a, as origin/main defines it, and the warning names what was left out.
// Reading the working tree's .mcp.json, as the Codex path does, gives evil.
func TestOpenCodeMcpFromBaseBranch(t *testing.T) {
	base := `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["base"]}}}`
	wt := openCodeRepo(t, map[string]string{".mcp.json": base})
	gittest.Run(t, wt, "checkout", "-qb", "feat/1626-stage")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["base"]}, "evil": {"command": "/bin/sh", "args": ["-c", "exit 0"]}}}`)
	gittest.Run(t, wt, "commit", "-qam", "a stage adds a server")
	gittest.Run(t, wt, "branch", "-f", "main", "HEAD")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["changed"]}, "evil": {"command": "/bin/sh"}, "evil2": {"url": "https://mcp.example.test/x"}}}`)

	p, err := ProvisionOpenCode(context.Background(), wt, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("MCP servers = %v, want [a], the base branch's only server", got)
	}
	if got := p.MCP["a"].Command; !slices.Equal(got, []string{"/usr/bin/true", "base"}) {
		t.Errorf("a's command = %v, want the base branch's /usr/bin/true base", got)
	}
	if p.McpSource != "origin/main" {
		t.Errorf("McpSource = %q, want origin/main", p.McpSource)
	}
	warnings := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"evil, evil2", "not started", "differently", "a"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the warnings do not say %q:\n%s", want, warnings)
		}
	}
}

// TestReadBaseBranchMcpServersMergesBothSources: as ReadPipelineMcpServers
// does, .mcp.json wins over .claude/settings.json on a name. A source the
// base branch tracks as a symbolic link is not followed.
func TestReadBaseBranchMcpServersMergesBothSources(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{
		".claude/settings.json": `{"mcpServers": {"shared": {"command": "from-settings"}, "settings-only": {"command": "s"}}}`,
		".mcp.json":             `{"mcpServers": {"shared": {"command": "from-mcp-json"}}}`,
	})
	servers, source, warnings, err := ReadBaseBranchMcpServers(context.Background(), wt)
	if err != nil {
		t.Fatal(err)
	}
	if source != "origin/main" || servers["shared"].Command != "from-mcp-json" || servers["settings-only"].Command != "s" || len(servers) != 2 {
		t.Errorf("servers from %s = %+v; want .mcp.json's shared and settings' settings-only", source, servers)
	}
	if len(warnings) != 0 {
		t.Errorf("a clone read at origin's tip gave warnings: %v", warnings)
	}

	outside := filepath.Join(t.TempDir(), "outside.json")
	writeFile(t, outside, `{"mcpServers": {"linked": {"command": "x"}}}`)
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	if err := os.Symlink(outside, filepath.Join(seed, ".mcp.json")); err != nil {
		t.Fatal(err)
	}
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "a linked .mcp.json")
	origin := filepath.Join(t.TempDir(), "origin.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, origin)
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "linked")
	servers, _, _, err = ReadBaseBranchMcpServers(context.Background(), filepath.Join(parent, "linked"))
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 0 {
		t.Errorf("a .mcp.json the branch tracks as a symbolic link was followed: %+v", servers)
	}
}

// TestOpenCodeMcpWithoutABaseBranch: a worktree git cannot read a base branch
// from gives the stage no MCP server, and a warning says why; it is not an
// error, because the stage runs without the servers.
func TestOpenCodeMcpWithoutABaseBranch(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"evil": {"command": "/bin/sh"}}}`)
	p, err := ProvisionOpenCode(context.Background(), dir, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MCP) != 0 || p.McpSource != "" {
		t.Errorf("a worktree with no base branch gave MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "none are started") || !strings.Contains(w, "not a git working tree") {
		t.Errorf("the warnings do not say why no server is started:\n%s", w)
	}
}

// TestOpenCodeMcpCredentialsAreEnvReferences: a server's credential reaches
// OpenCode only as {env:VAR}. Authorization: Bearer ${TOKEN} becomes
// "Bearer {env:TOKEN}", as does the bare $TOKEN form the Codex path accepts,
// and every other ${VAR} becomes {env:VAR}. The variable's value, which the
// environment holds, appears nowhere in the servers.
func TestOpenCodeMcpCredentialsAreEnvReferences(t *testing.T) {
	const value = "fake-mcp-credential-1626"
	t.Setenv("TOKEN", value)
	t.Setenv("OTHER", value)
	servers, warnings := OpenCodeMcpServers(map[string]PipelineMcpServer{
		"braced": {URL: "https://mcp.example.test/mcp", Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"}},
		"bare":   {Type: "sse", URL: "https://mcp.example.test/sse?team=${TEAM}", Headers: map[string]string{"authorization": "Bearer $TOKEN", "X-Key": "${OTHER}"}},
		"local":  {Command: "${HOME}/bin/srv", Args: []string{"--token=${TOKEN}"}, Env: map[string]string{"API_KEY": "${OTHER}", "LEVEL": "debug"}},
	})
	if len(warnings) > 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	raw, err := json.Marshal(servers)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), value) {
		t.Fatalf("a variable's value is in the servers:\n%s", raw)
	}
	for _, c := range []struct{ got, want string }{
		{servers["braced"].Headers["Authorization"], "Bearer {env:TOKEN}"},
		{servers["bare"].Headers["authorization"], "Bearer {env:TOKEN}"},
		{servers["bare"].Headers["X-Key"], "{env:OTHER}"},
		{servers["bare"].URL, "https://mcp.example.test/sse?team={env:TEAM}"},
		{servers["local"].Environment["API_KEY"], "{env:OTHER}"},
		{servers["local"].Environment["LEVEL"], "debug"},
		{strings.Join(servers["local"].Command, " "), "{env:HOME}/bin/srv --token={env:TOKEN}"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

// TestOpenCodeMcpShapes: a server with a command is OpenCode's local shape, a
// server with a URL and an http or sse type (or no command) is its remote
// shape with OAuth off, and each is enabled explicitly. A server with
// neither is left out with a warning, and a ${VAR:-default} keeps its
// variable and loses its default, which a warning says.
func TestOpenCodeMcpShapes(t *testing.T) {
	servers, warnings := OpenCodeMcpServers(map[string]PipelineMcpServer{
		"stdio":   {Type: "stdio", Command: "npx", Args: []string{"-y", "srv"}, Cwd: "tools", URL: "https://ignored.example.test"},
		"http":    {Type: "http", URL: "https://mcp.example.test/mcp"},
		"urlonly": {URL: "https://mcp.example.test/mcp"},
		"empty":   {Type: "stdio"},
		"default": {Command: "srv", Env: map[string]string{"LEVEL": "${LEVEL:-info}", "EMPTY": "${EMPTY:-}"}},
	})
	raw, _ := json.Marshal(servers)
	var doc map[string]map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if s := doc["stdio"]; s["type"] != "local" || s["cwd"] != "tools" || s["enabled"] != true || s["url"] != nil || s["oauth"] != nil {
		t.Errorf("stdio = %v; want a local server with its cwd, enabled, no url and no oauth", s)
	}
	for _, name := range []string{"http", "urlonly"} {
		if s := doc[name]; s["type"] != "remote" || s["oauth"] != false || s["enabled"] != true || s["command"] != nil {
			t.Errorf("%s = %v; want a remote server with oauth false, enabled", name, s)
		}
	}
	if _, ok := doc["empty"]; ok {
		t.Error("a server with neither a command nor a url was kept")
	}
	if got := servers["default"].Environment; got["LEVEL"] != "{env:LEVEL}" || got["EMPTY"] != "{env:EMPTY}" {
		t.Errorf("default's environment = %v", got)
	}
	w := strings.Join(warnings, "\n")
	if !strings.Contains(w, `"empty"`) || !strings.Contains(w, "neither a command nor a url") {
		t.Errorf("no warning names the server left out:\n%s", w)
	}
	if !strings.Contains(w, `"default"`) || !strings.Contains(w, "LEVEL") || strings.Contains(w, "EMPTY") {
		t.Errorf("the dropped-default warning should name LEVEL only (EMPTY's default is empty):\n%s", w)
	}
}

// TestOpenCodeMcpRefusesOpenCodeSyntax: OpenCode substitutes {env:...} and
// {file:...} anywhere in its config, so a server whose name or value already
// holds that syntax is left out, with a warning naming the server and the
// field and never the value. Claude would pass such a value through as text.
func TestOpenCodeMcpRefusesOpenCodeSyntax(t *testing.T) {
	servers, warnings := OpenCodeMcpServers(map[string]PipelineMcpServer{
		"file-header": {URL: "https://mcp.example.test", Headers: map[string]string{"X-Key": "{file:~/.ssh/id_rsa}"}},
		"env-arg":     {Command: "srv", Args: []string{"{env:ANTHROPIC_API_KEY}"}},
		"claude-env":  {Command: "srv", Env: map[string]string{"K": "${env:ANTHROPIC_API_KEY}"}},
		"url":         {URL: "https://mcp.example.test/?k={env:OPENAI_API_KEY}"},
		"{file:/x}":   {Command: "srv"},
		"ok":          {Command: "srv", Args: []string{`--json={"a":1}`}},
	})
	if got := mcpNames(servers); !slices.Equal(got, []string{"ok"}) {
		t.Errorf("servers = %v, want only ok", got)
	}
	w := strings.Join(warnings, "\n")
	for _, want := range []string{
		`"file-header" is not started: its headers`, `"env-arg" is not started: its args`,
		`"claude-env" is not started: its env`, `"url" is not started: its url`, `"{file:/x}" is not started: its name`,
	} {
		if !strings.Contains(w, want) {
			t.Errorf("the warnings do not name %s:\n%s", want, w)
		}
	}
	for _, secret := range []string{"id_rsa", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if strings.Contains(w, secret) {
			t.Errorf("a warning quotes the refused value %q:\n%s", secret, w)
		}
	}
}

// TestOpenCodeMcpBaseIsNotRepointedByAWorktree: remote-tracking refs are
// shared by every worktree of a repository, and a stage can write them. A
// stage in worktree A commits a server, then points origin/HEAD, which no
// fetch resets, at a ref of its own: origin/zz-base, or origin/master beside
// origin/main, a branch origin does not have, so no fetch resets that ref
// either. Last it moves origin/main itself. The base is the branch origin
// names as its HEAD, read at the tip origin reports, so an OpenCode stage in
// worktree B reads its servers from origin/main every time and never gets
// A's server, and a warning names an origin/HEAD that names another branch.
func TestOpenCodeMcpBaseIsNotRepointedByAWorktree(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	b := filepath.Join(t.TempDir(), "b")
	gittest.Run(t, wt, "worktree", "add", "-q", "-b", "feat/b", b, "origin/main")
	gittest.Run(t, wt, "checkout", "-qb", "feat/a")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "evil": {"command": "/bin/sh"}}}`)
	gittest.Run(t, wt, "commit", "-qam", "a stage adds a server")

	for _, fake := range []string{"origin/zz-base", "origin/master"} {
		gittest.Run(t, wt, "update-ref", "refs/remotes/"+fake, "HEAD")
		gittest.Run(t, wt, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/"+fake)
		p := provisionOpenCode(t, b)
		if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || p.McpSource != "origin/main" {
			t.Errorf("with origin/HEAD pointed at %s, worktree b gets MCP servers %v from %q, want [a] from origin/main", fake, got, p.McpSource)
		}
		if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "origin/HEAD names "+fake) {
			t.Errorf("no warning says origin/HEAD names %s:\n%s", fake, w)
		}
	}

	gittest.Run(t, wt, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	tip := gittest.Run(t, wt, "rev-parse", "refs/remotes/origin/main")
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")
	p := provisionOpenCode(t, b)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || p.McpSource != "origin/main" {
		t.Errorf("with origin/main moved to a stage's commit, worktree b gets MCP servers %v from %q, want [a], origin's own tip", got, p.McpSource)
	}

	// A replacement object (refs/replace/), which git reads in place of the
	// object it replaces and no fetch resets, does not stand in for origin's
	// tip either.
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", tip)
	gittest.Run(t, wt, "replace", tip, "HEAD")
	p = provisionOpenCode(t, b)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
		t.Errorf("with origin's tip replaced by a stage's commit, worktree b gets MCP servers %v, want [a]", got)
	}
}

// TestOpenCodeMcpFromADefaultBranchOfAnotherName: the base branch is the one
// origin names as its HEAD, whatever it is called, so a clone of a repository
// whose default branch is develop gets develop's servers.
func TestOpenCodeMcpFromADefaultBranchOfAnotherName(t *testing.T) {
	wt, _ := openCodeRepoOn(t, "develop", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	p := provisionOpenCode(t, wt)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || p.McpSource != "origin/develop" {
		t.Errorf("MCP servers %v from %q, want [a] from origin/develop:\n%s", got, p.McpSource, strings.Join(p.Warnings, "\n"))
	}
}

// TestOpenCodeMcpNeverReadsALocalBranch: no fetch resets a local branch, so a
// stage that deletes origin/HEAD and points a local main at a commit of its
// own would otherwise choose the servers of every later stage, in a
// repository whose default branch is not main. The servers come from origin's
// default branch still. A repository without an origin gets none, whatever
// its local main defines.
func TestOpenCodeMcpNeverReadsALocalBranch(t *testing.T) {
	wt, _ := openCodeRepoOn(t, "develop", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	stage := filepath.Join(t.TempDir(), "stage")
	gittest.Run(t, wt, "worktree", "add", "-q", "-b", "feat/stage", stage, "origin/develop")
	writeFile(t, filepath.Join(stage, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "evil": {"command": "/bin/sh"}}}`)
	gittest.Run(t, stage, "commit", "-qam", "a stage adds a server")
	gittest.Run(t, stage, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	gittest.Run(t, stage, "update-ref", "refs/heads/main", "HEAD")

	p := provisionOpenCode(t, wt)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || p.McpSource != "origin/develop" {
		t.Errorf("MCP servers %v from %q, want [a] from origin/develop, not the local main a stage pointed at its own commit", got, p.McpSource)
	}

	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	writeFile(t, filepath.Join(seed, ".mcp.json"), `{"mcpServers": {"local": {"command": "/usr/bin/true"}}}`)
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "a repository without an origin")
	p = provisionOpenCode(t, seed)
	if len(p.MCP) != 0 || p.McpSource != "" {
		t.Errorf("a repository without an origin gave MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "none are started") {
		t.Errorf("no warning says why no server is started:\n%s", w)
	}
}

// TestOpenCodeMcpBehindOrigin: when origin's tip is not in the repository,
// because origin has moved on since the last fetch, the servers are read from
// origin/main as the repository last fetched it, and a warning says so. A
// fetch brings them up to date.
func TestOpenCodeMcpBehindOrigin(t *testing.T) {
	wt, origin := openCodeRepoOn(t, "main", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "other")
	other := filepath.Join(parent, "other")
	writeFile(t, filepath.Join(other, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "b": {"command": "/usr/bin/true"}}}`)
	gittest.Run(t, other, "commit", "-qam", "origin moves on")
	gittest.Run(t, other, "push", "-q", "origin", "HEAD:main")

	p := provisionOpenCode(t, wt)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || p.McpSource != "origin/main" {
		t.Errorf("MCP servers %v from %q, want [a] from origin/main as last fetched", got, p.McpSource)
	}
	if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "origin/main is behind origin") {
		t.Errorf("no warning says origin/main is behind origin:\n%s", w)
	}

	gittest.Run(t, wt, "fetch", "-q", "origin")
	p = provisionOpenCode(t, wt)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("after a fetch, MCP servers = %v, want [a b]", got)
	}
	if w := strings.Join(p.Warnings, "\n"); strings.Contains(w, "behind") {
		t.Errorf("after a fetch, a warning still says origin/main is behind:\n%s", w)
	}
}

// TestOpenCodeMcpWhenOriginCannotBeAsked: the base branch is the one origin
// names, so a stage whose origin cannot be reached gets no MCP server, and the
// warning says why without quoting origin's URL, which can carry a
// credential.
func TestOpenCodeMcpWhenOriginCannotBeAsked(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	gittest.Run(t, wt, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone-origin-fixture.git"))
	p := provisionOpenCode(t, wt)
	if len(p.MCP) != 0 || p.McpSource != "" {
		t.Errorf("with origin unreachable, MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "none are started") || !strings.Contains(w, "origin could not be asked") {
		t.Errorf("no warning says origin could not be asked:\n%s", w)
	}
	if strings.Contains(w, "gone-origin-fixture") {
		t.Errorf("a warning quotes origin's URL:\n%s", w)
	}
}

// TestOpenCodeMcpLeavesOutValuesOpenCodeCannotPaste: OpenCode pastes a
// {env:VAR}'s value into its config text unescaped, then reads every
// {file:...} the text holds (read from its 1.18.30 bundled source, and
// observed). A value holding a quote, a backslash or a control character
// makes the whole config fail to parse, and OpenCode's error prints the
// config with every resolved credential in it; a {file:...} in a value reads
// that file into the config. So a server one of whose variables holds any of
// these is left out, and the warning names the server and the variable, never
// the value. An unset variable, a plain value, and a {env:...} in a value,
// which OpenCode does not substitute again, keep their server.
func TestOpenCodeMcpLeavesOutValuesOpenCodeCannotPaste(t *testing.T) {
	values := map[string]string{
		"FIXTURE_QUOTE":     `pass"word-fixture`,
		"FIXTURE_BACKSLASH": `C:\Users\fixture`,
		"FIXTURE_NEWLINE":   "line-one-fixture\nline-two",
		"FIXTURE_FILE":      "{file:/nonexistent/fixture}",
		"FIXTURE_BEARER":    `bearer"fixture`,
		"FIXTURE_PLAIN":     "plain-fixture-value",
		"FIXTURE_ENVREF":    "{env:FIXTURE_PLAIN}",
	}
	for k, v := range values {
		t.Setenv(k, v)
	}
	wt := openCodeRepo(t, map[string]string{".mcp.json": `{"mcpServers": {
  "quote":     {"command": "srv", "env": {"P": "${FIXTURE_QUOTE}"}},
  "backslash": {"command": "srv", "args": ["--home=${FIXTURE_BACKSLASH}", "${FIXTURE_PLAIN}"]},
  "newline":   {"url": "https://mcp.example.test/mcp", "headers": {"X-Key": "${FIXTURE_NEWLINE}"}},
  "file":      {"command": "srv", "env": {"K": "${FIXTURE_FILE}"}},
  "bearer":    {"url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer $FIXTURE_BEARER"}},
  "plain":     {"command": "srv", "env": {"T": "${FIXTURE_PLAIN}", "U": "${FIXTURE_UNSET_1626}"}},
  "envref":    {"command": "srv", "args": ["${FIXTURE_ENVREF}"]}
}}`})

	p := provisionOpenCode(t, wt)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"envref", "plain"}) {
		t.Errorf("MCP servers = %v, want envref and plain", got)
	}
	w := strings.Join(p.Warnings, "\n")
	for server, variable := range map[string]string{
		"quote": "FIXTURE_QUOTE", "backslash": "FIXTURE_BACKSLASH", "newline": "FIXTURE_NEWLINE",
		"file": "FIXTURE_FILE", "bearer": "FIXTURE_BEARER",
	} {
		if want := fmt.Sprintf("MCP server %q is not started: the value of %s holds", server, variable); !strings.Contains(w, want) {
			t.Errorf("no warning says %s:\n%s", want, w)
		}
	}
	for name, v := range values {
		if strings.Contains(w, v) {
			t.Errorf("a warning quotes the value of %s:\n%s", name, w)
		}
	}
}
