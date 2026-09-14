package codexprovision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/forge"
	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// OpenCode MCP servers (ADR-022 § 15, #1626).
//
// An OpenCode stage gets the pipeline's MCP servers, the ones a Claude stage
// gets from .mcp.json and .claude/settings.json, and no others, in the
// per-run config (adapters.BuildOpenCodeConfig). Two things differ from the
// Codex path, which reads the working tree and writes the servers verbatim:
//
//   - The servers come from the forge, not the repository on this machine
//     (ReadForgeMcpServers). A server is a command OpenCode runs or a URL it
//     sends the stage's tool calls to, and a stage can write its own
//     worktree, so a server one stage added to .mcp.json would otherwise run
//     in the next stage without review. A stage can write the rest of the
//     repository too: the refs and objects every worktree shares, the git
//     config that says where origin is and how git reaches it, and its
//     worktree's .git file. So none of it is read. The repository is the one
//     the pipeline records for the run (McpSource.Repo), and one forge
//     answer gives its default branch, the commit at the branch's head and
//     .claude/settings.json and .mcp.json at that commit. The read is bounded
//     (openCodeForgeTimeout); when it fails, times out or finds no
//     repository, the stage gets no server and one warning, and nothing
//     falls back to a local ref. A file the commit does not have gives no
//     server and is not a failure.
//   - Variable references are translated, never written as values. Claude
//     expands ${VAR} and ${VAR:-default} in a server's command, args, env, url
//     and headers, value by value; OpenCode 1.18.30 replaces each {env:VAR} in
//     its config text with the variable's value before it parses the text,
//     and escapes nothing, then reads each {file:...} the result holds (read
//     from its bundled source, and observed). Each ${VAR} becomes {env:VAR},
//     so no variable's value is written into the config. A value that already
//     holds OpenCode's own {env:...} or {file:...} syntax refuses its server:
//     Claude passes such a value through as text, while OpenCode would read
//     the variable, or the file, it names. And a server one of whose variables
//     holds a value OpenCode cannot paste (OpenCodeUnpastable) is left out
//     (openCodePastableMcpServers): that value would make the whole config
//     fail to parse, and OpenCode's error prints the config it substituted,
//     every other server's credentials in it. The values are read in the
//     environment OpenCode is spawned with: the run's isolation variables
//     laid over the inherited environment, less what the spawn withholds
//     (adapters.PrepareOpenCodeRun). BuildOpenCodeConfig checks every
//     {env:...} of the finished content the same way, the anthropic key's
//     included, and refuses the dispatch when one names such a value.

// OpenCodeMcpServer is one MCP server in opencode 1.18.30's config shape:
// McpLocalConfig (type "local": command, cwd, environment) or McpRemoteConfig
// (type "remote": url, headers, oauth), as its bundled config schema defines
// them. Enabled is always set, so a lower config layer's `enabled: false` for
// the same name loses to it.
type OpenCodeMcpServer struct {
	Type        string            `json:"type"`
	Command     []string          `json:"command,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	URL         string            `json:"url,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	// OAuth is false for every remote server: OpenCode's OAuth flow runs a
	// login in a browser and a callback server on this machine, which a
	// headless stage can neither complete nor should open (ADR-022 § 18), and
	// `oauth: false` turns off its auto-detection. It is nil for a local
	// server, whose schema has no such key.
	OAuth   *bool `json:"oauth,omitempty"`
	Enabled bool  `json:"enabled"`
}

// claudeVarRefRE is a Claude .mcp.json variable reference: ${VAR} or
// ${VAR:-default}.
var claudeVarRefRE = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-[^}]*)?\}`)

// openCodeSubstitutionRE is the text OpenCode 1.18.30 substitutes in its
// config before parsing it (read from its bundled source: /\{env:([^}]+)\}/g
// and /\{file:[^}]+\}/g, both case-sensitive).
var openCodeSubstitutionRE = regexp.MustCompile(`\{(env|file):`)

// OpenCodeMcpServers translates the pipeline's servers to OpenCode's shape.
// A server that carries neither a command nor a URL, or holds OpenCode's own
// substitution syntax anywhere, is left out; warnings say which and why,
// naming the server and never a value.
func OpenCodeMcpServers(servers map[string]PipelineMcpServer) (map[string]OpenCodeMcpServer, []string) {
	out := map[string]OpenCodeMcpServer{}
	var warnings []string
	for _, name := range sortedServerNames(servers) {
		s := servers[name]
		if field := openCodeSyntaxIn(name, s); field != "" {
			warnings = append(warnings, fmt.Sprintf(
				"MCP server %q is not started: its %s holds OpenCode's {env:...} or {file:...} syntax, which OpenCode would substitute; write a variable as ${VAR}", name, field))
			continue
		}
		srv, dropped, ok := toOpenCodeMcpServer(s)
		if !ok {
			warnings = append(warnings, fmt.Sprintf("MCP server %q is not started: it names neither a command nor a url", name))
			continue
		}
		if len(dropped) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"MCP server %q: OpenCode's {env:...} has no default, so the default of %s is dropped and an unset variable reads as empty", name, strings.Join(dropped, ", ")))
		}
		out[name] = srv
	}
	return out, warnings
}

// openCodeSyntaxIn names the first field of s, or its name, that holds
// OpenCode's substitution syntax, or "" when none does.
func openCodeSyntaxIn(name string, s PipelineMcpServer) string {
	check := func(v string) bool { return openCodeSubstitutionRE.MatchString(v) }
	switch {
	case check(name):
		return "name"
	case check(s.Command):
		return "command"
	case check(s.Cwd):
		return "cwd"
	case check(s.URL):
		return "url"
	}
	for _, a := range s.Args {
		if check(a) {
			return "args"
		}
	}
	for k, v := range s.Env {
		if check(k) || check(v) {
			return "env"
		}
	}
	for k, v := range s.Headers {
		if check(k) || check(v) {
			return "headers"
		}
	}
	return ""
}

// toOpenCodeMcpServer translates one server, choosing local or remote the way
// toCodexMcpServer does. It returns the variables whose ${VAR:-default}
// default was dropped, and false when s names neither a command nor a URL.
func toOpenCodeMcpServer(s PipelineMcpServer) (OpenCodeMcpServer, []string, bool) {
	var dropped []string
	ref := func(v string) string {
		out, d := openCodeVarRefs(v)
		dropped = append(dropped, d...)
		return out
	}
	isHTTP := s.URL != "" &&
		(s.Type == "" || s.Type == "http" || s.Type == "sse" || s.Command == "")
	switch {
	case isHTTP:
		off := false
		out := OpenCodeMcpServer{Type: "remote", URL: ref(s.URL), OAuth: &off, Enabled: true}
		if len(s.Headers) > 0 {
			out.Headers = map[string]string{}
			for k, v := range s.Headers {
				if strings.EqualFold(k, "authorization") {
					if env := bearerEnvVar(v); env != "" {
						out.Headers[k] = "Bearer {env:" + env + "}"
						continue
					}
				}
				out.Headers[k] = ref(v)
			}
		}
		return out, uniqueSorted(dropped), true
	case s.Command != "":
		out := OpenCodeMcpServer{Type: "local", Command: []string{ref(s.Command)}, Cwd: ref(s.Cwd), Enabled: true}
		for _, a := range s.Args {
			out.Command = append(out.Command, ref(a))
		}
		if len(s.Env) > 0 {
			out.Environment = map[string]string{}
			for k, v := range s.Env {
				out.Environment[k] = ref(v)
			}
		}
		return out, uniqueSorted(dropped), true
	}
	return OpenCodeMcpServer{}, nil, false
}

// openCodeEnvRefRE is a {env:VAR} reference as openCodeVarRefs writes it.
var openCodeEnvRefRE = regexp.MustCompile(`\{env:([A-Za-z_][A-Za-z0-9_]*)\}`)

// OpenCodeUnpastable reports whether OpenCode 1.18.30 cannot paste v, a
// variable's value, into its config text in place of a {env:VAR}: a quote or
// a backslash would end or escape the JSON string it lands in, a control
// character cannot stand in one, and each makes the whole config fail to
// parse; a {file:...} in it would be read as a file reference by the pass
// that follows. A {env:...} in it is not substituted again, so it is text.
func OpenCodeUnpastable(v string) bool {
	return strings.ContainsAny(v, `"\`) || strings.Contains(v, "{file:") ||
		strings.IndexFunc(v, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0
}

// openCodePastableMcpServers leaves out each server with a {env:VAR} whose
// variable, as lookup reads the environment OpenCode is spawned with, holds a value
// OpenCode cannot paste into its config text (OpenCodeUnpastable). An unset
// variable reads as empty in OpenCode and keeps its server. The warnings name
// the server and the variables, never a value.
func openCodePastableMcpServers(servers map[string]OpenCodeMcpServer, lookup func(string) (string, bool)) (map[string]OpenCodeMcpServer, []string) {
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := map[string]OpenCodeMcpServer{}
	var warnings []string
	for _, name := range names {
		s := servers[name]
		values := append([]string{s.Cwd, s.URL}, s.Command...)
		for k, v := range s.Environment {
			values = append(values, k, v)
		}
		for k, v := range s.Headers {
			values = append(values, k, v)
		}
		var unpastable []string
		for _, v := range values {
			for _, m := range openCodeEnvRefRE.FindAllStringSubmatch(v, -1) {
				if value, ok := lookup(m[1]); ok && OpenCodeUnpastable(value) {
					unpastable = append(unpastable, m[1])
				}
			}
		}
		if unpastable = uniqueSorted(unpastable); len(unpastable) > 0 {
			warnings = append(warnings, fmt.Sprintf(
				"MCP server %q is not started: the value of %s holds a quote, a backslash, a control character or {file:, which OpenCode would paste into its config text unescaped, so the config would not parse", name, strings.Join(unpastable, ", ")))
			continue
		}
		out[name] = s
	}
	return out, warnings
}

// openCodeVarRefs rewrites every Claude variable reference in v as OpenCode's
// {env:VAR}, and returns the names whose default it dropped.
func openCodeVarRefs(v string) (string, []string) {
	var dropped []string
	out := claudeVarRefRE.ReplaceAllStringFunc(v, func(m string) string {
		sub := claudeVarRefRE.FindStringSubmatch(m)
		if sub[2] != "" && sub[2] != ":-" {
			dropped = append(dropped, sub[1])
		}
		return "{env:" + sub[1] + "}"
	})
	return out, dropped
}

func uniqueSorted(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	out := names[:1]
	for _, n := range names[1:] {
		if n != out[len(out)-1] {
			out = append(out, n)
		}
	}
	return out
}

// openCodeForgeTimeout bounds the forge read of the MCP servers, the
// identity's token included, so a forge that does not answer holds a stage up
// no longer. A variable so a test can shorten it.
var openCodeForgeTimeout = 15 * time.Second

// McpSource is where an OpenCode stage's MCP servers are read: the
// repository the pipeline records for the run, at the head of its default
// branch, as its forge serves it.
type McpSource struct {
	// Repo is the repository as owner/name, as the pipeline records it for
	// the run (the dispatch's target repository). It is never read from the
	// worktree: a stage can write the worktree's git config and its .git
	// file.
	Repo string
	// Forge reads the files. nil gives no server.
	Forge forge.DefaultBranchFileService
}

// repoSlugPartRE is one half of an owner/name slug as GitHub allows it.
var repoSlugPartRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// splitRepoSlug returns the owner and name of an owner/name slug.
func splitRepoSlug(repo string) (owner, name string, ok bool) {
	owner, name, ok = strings.Cut(repo, "/")
	if !ok || !repoSlugPartRE.MatchString(owner) || !repoSlugPartRE.MatchString(name) {
		return "", "", false
	}
	return owner, name, true
}

// mcpSourceFiles are the files the pipeline's MCP servers come from, in the
// order ReadPipelineMcpServers merges them: a later file wins on a name.
var mcpSourceFiles = []string{".claude/settings.json", ".mcp.json"}

// ReadForgeMcpServers reads the pipeline's MCP servers as
// ReadPipelineMcpServers does, .mcp.json over .claude/settings.json's
// mcpServers, from src.Repo's default branch as its forge serves it: the
// branch, the commit at its head and both files come from one forge answer,
// and nothing of the repository on this machine (its refs, its objects, its
// git config or a worktree's .git file) is read, so no stage can choose them.
// It returns the servers and the source they were read from, such as
// owner/name@main (0123abc). A file the commit does not have, or has as
// anything but a regular file (a symbolic link is never followed),
// contributes nothing. The read, the identity's token included, is bounded by
// openCodeForgeTimeout. An error means the servers could not be read at all:
// it names the repository and never quotes a server or a value.
func ReadForgeMcpServers(ctx context.Context, src McpSource) (servers map[string]PipelineMcpServer, source string, err error) {
	if src.Repo == "" {
		return nil, "", errors.New("the run records no repository to read them from")
	}
	owner, name, ok := splitRepoSlug(src.Repo)
	if !ok {
		return nil, "", fmt.Errorf("the run's repository %q is not owner/name", src.Repo)
	}
	if src.Forge == nil {
		return nil, "", fmt.Errorf("there is no forge to read %s from", src.Repo)
	}
	ctx, cancel := context.WithTimeout(ctx, openCodeForgeTimeout)
	defer cancel()
	type answer struct {
		files *forgetypes.DefaultBranchFiles
		err   error
	}
	// The read runs apart, so the bound holds even for a forge that does not
	// honour ctx; the channel is buffered, so it never blocks on the send.
	done := make(chan answer, 1)
	go func() {
		files, err := src.Forge.DefaultBranchFiles(ctx, owner, name, mcpSourceFiles)
		done <- answer{files, err}
	}()
	var got answer
	select {
	case got = <-done:
	case <-ctx.Done():
		return nil, "", fmt.Errorf("the forge did not answer for %s within %s", src.Repo, openCodeForgeTimeout)
	}
	if got.err != nil {
		if ctx.Err() != nil {
			return nil, "", fmt.Errorf("the forge did not answer for %s within %s", src.Repo, openCodeForgeTimeout)
		}
		return nil, "", fmt.Errorf("the forge could not read %s: %v", src.Repo, got.err)
	}
	files := got.files
	if files == nil || files.Branch == "" || !isObjectID(files.Commit) {
		return nil, "", fmt.Errorf("the forge named no default branch head for %s", src.Repo)
	}
	source = fmt.Sprintf("%s@%s (%s)", src.Repo, files.Branch, files.Commit[:7])
	merged := map[string]PipelineMcpServer{}
	for _, path := range mcpSourceFiles {
		file, ok := files.Files[path]
		if !ok || !file.Regular {
			continue
		}
		for k, v := range extractServersFromJSON(file.Content) {
			merged[k] = v
		}
	}
	return merged, source, nil
}

// isObjectID reports whether s is a full SHA-1 or SHA-256 object id.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }) < 0
}

// compareMcpServers names where the working tree's MCP sources and the
// default branch's on the forge (base) differ: a server only the worktree
// defines, which OpenCode is not given; one the worktree defines differently,
// which it is given as the base defines it; and one only the base defines,
// which it is given all the same.
func compareMcpServers(worktree, base map[string]PipelineMcpServer) (worktreeOnly, changed, baseOnly []string) {
	for name, s := range worktree {
		b, ok := base[name]
		switch {
		case !ok:
			worktreeOnly = append(worktreeOnly, name)
		case !reflect.DeepEqual(b, s):
			changed = append(changed, name)
		}
	}
	for name := range base {
		if _, ok := worktree[name]; !ok {
			baseOnly = append(baseOnly, name)
		}
	}
	sort.Strings(worktreeOnly)
	sort.Strings(changed)
	sort.Strings(baseOnly)
	return worktreeOnly, changed, baseOnly
}
