package codexprovision

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// OpenCode MCP servers (ADR-022 § 15, #1626).
//
// An OpenCode stage gets the pipeline's MCP servers, the ones a Claude stage
// gets from .mcp.json and .claude/settings.json, and no others, in the
// per-run config (adapters.BuildOpenCodeConfig). Two things differ from the
// Codex path, which reads the working tree and writes the servers verbatim:
//
//   - The servers come from the base branch. A server is a command OpenCode
//     runs or a URL it sends the stage's tool calls to, and a stage can write
//     its own worktree, so a server one stage added to .mcp.json would
//     otherwise run in the next stage without review.
//   - Variable references are translated, never resolved. Claude expands
//     ${VAR} and ${VAR:-default} in a server's command, args, env, url and
//     headers; OpenCode substitutes {env:VAR} anywhere in its config text, in
//     its own process. Each ${VAR} becomes {env:VAR}, so Nightgauge reads no
//     variable's value and none is written into the config. A value that
//     already holds OpenCode's own {env:...} or {file:...} syntax refuses its
//     server: Claude passes such a value through as text, while OpenCode would
//     read the variable, or the file, it names.

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

// baseBranchRef resolves the branch the MCP servers are read from: origin's
// default branch as the repository records it (refs/remotes/origin/HEAD),
// else origin/main or origin/master, else a local main or master. It returns
// the full ref and a short name for messages.
func baseBranchRef(ctx context.Context, top string) (ref, short string, err error) {
	candidates := []string{"refs/remotes/origin/main", "refs/remotes/origin/master", "refs/heads/main", "refs/heads/master"}
	if out, err := gitOut(ctx, top, nil, "", "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if head := strings.TrimSpace(out); strings.HasPrefix(head, "refs/remotes/origin/") {
			candidates = append([]string{head}, candidates...)
		}
	}
	for _, c := range candidates {
		if _, err := gitOut(ctx, top, nil, "", "rev-parse", "-q", "--verify", c+"^{commit}"); err == nil {
			return c, strings.TrimPrefix(strings.TrimPrefix(c, "refs/remotes/"), "refs/heads/"), nil
		}
	}
	return "", "", errors.New("the repository has no origin/HEAD, origin/main, origin/master, main or master to read them from")
}

// mcpSourceFiles are the files the pipeline's MCP servers come from, in the
// order ReadPipelineMcpServers merges them: a later file wins on a name.
var mcpSourceFiles = []string{".claude/settings.json", ".mcp.json"}

// ReadBaseBranchMcpServers reads the pipeline's MCP servers as
// ReadPipelineMcpServers does, .mcp.json over .claude/settings.json's
// mcpServers, from the base branch's blobs (baseBranchRef) instead of the
// working tree, and returns the short name of the branch it read. A file the
// branch does not track, or tracks as anything but a regular file (a symbolic
// link is never followed), contributes nothing. An error means the servers
// could not be read at all.
func ReadBaseBranchMcpServers(ctx context.Context, worktree string) (map[string]PipelineMcpServer, string, error) {
	top, err := gitOut(ctx, worktree, nil, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", fmt.Errorf("%s is not a git working tree", worktree)
	}
	top = strings.TrimSpace(top)
	ref, short, err := baseBranchRef(ctx, top)
	if err != nil {
		return nil, "", err
	}
	merged := map[string]PipelineMcpServer{}
	for _, path := range mcpSourceFiles {
		listing, err := gitOut(ctx, top, nil, "", "ls-tree", "-z", "--full-tree", ref, "--", path)
		if err != nil {
			return nil, short, fmt.Errorf("list %s on %s: %w", path, short, err)
		}
		oid, ok := regularBlob(listing, path)
		if !ok {
			continue
		}
		raw, err := gitOut(ctx, top, nil, "", "cat-file", "blob", oid)
		if err != nil {
			return nil, short, fmt.Errorf("read %s on %s: %w", path, short, err)
		}
		for k, v := range extractServersFromJSON([]byte(raw)) {
			merged[k] = v
		}
	}
	return merged, short, nil
}

// regularBlob returns the object id `git ls-tree -z` lists for path when it is
// a regular file.
func regularBlob(listing, path string) (string, bool) {
	for _, entry := range strings.Split(listing, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok || name != path {
			continue
		}
		f := strings.Fields(meta)
		if len(f) == 3 && (f[0] == "100644" || f[0] == "100755") && f[1] == "blob" {
			return f[2], true
		}
	}
	return "", false
}

// worktreeOnlyServers names what the working tree's MCP sources define that
// OpenCode is not given: a server the base branch does not define, and one
// whose definition there differs.
func worktreeOnlyServers(worktree, base map[string]PipelineMcpServer) (added, changed []string) {
	for name, s := range worktree {
		b, ok := base[name]
		switch {
		case !ok:
			added = append(added, name)
		case !reflect.DeepEqual(b, s):
			changed = append(changed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	return added, changed
}
