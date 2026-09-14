package codexprovision

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
	"github.com/nightgauge/nightgauge/internal/gittest"
)

// openCodeRepo commits files as the default branch of a repository,
// publishes it as origin, and returns a clone of it: the worktree a stage
// runs in.
func openCodeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	wt, _ := openCodeRepoOn(t, "main", files)
	return wt
}

// openCodeRepoForge is openCodeRepo, and the forge serving origin.
func openCodeRepoForge(t *testing.T, files map[string]string) (string, *gitForge) {
	t.Helper()
	wt, origin := openCodeRepoOn(t, "main", files)
	return wt, forgeOf(origin)
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

// evilOrigin publishes a repository whose default branch main defines the
// server evil, as a stage could, and returns its path.
func evilOrigin(t *testing.T) string {
	t.Helper()
	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	writeFile(t, filepath.Join(seed, ".mcp.json"), `{"mcpServers": {"evil": {"command": "/bin/sh"}}}`)
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "a stage's own servers")
	evil := filepath.Join(t.TempDir(), "evil.git")
	gittest.Run(t, seed, "clone", "-q", "--bare", seed, evil)
	return evil
}

// moveOriginOn commits servers to origin's main from another clone, so the
// repository a stage runs in no longer holds origin's head.
func moveOriginOn(t *testing.T, origin, servers string) {
	t.Helper()
	parent := t.TempDir()
	gittest.Run(t, parent, "clone", "-q", origin, "other")
	other := filepath.Join(parent, "other")
	writeFile(t, filepath.Join(other, ".mcp.json"), servers)
	gittest.Run(t, other, "commit", "-qam", "origin moves on")
	gittest.Run(t, other, "push", "-q", "origin", "HEAD:main")
}

// provisionOpenCode runs ProvisionOpenCode on wt for a run that records no
// repository, so the stage is given its steering and no MCP server.
func provisionOpenCode(t *testing.T, wt string) OpenCodeProvision {
	t.Helper()
	p, err := provision(wt)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// provisionMcp runs ProvisionOpenCode on wt with its MCP servers read from
// forge for fixtureRepo.
func provisionMcp(t *testing.T, wt string, forge *gitForge) OpenCodeProvision {
	t.Helper()
	p, err := provisionFrom(wt, forge)
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
// default branch defines on the forge and no others. The fixture's default
// branch defines a; the stage's branch commits evil, moves the local main to
// that commit, and leaves evil2 in the working tree's .mcp.json and a changed
// a. The config gets a, as the forge serves it, and the warning names what
// was left out. Reading the working tree's .mcp.json, as the Codex path does,
// gives evil.
func TestOpenCodeMcpFromBaseBranch(t *testing.T) {
	base := `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["base"]}}}`
	wt, forge := openCodeRepoForge(t, map[string]string{".mcp.json": base})
	gittest.Run(t, wt, "checkout", "-qb", "feat/1626-stage")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["base"]}, "evil": {"command": "/bin/sh", "args": ["-c", "exit 0"]}}}`)
	gittest.Run(t, wt, "commit", "-qam", "a stage adds a server")
	gittest.Run(t, wt, "branch", "-f", "main", "HEAD")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true", "args": ["changed"]}, "evil": {"command": "/bin/sh"}, "evil2": {"url": "https://mcp.example.test/x"}}}`)

	p := provisionMcp(t, wt, forge)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
		t.Fatalf("MCP servers = %v, want [a], the default branch's only server", got)
	}
	if got := p.MCP["a"].Command; !slices.Equal(got, []string{"/usr/bin/true", "base"}) {
		t.Errorf("a's command = %v, want the default branch's /usr/bin/true base", got)
	}
	if !strings.HasPrefix(p.McpSource, fixtureRepo+"@main (") {
		t.Errorf("McpSource = %q, want %s@main and its commit", p.McpSource, fixtureRepo)
	}
	warnings := strings.Join(p.Warnings, "\n")
	for _, want := range []string{"evil, evil2", "not started", "differently", "a"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("the warnings do not say %q:\n%s", want, warnings)
		}
	}
	if got := forge.askedFor(); !slices.Equal(got, []string{fixtureRepo}) {
		t.Errorf("the forge was asked for %v, want the run's repository %s", got, fixtureRepo)
	}
}

// TestReadForgeMcpServersMergesBothSources: as ReadPipelineMcpServers does,
// .mcp.json wins over .claude/settings.json on a name. A file the commit has
// as a symbolic link is not followed, and one it does not have gives nothing
// and is not a failure.
func TestReadForgeMcpServersMergesBothSources(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	both := fixedForge{&forgetypes.DefaultBranchFiles{Branch: "main", Commit: commit, Files: map[string]forgetypes.RepoFile{
		".claude/settings.json": {Regular: true, Content: []byte(`{"mcpServers": {"shared": {"command": "from-settings"}, "settings-only": {"command": "s"}}}`)},
		".mcp.json":             {Regular: true, Content: []byte(`{"mcpServers": {"shared": {"command": "from-mcp-json"}}}`)},
	}}}
	servers, source, err := readForge(McpSource{Repo: fixtureRepo, Forge: both})
	if err != nil {
		t.Fatal(err)
	}
	if source != fixtureRepo+"@main (0123456)" || servers["shared"].Command != "from-mcp-json" || servers["settings-only"].Command != "s" || len(servers) != 2 {
		t.Errorf("servers from %s = %+v; want .mcp.json's shared and settings' settings-only", source, servers)
	}

	linked := fixedForge{&forgetypes.DefaultBranchFiles{Branch: "main", Commit: commit, Files: map[string]forgetypes.RepoFile{
		".mcp.json": {Regular: false},
	}}}
	servers, _, err = readForge(McpSource{Repo: fixtureRepo, Forge: linked})
	if err != nil || len(servers) != 0 {
		t.Errorf("a .mcp.json the commit has as a symbolic link gave %+v, %v; want nothing and no error", servers, err)
	}

	missing := fixedForge{&forgetypes.DefaultBranchFiles{Branch: "main", Commit: commit, Files: map[string]forgetypes.RepoFile{}}}
	servers, source, err = readForge(McpSource{Repo: fixtureRepo, Forge: missing})
	if err != nil || len(servers) != 0 || source == "" {
		t.Errorf("a commit with neither file gave %+v from %q, %v; want no server from the forge and no error", servers, source, err)
	}
}

// TestOpenCodeMcpWithoutARecordedRepository: the servers are read from the
// repository the pipeline records for the run, so a run that records none,
// or one that is not owner/name, or has no forge to read it from, gives the
// stage no MCP server, and a warning says why; it is not an error, because
// the stage runs without the servers. Nothing is read from the worktree's
// repository in its place.
func TestOpenCodeMcpWithoutARecordedRepository(t *testing.T) {
	wt, forge := openCodeRepoForge(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	for name, tc := range map[string]struct {
		src  McpSource
		want string
	}{
		"no repository":   {McpSource{Forge: forge}, "records no repository"},
		"a bare name":     {McpSource{Repo: "fixture-repo", Forge: forge}, "is not owner/name"},
		"a path":          {McpSource{Repo: "../fixture/repo", Forge: forge}, "is not owner/name"},
		"no forge to ask": {McpSource{Repo: fixtureRepo}, "no forge"},
	} {
		t.Run(name, func(t *testing.T) {
			p, err := provisionSource(wt, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if len(p.MCP) != 0 || p.McpSource != "" {
				t.Errorf("MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
			}
			if w := strings.Join(p.Warnings, "\n"); !strings.Contains(w, "none are started") || !strings.Contains(w, tc.want) {
				t.Errorf("the warnings do not say %q:\n%s", tc.want, w)
			}
		})
	}
	if got := forge.askedFor(); len(got) != 0 {
		t.Errorf("the forge was asked for %v", got)
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".mcp.json"), `{"mcpServers": {"evil": {"command": "/bin/sh"}}}`)
	p, err := provisionFrom(dir, forge)
	if err != nil {
		t.Fatal(err)
	}
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
		t.Errorf("a worktree that is no git working tree gets MCP servers %v, want the forge's [a]", got)
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
// stage in worktree A commits a server, then points origin/HEAD at a ref of
// its own, then moves origin/main itself, then replaces origin's head with a
// replacement object (refs/replace/). None of it is read: an OpenCode stage in
// worktree B reads its servers from the forge every time and never gets A's
// server.
func TestOpenCodeMcpBaseIsNotRepointedByAWorktree(t *testing.T) {
	wt, forge := openCodeRepoForge(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	b := filepath.Join(t.TempDir(), "b")
	gittest.Run(t, wt, "worktree", "add", "-q", "-b", "feat/b", b, "origin/main")
	gittest.Run(t, wt, "checkout", "-qb", "feat/a")
	writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "evil": {"command": "/bin/sh"}}}`)
	gittest.Run(t, wt, "commit", "-qam", "a stage adds a server")

	check := func(what string) {
		t.Helper()
		p := provisionMcp(t, b, forge)
		if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || !strings.HasPrefix(p.McpSource, fixtureRepo+"@main") {
			t.Errorf("with %s, worktree b gets MCP servers %v from %q, want [a] from %s@main", what, got, p.McpSource, fixtureRepo)
		}
	}
	for _, fake := range []string{"origin/zz-base", "origin/master"} {
		gittest.Run(t, wt, "update-ref", "refs/remotes/"+fake, "HEAD")
		gittest.Run(t, wt, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/"+fake)
		check("origin/HEAD pointed at " + fake)
	}
	gittest.Run(t, wt, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	tip := gittest.Run(t, wt, "rev-parse", "refs/remotes/origin/main")
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", "HEAD")
	check("origin/main moved to a stage's commit")
	gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", tip)
	gittest.Run(t, wt, "replace", tip, "HEAD")
	check("origin's head replaced by a stage's commit")
}

// TestOpenCodeMcpFromADefaultBranchOfAnotherName: the branch is the one the
// forge names as the repository's default, whatever it is called, so a
// repository whose default branch is develop gets develop's servers.
func TestOpenCodeMcpFromADefaultBranchOfAnotherName(t *testing.T) {
	wt, origin := openCodeRepoOn(t, "develop", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	p := provisionMcp(t, wt, forgeOf(origin))
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || !strings.HasPrefix(p.McpSource, fixtureRepo+"@develop (") {
		t.Errorf("MCP servers %v from %q, want [a] from %s@develop:\n%s", got, p.McpSource, fixtureRepo, strings.Join(p.Warnings, "\n"))
	}
}

// TestOpenCodeMcpNeverReadsALocalBranch: no fetch resets a local branch, so a
// stage that deletes origin/HEAD and points a local main at a commit of its
// own would otherwise choose the servers of every later stage, in a
// repository whose default branch is not main. The servers come from the
// forge's default branch still, and a repository without an origin gets the
// forge's servers, not its local main's.
func TestOpenCodeMcpNeverReadsALocalBranch(t *testing.T) {
	wt, origin := openCodeRepoOn(t, "develop", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	forge := forgeOf(origin)
	stage := filepath.Join(t.TempDir(), "stage")
	gittest.Run(t, wt, "worktree", "add", "-q", "-b", "feat/stage", stage, "origin/develop")
	writeFile(t, filepath.Join(stage, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "evil": {"command": "/bin/sh"}}}`)
	gittest.Run(t, stage, "commit", "-qam", "a stage adds a server")
	gittest.Run(t, stage, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	gittest.Run(t, stage, "update-ref", "refs/heads/main", "HEAD")

	p := provisionMcp(t, wt, forge)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) || !strings.HasPrefix(p.McpSource, fixtureRepo+"@develop") {
		t.Errorf("MCP servers %v from %q, want [a] from %s@develop, not the local main a stage pointed at its own commit", got, p.McpSource, fixtureRepo)
	}

	seed := gittest.InitRepo(t, t.TempDir(), "-b", "main")
	writeFile(t, filepath.Join(seed, ".mcp.json"), `{"mcpServers": {"local": {"command": "/usr/bin/true"}}}`)
	gittest.Run(t, seed, "add", "-A")
	gittest.Run(t, seed, "commit", "-qm", "a repository without an origin")
	p = provisionMcp(t, seed, forge)
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
		t.Errorf("a repository without an origin gave MCP servers %v, want the forge's [a]", got)
	}
}

// TestOpenCodeMcpReadsTheForgesHeadNotTheLastFetch inverts the round-2
// behaviour: when origin has moved on since the last fetch, the servers are
// what the forge serves at its head now, not origin/main as the repository
// last fetched it, and no warning says the repository is behind, because the
// repository is not read.
func TestOpenCodeMcpReadsTheForgesHeadNotTheLastFetch(t *testing.T) {
	wt, origin := openCodeRepoOn(t, "main", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	moveOriginOn(t, origin, `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "b": {"command": "/usr/bin/true"}}}`)
	head := gittest.Run(t, origin, "rev-parse", "main")

	p := provisionMcp(t, wt, forgeOf(origin))
	if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("MCP servers = %v, want [a b], the forge's head, with no fetch", got)
	}
	if want := fmt.Sprintf("%s@main (%s)", fixtureRepo, head[:7]); p.McpSource != want {
		t.Errorf("McpSource = %q, want %q", p.McpSource, want)
	}
	if w := strings.Join(p.Warnings, "\n"); strings.Contains(w, "behind") {
		t.Errorf("a warning says the repository is behind, so a ref of it was read:\n%s", w)
	}
}

// TestOpenCodeMcpIgnoresAStageMovedTrackingRef: when the repository does not
// hold origin's head, round 2 read refs/remotes/origin/main, which a stage
// can move with update-ref, and a stage could force that path by deleting the
// loose object of origin's head. Neither has any effect now: the servers are
// the forge's.
func TestOpenCodeMcpIgnoresAStageMovedTrackingRef(t *testing.T) {
	stageCommit := func(t *testing.T, wt string) string {
		t.Helper()
		gittest.Run(t, wt, "checkout", "-qb", "feat/stage")
		writeFile(t, filepath.Join(wt, ".mcp.json"), `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "evil": {"command": "/bin/sh"}}}`)
		gittest.Run(t, wt, "commit", "-qam", "a stage adds a server")
		return gittest.Run(t, wt, "rev-parse", "HEAD")
	}
	for name, setup := range map[string]func(t *testing.T, wt, origin string){
		"origin's head not fetched": func(t *testing.T, wt, origin string) {
			moveOriginOn(t, origin, `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "b": {"command": "/usr/bin/true"}}}`)
			gittest.Run(t, wt, "update-ref", "refs/remotes/origin/main", stageCommit(t, wt))
		},
		"origin's head deleted": func(t *testing.T, wt, origin string) {
			moveOriginOn(t, origin, `{"mcpServers": {"a": {"command": "/usr/bin/true"}, "b": {"command": "/usr/bin/true"}}}`)
			gittest.Run(t, wt, "fetch", "-q", "origin")
			head := gittest.Run(t, wt, "rev-parse", "refs/remotes/origin/main")
			loose := filepath.Join(wt, ".git", "objects", head[:2], head[2:])
			if err := os.Remove(loose); err != nil {
				t.Fatalf("the fetched head is not a loose object to delete: %v", err)
			}
			gittest.Run(t, wt, "update-ref", "--no-deref", "refs/remotes/origin/main", stageCommit(t, wt))
		},
	} {
		t.Run(name, func(t *testing.T) {
			wt, origin := openCodeRepoOn(t, "main", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
			setup(t, wt, origin)
			p := provisionMcp(t, wt, forgeOf(origin))
			if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a", "b"}) {
				t.Errorf("MCP servers = %v, want the forge's [a b], not the stage's evil", got)
			}
		})
	}
}

// TestOpenCodeMcpIgnoresTheRepositorysGitConfig: round 2 asked `git ls-remote
// origin`, which finds origin through the repository's git config and the
// worktree's .git file, both a stage's to write. A stage that points
// remote.origin.url at a repository of its own, or rewrites origin's URL with
// url.<x>.insteadOf, or points its worktree's .git file at another
// repository, has no effect now: the forge is asked for the repository the
// run records, and the servers are the forge's.
func TestOpenCodeMcpIgnoresTheRepositorysGitConfig(t *testing.T) {
	for name, attack := range map[string]func(t *testing.T, wt, origin, evil string){
		"remote.origin.url": func(t *testing.T, wt, _, evil string) {
			gittest.Run(t, wt, "fetch", "-q", evil, "main")
			gittest.Run(t, wt, "config", "remote.origin.url", evil)
		},
		"url.insteadOf": func(t *testing.T, wt, origin, evil string) {
			gittest.Run(t, wt, "fetch", "-q", evil, "main")
			gittest.Run(t, wt, "config", "url."+evil+".insteadOf", origin)
		},
		".git file": func(t *testing.T, wt, _, evil string) {
			parent := t.TempDir()
			gittest.Run(t, parent, "clone", "-q", evil, "evil")
			writeFile(t, filepath.Join(wt, ".git"), "gitdir: "+filepath.Join(parent, "evil", ".git")+"\n")
		},
	} {
		t.Run(name, func(t *testing.T) {
			repo, origin := openCodeRepoOn(t, "main", map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
			wt := filepath.Join(t.TempDir(), "stage")
			gittest.Run(t, repo, "worktree", "add", "-q", "-b", "feat/stage", wt, "origin/main")
			attack(t, wt, origin, evilOrigin(t))
			forge := forgeOf(origin)
			p := provisionMcp(t, wt, forge)
			if got := mcpNames(p.MCP); !slices.Equal(got, []string{"a"}) {
				t.Errorf("MCP servers = %v, want the forge's [a], not the stage's evil", got)
			}
			if got := forge.askedFor(); !slices.Equal(got, []string{fixtureRepo}) {
				t.Errorf("the forge was asked for %v, want the run's repository %s", got, fixtureRepo)
			}
		})
	}
}

// TestOpenCodeMcpForgeReadIsBounded: a forge that does not answer holds the
// stage up for openCodeForgeTimeout and no longer, even one that ignores its
// context, and the stage then gets no MCP server and one warning; nothing is
// read from the repository in its place, although it holds origin's head.
func TestOpenCodeMcpForgeReadIsBounded(t *testing.T) {
	prev := openCodeForgeTimeout
	openCodeForgeTimeout = 300 * time.Millisecond
	t.Cleanup(func() { openCodeForgeTimeout = prev })
	wt := openCodeRepo(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	stalled := stalledForge{release: make(chan struct{})}
	t.Cleanup(func() { close(stalled.release) })

	start := time.Now()
	p, err := provisionFrom(wt, stalled)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the stalled forge read took %s, want about 300ms", elapsed)
	}
	if len(p.MCP) != 0 || p.McpSource != "" {
		t.Errorf("with the forge stalled, MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "none are started") || !strings.Contains(w, "did not answer") {
		t.Errorf("no warning says the forge did not answer:\n%s", w)
	}
	if n := strings.Count(w, "MCP servers"); n != 1 {
		t.Errorf("%d warnings about the MCP servers, want one:\n%s", n, w)
	}
}

// TestOpenCodeMcpWhenTheForgeFails: a forge read that fails, as offline or
// refused, gives the stage no MCP server and one warning, and nothing falls
// back to a ref of the repository, although it holds origin's head.
func TestOpenCodeMcpWhenTheForgeFails(t *testing.T) {
	wt := openCodeRepo(t, map[string]string{".mcp.json": `{"mcpServers": {"a": {"command": "/usr/bin/true"}}}`})
	p, err := provisionFrom(wt, failingForge{errors.New("dial tcp: lookup api.example.test: no such host")})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.MCP) != 0 || p.McpSource != "" {
		t.Errorf("with the forge failing, MCP servers %v from %q", mcpNames(p.MCP), p.McpSource)
	}
	w := strings.Join(p.Warnings, "\n")
	if !strings.Contains(w, "none are started") || !strings.Contains(w, "the forge could not read "+fixtureRepo) {
		t.Errorf("no warning says the forge could not be read:\n%s", w)
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
	wt, forge := openCodeRepoForge(t, map[string]string{".mcp.json": `{"mcpServers": {
  "quote":     {"command": "srv", "env": {"P": "${FIXTURE_QUOTE}"}},
  "backslash": {"command": "srv", "args": ["--home=${FIXTURE_BACKSLASH}", "${FIXTURE_PLAIN}"]},
  "newline":   {"url": "https://mcp.example.test/mcp", "headers": {"X-Key": "${FIXTURE_NEWLINE}"}},
  "file":      {"command": "srv", "env": {"K": "${FIXTURE_FILE}"}},
  "bearer":    {"url": "https://mcp.example.test/mcp", "headers": {"Authorization": "Bearer $FIXTURE_BEARER"}},
  "plain":     {"command": "srv", "env": {"T": "${FIXTURE_PLAIN}", "U": "${FIXTURE_UNSET_1626}"}},
  "envref":    {"command": "srv", "args": ["${FIXTURE_ENVREF}"]}
}}`})

	p := provisionMcp(t, wt, forge)
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
