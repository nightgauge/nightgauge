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
	"unicode"
)

// OpenCode MCP servers (ADR-022 § 15, #1626).
//
// An OpenCode stage gets the pipeline's MCP servers, the ones a Claude stage
// gets from .mcp.json and .claude/settings.json, and no others, in the
// per-run config (adapters.BuildOpenCodeConfig). Two things differ from the
// Codex path, which reads the working tree and writes the servers verbatim:
//
//   - The servers come from the base branch, origin's default branch. A
//     server is a command OpenCode runs or a URL it sends the stage's tool
//     calls to, and a stage can write its own worktree, so a server one stage
//     added to .mcp.json would otherwise run in the next stage without review.
//     A stage can write the repository's refs too, which every worktree
//     shares, so no ref of the repository names the branch: origin itself
//     does (baseBranchRef), and the servers are read at the tip origin
//     reports whenever the repository holds that commit, with git's
//     replacement objects off (baseReadEnv).
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
//     every other server's credentials in it. BuildOpenCodeConfig checks every
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
// variable, as lookup reads the environment OpenCode inherits, holds a value
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

// openCodeOriginTimeout bounds the question to origin about its default
// branch, so an origin that does not answer holds a stage up no longer.
const openCodeOriginTimeout = 15 * time.Second

// baseReadEnv is the environment of every git command that reads the base:
// git would otherwise read a replacement object (refs/replace/) in place of
// the commit, tree or blob it replaces, and those refs, which every worktree
// shares and no fetch resets, are a stage's to write as well.
var baseReadEnv = []string{"GIT_NO_REPLACE_OBJECTS=1"}

// baseBranch is where the MCP servers are read from.
type baseBranch struct {
	// rev is what the blobs are read at: origin's tip when the repository
	// holds that commit, else the branch's remote-tracking ref.
	rev string
	// short names the branch for messages: origin/<branch>.
	short string
	// warnings are what the stage is told about the read, one line each.
	warnings []string
}

// originDefaultBranch asks origin which branch its HEAD names, the question
// `git remote set-head origin --auto` asks, and returns that branch and the
// commit origin reports at its tip. Git's own error is not passed on, because
// it can quote origin's URL, which can carry a credential.
func originDefaultBranch(ctx context.Context, top string) (branch, tip string, err error) {
	ctx, cancel := context.WithTimeout(ctx, openCodeOriginTimeout)
	defer cancel()
	out, err := gitOut(ctx, top, []string{"GIT_TERMINAL_PROMPT=0"}, "", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		if ctx.Err() != nil {
			return "", "", fmt.Errorf("origin could not be asked which branch that is: it did not answer within %s", openCodeOriginTimeout)
		}
		return "", "", errors.New("origin could not be asked which branch that is (`git ls-remote --symref origin HEAD` failed, and running it shows why)")
	}
	// The answer is "ref: refs/heads/<branch>\tHEAD" and "<commit>\tHEAD".
	for _, line := range strings.Split(out, "\n") {
		line, isSymref := strings.CutPrefix(line, "ref: ")
		value, name, ok := strings.Cut(line, "\t")
		switch {
		case !ok || name != "HEAD":
		case isSymref:
			if b, isBranch := strings.CutPrefix(value, "refs/heads/"); isBranch {
				branch = b
			}
		default:
			tip = value
		}
	}
	if branch == "" || strings.IndexFunc(branch, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", "", errors.New("origin's HEAD names no branch")
	}
	if !isObjectID(tip) {
		return "", "", fmt.Errorf("origin reports no commit at the tip of %s", branch)
	}
	return branch, tip, nil
}

// isObjectID reports whether s is a full SHA-1 or SHA-256 object id.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return !strings.ContainsRune("0123456789abcdef", r) }) < 0
}

// baseBranchRef resolves where the MCP servers are read from: origin's
// default branch, as origin names it (originDefaultBranch), at the tip origin
// reports when the repository holds that commit, else at the branch's
// remote-tracking ref, with a warning that it is behind origin. No ref of the
// repository chooses the branch: every worktree shares them and a stage can
// write them, and neither origin/HEAD, a remote-tracking ref for a branch
// origin does not have, nor a local branch is ever reset by a fetch, so any
// of them would let one stage choose the servers of every later one. An
// origin/HEAD that names another branch is named in a warning. A repository
// whose origin cannot be asked gets no server.
func baseBranchRef(ctx context.Context, top string) (baseBranch, error) {
	branch, tip, err := originDefaultBranch(ctx, top)
	if err != nil {
		return baseBranch{}, err
	}
	b := baseBranch{short: "origin/" + branch}
	tracking := "refs/remotes/origin/" + branch
	if out, err := gitOut(ctx, top, nil, "", "symbolic-ref", "-q", "refs/remotes/origin/HEAD"); err == nil {
		if head := strings.TrimSpace(out); head != tracking {
			b.warnings = append(b.warnings, fmt.Sprintf(
				"MCP servers: origin/HEAD names %s, but origin names %s as its default branch, so they are read from %s (`git remote set-head origin --auto` records origin's)",
				strings.TrimPrefix(head, "refs/remotes/"), branch, b.short))
		}
	}
	isCommit := func(rev string) bool {
		_, err := gitOut(ctx, top, baseReadEnv, "", "rev-parse", "-q", "--verify", rev+"^{commit}")
		return err == nil
	}
	switch {
	case isCommit(tip):
		b.rev = tip
	case isCommit(tracking):
		b.rev = tracking
		b.warnings = append(b.warnings, fmt.Sprintf(
			"MCP servers: %s is behind origin, whose tip the repository does not hold yet, so they are read from %s as it was last fetched (`git fetch origin %s` brings them up to date)",
			b.short, b.short, branch))
	default:
		return baseBranch{}, fmt.Errorf("the repository has not fetched %s (`git fetch origin %s`)", b.short, branch)
	}
	return b, nil
}

// mcpSourceFiles are the files the pipeline's MCP servers come from, in the
// order ReadPipelineMcpServers merges them: a later file wins on a name.
var mcpSourceFiles = []string{".claude/settings.json", ".mcp.json"}

// ReadBaseBranchMcpServers reads the pipeline's MCP servers as
// ReadPipelineMcpServers does, .mcp.json over .claude/settings.json's
// mcpServers, from the base branch's blobs (baseBranchRef) instead of the
// working tree, and returns the short name of the branch it read, such as
// origin/main, and the warnings the read gives. A file the branch does not
// track, or tracks as anything but a regular file (a symbolic link is never
// followed), contributes nothing. An error means the servers could not be
// read at all.
func ReadBaseBranchMcpServers(ctx context.Context, worktree string) (servers map[string]PipelineMcpServer, source string, warnings []string, err error) {
	top, err := gitOut(ctx, worktree, nil, "", "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, "", nil, fmt.Errorf("%s is not a git working tree", worktree)
	}
	top = strings.TrimSpace(top)
	base, err := baseBranchRef(ctx, top)
	if err != nil {
		return nil, "", nil, err
	}
	merged := map[string]PipelineMcpServer{}
	for _, path := range mcpSourceFiles {
		listing, err := gitOut(ctx, top, baseReadEnv, "", "ls-tree", "-z", "--full-tree", base.rev, "--", path)
		if err != nil {
			return nil, base.short, nil, fmt.Errorf("list %s on %s: %w", path, base.short, err)
		}
		oid, ok := regularBlob(listing, path)
		if !ok {
			continue
		}
		raw, err := gitOut(ctx, top, baseReadEnv, "", "cat-file", "blob", oid)
		if err != nil {
			return nil, base.short, nil, fmt.Errorf("read %s on %s: %w", path, base.short, err)
		}
		for k, v := range extractServersFromJSON([]byte(raw)) {
			merged[k] = v
		}
	}
	return merged, base.short, base.warnings, nil
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

// compareMcpServers names where the working tree's MCP sources and the base
// branch's differ: a server only the worktree defines, which OpenCode is not
// given; one the worktree defines differently, which it is given as the base
// defines it; and one only the base defines, which it is given all the same.
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
