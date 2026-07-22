package hooks

import "strings"

// cmdparse.go provides a small, dependency-free shell-command tokenizer used by
// the PreToolUse guards. The guards historically classified commands by
// substring-matching the entire command string (e.g. `strings.Contains(cmd,
// "push")`), which produced false positives whenever a benign word appeared
// inside an echo, a commit message, a `--body` payload, or a heredoc — see
// issue #4069. The fix is to parse the actual command structure and inspect the
// real `git`/`gh` argv rather than scan raw prose.
//
// The tokenizer is intentionally conservative, not a full POSIX shell parser. It
// understands single quotes, double quotes, ANSI-C `$'…'` quotes, backslash
// escapes, here-document bodies, and the top-level command separators
// `;` `&&` `||` `|` `&` and newlines. For the security-relevant gates this is
// sufficient: a command that *looks* like `git push origin main` is classified
// as such even under ambiguous quoting (fail closed), while quoted prose never
// contributes a token (no false positives).

// Segment is one command within a compound shell command (split on top-level
// `;` `&&` `||` `|` `&` newlines, and subshell/substitution `(` `)` backtick).
// Argv holds the whitespace-separated words with surrounding quotes removed;
// quoted spans collapse into a single word so their contents never split into
// separate tokens. PipedFromPrev is true when this segment receives the previous
// segment's stdout via a single `|` (used to group a pipeline for the careful
// SQL check — `echo 'DROP TABLE' | psql` is one pipeline, `echo 'DROP'; psql` is
// two).
type Segment struct {
	Raw           string
	Argv          []string
	PipedFromPrev bool
}

// CommandArgv returns the segment's argv with any leading `NAME=value`
// environment-assignment words removed, so Argv[0] is the actual command being
// run (e.g. `GH_TOKEN=x git push` → ["git", "push", …]).
func (s Segment) CommandArgv() []string {
	i := 0
	for i < len(s.Argv) && isEnvAssignment(s.Argv[i]) {
		i++
	}
	return s.Argv[i:]
}

// isEnvAssignment reports whether a word is a leading environment assignment
// (NAME=value), which precedes the real command in a shell invocation.
func isEnvAssignment(word string) bool {
	eq := strings.IndexByte(word, '=')
	if eq <= 0 {
		return false
	}
	name := word[:eq]
	for i, r := range name {
		if r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// SplitSegments parses a shell command into its command segments. Here-document
// bodies are removed first so their contents are never tokenized as commands.
func SplitSegments(cmd string) []Segment {
	operative := stripHeredocs(cmd)
	rawSegments := splitTopLevel(operative)
	segments := make([]Segment, 0, len(rawSegments))
	for _, raw := range rawSegments {
		trimmed := strings.TrimSpace(raw.text)
		if trimmed == "" {
			continue
		}
		segments = append(segments, Segment{Raw: trimmed, Argv: tokenize(trimmed), PipedFromPrev: raw.pipedFromPrev})
	}
	return segments
}

// Pipelines groups segments into pipelines: a run of segments connected by a
// single `|`. Each returned group is one pipeline (commands whose stdout feeds
// the next). Segments joined by `;` `&&` `||` `&` start a new pipeline.
func Pipelines(segments []Segment) [][]Segment {
	var pipes [][]Segment
	for _, seg := range segments {
		if seg.PipedFromPrev && len(pipes) > 0 {
			pipes[len(pipes)-1] = append(pipes[len(pipes)-1], seg)
		} else {
			pipes = append(pipes, []Segment{seg})
		}
	}
	return pipes
}

// StrippedText returns the command with here-document bodies removed and all
// quoted spans (single, double, ANSI-C) replaced by a space. It is the safe
// input for keyword/regex scanners: real operations survive, but prose inside
// echoes, commit messages, `--body` payloads, and heredocs cannot match.
func StrippedText(cmd string) string {
	return stripQuotedSpans(stripHeredocs(cmd))
}

// stripHeredocs removes here-document bodies (and their terminator lines) from a
// command, leaving the command lines (including the `<<DELIM` redirection token)
// intact. Detection is quote-aware so `echo "<<EOF"` is not treated as a
// heredoc. Multiple heredocs introduced on one line are consumed in order.
func stripHeredocs(cmd string) string {
	if !strings.Contains(cmd, "<<") {
		return cmd
	}
	lines := strings.Split(cmd, "\n")
	var out []string
	var pending []heredoc // FIFO queue of open heredoc bodies
	for _, line := range lines {
		if len(pending) > 0 {
			h := pending[0]
			cmp := line
			if h.stripTabs {
				cmp = strings.TrimLeft(cmp, "\t")
			}
			if strings.TrimRight(cmp, "\r") == h.delim {
				pending = pending[1:] // terminator — drop it, body closed
			}
			continue // drop body line
		}
		out = append(out, line)
		pending = append(pending, scanHeredocStarts(line)...)
	}
	return strings.Join(out, "\n")
}

type heredoc struct {
	delim     string
	stripTabs bool
}

// scanHeredocStarts finds `<<[-]DELIM` here-document introducers on a single
// line, skipping any that appear inside quotes and ignoring `<<<` here-strings.
func scanHeredocStarts(line string) []heredoc {
	var found []heredoc
	var quote byte // 0, '\'' or '"'
	for i := 0; i < len(line); i++ {
		c := line[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' {
				i++
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '\\':
			i++
		case '<':
			if i+1 < len(line) && line[i+1] == '<' {
				// `<<<` is a here-string, not a heredoc body.
				if i+2 < len(line) && line[i+2] == '<' {
					i += 2
					continue
				}
				j := i + 2
				stripTabs := false
				if j < len(line) && line[j] == '-' {
					stripTabs = true
					j++
				}
				for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
					j++
				}
				delim, next := readHeredocDelim(line, j)
				if delim != "" {
					found = append(found, heredoc{delim: delim, stripTabs: stripTabs})
				}
				i = next - 1
			}
		}
	}
	return found
}

// readHeredocDelim reads a here-document delimiter token starting at index j,
// which may be quoted (`'EOF'`, `"EOF"`) or bare (`EOF`). It returns the
// unquoted delimiter and the index just past it.
func readHeredocDelim(line string, j int) (string, int) {
	if j >= len(line) {
		return "", j
	}
	if line[j] == '\'' || line[j] == '"' {
		q := line[j]
		k := j + 1
		var b strings.Builder
		for k < len(line) && line[k] != q {
			b.WriteByte(line[k])
			k++
		}
		return b.String(), k + 1
	}
	k := j
	for k < len(line) {
		c := line[k]
		if c == ' ' || c == '\t' || c == ';' || c == '|' || c == '&' || c == '<' || c == '>' {
			break
		}
		k++
	}
	// Compute bash's effective delimiter: a backslash or embedded quotes in the
	// delimiter word disable expansion but do NOT change the matched terminator
	// (`<<\EOF`, `<<E'O'F`, `<<EO"F"` all terminate at a line reading `EOF`).
	return unquoteDelim(line[j:k]), k
}

// unquoteDelim removes backslashes and quote characters from a bare here-document
// delimiter word, yielding the literal terminator bash will match against.
func unquoteDelim(raw string) string {
	if !strings.ContainsAny(raw, `\'"`) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch c {
		case '\\':
			if i+1 < len(raw) {
				i++
				b.WriteByte(raw[i])
			}
		case '\'', '"':
			// drop quote characters
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// rawSeg is a raw command segment plus whether it is piped from the previous one.
type rawSeg struct {
	text          string
	pipedFromPrev bool
}

// splitTopLevel splits a command on top-level separators (`;` `&&` `||` `|` `&`
// newlines, and subshell/command-substitution `(` `)` and backtick), respecting
// quotes and backslash escapes. Redirections and other operators are left inside
// their segment. A segment is flagged pipedFromPrev when the operator that
// preceded it was a single `|` (not `||`), so callers can reconstruct pipelines.
func splitTopLevel(cmd string) []rawSeg {
	var segments []rawSeg
	var cur strings.Builder
	var quote byte
	pipedFromPrev := false
	// flush emits the current segment (preceded by the prior operator) and sets
	// whether the NEXT segment is piped from this one.
	flush := func(nextPiped bool) {
		segments = append(segments, rawSeg{text: cur.String(), pipedFromPrev: pipedFromPrev})
		cur.Reset()
		pipedFromPrev = nextPiped
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if quote != 0 {
			cur.WriteByte(c)
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(cmd) {
				i++
				cur.WriteByte(cmd[i])
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
			cur.WriteByte(c)
		case '\\':
			cur.WriteByte(c)
			if i+1 < len(cmd) {
				i++
				cur.WriteByte(cmd[i])
			}
		case '\n', ';', '(', ')', '`':
			flush(false)
		case '&':
			if i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
			}
			flush(false)
		case '|':
			single := !(i+1 < len(cmd) && cmd[i+1] == '|')
			if !single {
				i++
			}
			flush(single)
		default:
			cur.WriteByte(c)
		}
	}
	flush(false)
	return segments
}

// tokenize splits a single segment into argv words, removing surrounding quotes.
// A quoted span collapses into (part of) one word so its inner whitespace does
// not create extra tokens.
func tokenize(seg string) []string {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote byte
	end := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(seg); i++ {
		c := seg[i]
		if quote != 0 {
			if c == quote {
				quote = 0
			} else if c == '\\' && quote == '"' && i+1 < len(seg) {
				i++
				cur.WriteByte(seg[i])
			} else {
				cur.WriteByte(c)
			}
			continue
		}
		switch c {
		case ' ', '\t', '\r':
			end()
		case '\'', '"':
			inWord = true
			quote = c
		case '$':
			// ANSI-C quoting $'…' — strip the $ and the quotes.
			if i+1 < len(seg) && seg[i+1] == '\'' {
				inWord = true
				quote = '\''
				i++
			} else {
				inWord = true
				cur.WriteByte(c)
			}
		case '\\':
			inWord = true
			if i+1 < len(seg) {
				i++
				cur.WriteByte(seg[i])
			}
		default:
			inWord = true
			cur.WriteByte(c)
		}
	}
	end()
	return words
}

// shellWrappers run a command string passed via `-c`; their inner script must be
// re-parsed so a wrapped `bash -c "git push origin main"` is still classified.
var shellWrappers = map[string]bool{"sh": true, "bash": true, "dash": true, "zsh": true, "ksh": true}

// prefixWrappers run another command given as their trailing argv (after the
// wrapper's own options), e.g. `sudo git push`, `env A=b git push`, `xargs git
// push`, `timeout 5 git reset --hard`. The real command is recovered by scanning
// for the first command-of-interest token.
var prefixWrappers = map[string]bool{
	"env": true, "sudo": true, "xargs": true, "nice": true, "timeout": true,
	"nohup": true, "time": true, "command": true, "setsid": true, "stdbuf": true,
	"doas": true, "ionice": true, "chrt": true,
}

// commandsOfInterest are the programs the guards classify (git operations and the
// careful-mode destructive programs). Wrapper expansion looks for one of these as
// the real inner command.
var commandsOfInterest = map[string]bool{
	"git": true, "docker": true, "docker-compose": true, "kubectl": true,
	"podman": true, "psql": true, "mysql": true, "mariadb": true, "mysqladmin": true,
	"sqlite3": true, "cockroach": true, "pgcli": true, "mycli": true, "sqlcmd": true,
	"usql": true, "clickhouse-client": true,
}

// ExpandWrappers re-parses command wrappers so a dangerous command hidden behind
// `bash -c`, `sh -c`, `sudo`, `env`, `xargs`, `timeout`, etc. is surfaced as its
// own segment and classified by the gates. Recursion is depth-limited.
func ExpandWrappers(segments []Segment) []Segment {
	return expandWrappers(segments, 0)
}

func expandWrappers(segments []Segment, depth int) []Segment {
	if depth > 4 {
		return segments
	}
	out := make([]Segment, 0, len(segments))
	for _, seg := range segments {
		argv := seg.CommandArgv()
		if len(argv) == 0 {
			out = append(out, seg)
			continue
		}
		prog := baseName(argv[0])

		if shellWrappers[prog] {
			if script := shellCArg(argv); script != "" {
				inner := expandWrappers(SplitSegments(script), depth+1)
				inner = inheritPipe(inner, seg.PipedFromPrev)
				out = append(out, inner...)
				continue
			}
		} else if prefixWrappers[prog] {
			if innerArgv := findInnerCommand(argv); innerArgv != nil {
				innerSeg := Segment{Raw: strings.Join(innerArgv, " "), Argv: innerArgv, PipedFromPrev: seg.PipedFromPrev}
				out = append(out, expandWrappers([]Segment{innerSeg}, depth+1)...)
				continue
			}
		}
		out = append(out, seg)
	}
	return out
}

// shellCArg returns the script argument that follows a `-c` (or a short cluster
// containing `c`, e.g. `-lc`) flag in a shell wrapper's argv, or "".
func shellCArg(argv []string) string {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		isC := a == "-c" || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.ContainsRune(a, 'c'))
		if isC && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}

// findInnerCommand returns the inner command argv of a prefix-wrapper invocation
// by locating the first command-of-interest token, or nil if none is present.
func findInnerCommand(argv []string) []string {
	for i := 1; i < len(argv); i++ {
		if commandsOfInterest[baseName(argv[i])] {
			return argv[i:]
		}
	}
	return nil
}

// inheritPipe sets the first segment's PipedFromPrev so an expanded wrapper keeps
// the outer pipeline connection (for the careful SQL grouping).
func inheritPipe(segs []Segment, piped bool) []Segment {
	if len(segs) > 0 {
		segs[0].PipedFromPrev = piped
	}
	return segs
}

// stripQuotedSpans replaces every quoted span (single, double, ANSI-C) with a
// space, leaving unquoted operative text intact. Backslash escapes outside
// quotes are preserved (their next char is kept).
func stripQuotedSpans(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\'', '"':
			q := c
			b.WriteByte(' ')
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' && q == '"' && i+1 < len(s) {
					i++
				}
				i++
			}
		case '$':
			if i+1 < len(s) && s[i+1] == '\'' {
				b.WriteByte(' ')
				i += 2
				for i < len(s) && s[i] != '\'' {
					if s[i] == '\\' && i+1 < len(s) {
						i++
					}
					i++
				}
			} else {
				b.WriteByte(c)
			}
		case '\\':
			b.WriteByte(c)
			if i+1 < len(s) {
				i++
				b.WriteByte(s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
