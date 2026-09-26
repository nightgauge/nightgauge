// opencode_usage.go completes an opencode stage's RunResult after the process
// exits (ADR-022 § 1-3, § 9, § 22): the usage of subagent sessions, which the
// stream never carries; the served model, which the stream does not name; the
// CLI's version; the marker for the permission OpenCode rejected on its own; and
// the redaction of credentials from every line the child prints.
//
// Every opencode process started here (--version, db, export) runs in its own
// process group under a timeout, from the run's own root directory, with
// --pure, and with only the variables that point it at the run's root
// (openCodeHelperEnv): it reads the run's session database, loads no plugin,
// and holds no credential. Its stdout is an unnamed file, never a pipe, which
// would cut a long export short (helper). A failure never fails the stage: it
// marks usage partial and leaves a drift marker.
package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/models"
)

const (
	// openCodeHelperTimeout bounds each opencode process the fold starts.
	openCodeHelperTimeout = 10 * time.Second
	// openCodeFoldBudget bounds the whole fold of one stage, whatever the
	// number of sessions: 64 exports that each run close to the timeout
	// cannot hold a stage's end for ten minutes.
	openCodeFoldBudget = 2 * time.Minute
	// openCodeMaxDescendants caps the subagent sessions folded per stage.
	openCodeMaxDescendants = 64
	// openCodeHelperMaxOutput caps what one helper may print. A sanitized
	// export keeps every message's shape, so a long session is large, but
	// not this large.
	openCodeHelperMaxOutput = 64 << 20
	// openCodeMaxSessionRows caps the session rows discovery reads.
	openCodeMaxSessionRows = 4096
	// streamLineLimit is the longest line the manager keeps from a child,
	// the limit its line scanner has always had.
	streamLineLimit = 1024 * 1024
)

// PermissionRejectedMarker and PermissionDeniedMarker name a permission
// OpenCode rejected by itself, for failure classification (#1631). The first
// is for a tool the stage's allowed tools grant, so the adapter's own
// permission posture refused it; the second for one they do not.
const (
	PermissionRejectedMarker = "[adapter-permission-rejected]"
	PermissionDeniedMarker   = "[permission-denied]"
)

// ansiEscapeRE matches the terminal escape sequences OpenCode puts around the
// "!" of an auto-reject line, even when stderr is not a terminal.
var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// openCodeAutoRejectStartRE is the start of the notice opencode 1.18.30's run
// command prints to stderr when it rejects a permission request, for its own
// session and for every subagent session:
//
//	! permission requested: <permission> (<patterns>); auto-rejecting
//
// The patterns are the tool's input, joined with ", " and printed unescaped,
// so a call whose input holds a newline (a heredoc, a commit message with a
// body, `python -c` over several lines) spreads the notice over several
// lines, the last ending in openCodeAutoRejectEnd. The permission is taken
// from the first line only, and nothing else of the notice is ever read.
var openCodeAutoRejectStartRE = regexp.MustCompile(`^!\s*permission requested: (\S+) \(`)

// openCodeAutoRejectEnd ends the notice's last line.
const openCodeAutoRejectEnd = "); auto-rejecting"

// openCodePermissions are the permissions opencode 1.18.30 asks for itself,
// read from its bundled source. A notice naming any other, such as an MCP
// tool's, which a config names, is recorded as openCodeUnknownPermission: a
// marker is what failure classification reads, so no name a config chose may
// reach it.
var openCodePermissions = map[string]bool{
	"bash": true, "read": true, "edit": true, "glob": true, "grep": true,
	"task": true, "webfetch": true, "websearch": true, "todowrite": true,
	"skill": true, "lsp": true, "external_directory": true, "doom_loop": true,
	"workflow_tool_approval": true,
}

// openCodeUnknownPermission stands for a rejected permission this parser
// could not name.
const openCodeUnknownPermission = "unknown"

// OpenCodeAutoRejectMarker reads the first line of an auto-reject notice.
// When it is one it returns the classification marker for the rejected
// permission: PermissionRejectedMarker when allowedTools (Claude Code tool
// names, as RunOptions.AllowedTools holds them) grant it, and
// PermissionDeniedMarker otherwise. A line naming no permission of
// openCodePermissions yields "tool=unknown".
func OpenCodeAutoRejectMarker(line string, allowedTools []string) (string, bool) {
	permission, ok := openCodeRejectedPermission(openCodePlain(line))
	if !ok {
		return "", false
	}
	return openCodeRejectionMarker(permission, adapters.OpenCodeToolsAllowed(allowedTools)), true
}

// openCodePlain is a stderr line as the notice checks read it: without
// terminal escapes and surrounding space.
func openCodePlain(line string) string {
	return strings.TrimSpace(ansiEscapeRE.ReplaceAllString(line, ""))
}

// openCodeRejectedPermission reads a plain line that may start an auto-reject
// notice, and returns the permission the notice names ("unknown" when it is
// none of openCodePermissions).
func openCodeRejectedPermission(plain string) (string, bool) {
	m := openCodeAutoRejectStartRE.FindStringSubmatch(plain)
	if m == nil {
		return "", false
	}
	if !openCodePermissions[m[1]] {
		return openCodeUnknownPermission, true
	}
	return m[1], true
}

// openCodeKeptNotice is what an auto-reject notice becomes in the stderr the
// stage keeps and streams: the permission, without the patterns. The patterns
// are the call's input, authored by the model, and the stage's stderr is what
// failure classification reads.
func openCodeKeptNotice(permission string) string {
	return "! permission requested: " + permission + " (..." + openCodeAutoRejectEnd
}

// openCodeToolRejectionPermission maps a rejected tool_use event's own
// part.tool name (stream.go's OpenCodeStream.RejectedTool, opencode's own
// lowercase tool id) to the permission key openCodeRejectionMarker
// classifies by. 1.18.30 has no permission of its own for "write" or
// "apply_patch": both are governed by the same "edit" permission the
// generated map's own "edit" key controls (ADR-022 § 9; the map's edit
// deny-list is what a "write" or "apply_patch" call is actually refused
// against). Every other tool name already equals its own permission key
// ("bash", "read", "glob", "grep", "task", "webfetch", "websearch",
// "todowrite", "skill"), so it passes through unchanged.
func openCodeToolRejectionPermission(tool string) string {
	switch tool {
	case "write", "apply_patch":
		return "edit"
	default:
		return tool
	}
}

func openCodeRejectionMarker(permission string, allowed map[string]bool) string {
	if allowed[permission] {
		return PermissionRejectedMarker + " tool=" + permission
	}
	return PermissionDeniedMarker + " tool=" + permission
}

// credentialLeft is what may come right before a credential, captured so the
// replacement keeps it: the start of the text; a character no credential
// holds; a JSON escape whose last character is a letter or a digit (`\n`,
// `\t`, a four-hex-digit unicode escape), which is what precedes a credential at the start of any
// line but the first of a tool's output in a --format json event; or a
// terminal escape sequence, raw or JSON-escaped, which precedes a credential
// a tool prints in colour. A plain word boundary misses the last two, since
// `n` and `m` are word characters.
const credentialLeft = `(^|[^A-Za-z0-9_]|\\[bfnrt]|\\u[0-9A-Fa-f]{4}|(?:\x1b|\\u001[bB])\[[0-9;?]*[A-Za-z])`

// credentialPatterns are the credential shapes removed from every line an
// opencode child prints, after the values of the variables the adapter names
// (envValueRedactor), so a secret the child read from a file or inherited is
// removed too (ADR-022 § 22). Each shape is specific to credentials: a
// provider's key prefix, a forge token prefix, a bearer or authorization
// credential, a URL's user:password, a credential query parameter. What a
// pattern replaces holds no quote and no backslash, beyond the left context
// its replacement keeps, and no replacement adds one, so a JSON event stays
// valid JSON.
var credentialPatterns = []struct {
	// hints are lower-case substrings at least one of which every match
	// contains, so a line holding none skips the expression.
	hints []string
	re    *regexp.Regexp
	repl  string
}{
	// API keys, by their issuers' prefixes: sk- (OpenAI, Anthropic,
	// OpenRouter, DeepSeek), xai- (xAI), AIza (Google), AKIA and ASIA (AWS
	// access key ids), gsk_ (Groq), hf_ (Hugging Face).
	{[]string{"sk-", "xai-", "aiza", "akia", "asia", "gsk_", "hf_"},
		regexp.MustCompile(credentialLeft + `(?:sk-[A-Za-z0-9_-]{20,}|xai-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|(?:AKIA|ASIA)[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,})`),
		"${1}[REDACTED:api-key]"},
	// GitHub tokens of every kind, and GitLab personal access tokens.
	{[]string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-"},
		regexp.MustCompile(credentialLeft + `(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,})`),
		"${1}[REDACTED:forge-token]"},
	// A bearer credential, and the credential of an Authorization header.
	{[]string{"bearer"}, regexp.MustCompile(`(?i)` + credentialLeft + `(bearer\s+)[A-Za-z0-9._~+/-]{16,}=*`), "${1}${2}[REDACTED:bearer-token]"},
	{[]string{"authorization"},
		regexp.MustCompile(`(?i)` + credentialLeft + `(authorization\s*[:=]\s*(?:basic|token)\s+)[A-Za-z0-9._~+/-]{8,}=*`),
		"${1}${2}[REDACTED:authorization]"},
	// user:password in a URL.
	{[]string{"://"}, regexp.MustCompile(credentialLeft + `([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:"'\\]+:[^\s/@"'\\]+@`), "${1}${2}[REDACTED:userinfo]@"},
	// A credential in a query string.
	{[]string{"key=", "token=", "secret=", "passw", "pwd=", "sig=", "signature=", "credential="},
		regexp.MustCompile(`(?i)([?&](?:api[_-]?key|apikey|key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|secret|password|passwd|pwd|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=)[^&#\s"'\\]+`),
		"${1}[REDACTED:query-credential]"},
}

// openCodeOutputRedactor returns the redaction every line an opencode child
// prints goes through (ADR-022 § 22), stdout and stderr alike. redact is the
// replacer of the values of the variables the adapter names
// (envValueRedactor), or nil.
//
// A --format json event carries a tool's output as a JSON string, so what the
// tool printed is escaped there: its newlines are `\n`, its quotes `\"`. Each
// string of a line that holds a JSON object is therefore decoded, redacted
// and, only when that changed it, re-encoded in place, the way
// redact-opencode.jq redacts a fixture. The whole line is then redacted as
// text as well, which is all a line that is not JSON gets.
func openCodeOutputRedactor(redact *strings.Replacer) func([]byte) []byte {
	text := func(s string) string {
		if redact != nil {
			s = redact.Replace(s)
		}
		return RedactCredentials(s)
	}
	return func(line []byte) []byte {
		return []byte(text(string(redactJSONStrings(line, text))))
	}
}

// redactJSONStrings applies redact to the decoded value of every JSON string
// in line, when line holds a JSON object, and returns line with each string
// that redact changed re-encoded in place. Nothing else of the line changes:
// no key is reordered and no number is re-formatted. A string that does not
// decode is left as it is, and so is the rest of a line whose last string
// does not end. The slice returned may be line itself.
func redactJSONStrings(line []byte, redact func(string) string) []byte {
	if first := bytes.TrimLeft(line, " \t"); len(first) == 0 || first[0] != '{' {
		return line
	}
	var out []byte
	last := 0
	for i := 0; i < len(line); i++ {
		if line[i] != '"' {
			continue
		}
		end, escaped := i+1, false
		for end < len(line) && line[end] != '"' {
			if line[end] == '\\' {
				escaped = true
				end++
			}
			end++
		}
		if end >= len(line) {
			break
		}
		literal := line[i : end+1]
		var value string
		if !escaped {
			value = string(literal[1 : len(literal)-1])
		} else if json.Unmarshal(literal, &value) != nil {
			i = end
			continue
		}
		if redacted := redact(value); redacted != value {
			out = append(append(out, line[last:i]...), '"')
			out = append(append(out, jsonEscaped(redacted)...), '"')
			last = end + 1
		}
		i = end
	}
	if out == nil {
		return line
	}
	return append(out, line[last:]...)
}

// jsonEscaped is s as the content of a JSON string, without its quotes, the
// way OpenCode's JSON.stringify writes it: `<`, `>` and `&` are not escaped.
func jsonEscaped(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	quoted := bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
	return string(quoted[1 : len(quoted)-1])
}

// RedactCredentials removes every credentialPatterns shape from s.
func RedactCredentials(s string) string {
	lower := strings.ToLower(s)
	for _, p := range credentialPatterns {
		for _, hint := range p.hints {
			if strings.Contains(lower, hint) {
				if r := p.re.ReplaceAllString(s, p.repl); r != s {
					s, lower = r, strings.ToLower(r)
				}
				break
			}
		}
	}
	return s
}

// oversizeEdge is how many bytes of each end of an oversized line forEachLine
// hands onOversize: enough for the start and the end of an auto-reject notice.
const oversizeEdge = 256

// forEachLine calls onLine with every line r holds, without its line ending,
// the way bufio.Scanner splits lines. A line longer than limit bytes is not
// delivered: it is read to its end and discarded, and onOversize is called
// with its first and last oversizeEdge bytes, so one oversized line costs a
// marker instead of ending the read and leaving the child blocked on a full
// pipe. The slices a callback receives are valid only until it returns.
func forEachLine(r io.Reader, limit int, onLine func([]byte), onOversize func(head, tail []byte)) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var line, head, tail []byte
	size, over := 0, false
	for {
		chunk, err := br.ReadSlice('\n')
		complete := err == nil
		data := chunk
		if complete {
			data = chunk[:len(chunk)-1]
		}
		size += len(data)
		if !over && size > limit {
			over = true
			head = append(head[:0], line[:min(len(line), oversizeEdge)]...)
			if room := oversizeEdge - len(head); room > 0 {
				head = append(head, data[:min(len(data), room)]...)
			}
			// One byte more than the edge, for a '\r' ending the line.
			tail = appendTail(tail[:0], line, oversizeEdge+1)
			line = line[:0]
		}
		if over {
			tail = appendTail(tail, data, oversizeEdge+1)
		} else {
			line = append(line, data...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if complete || size > 0 {
			if over {
				end := bytes.TrimSuffix(tail, []byte{'\r'})
				onOversize(head, end[max(0, len(end)-oversizeEdge):])
			} else {
				onLine(bytes.TrimSuffix(line, []byte{'\r'}))
			}
		}
		line, size, over = line[:0], 0, false
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

// appendTail appends data to tail and keeps only its last n bytes.
func appendTail(tail, data []byte, n int) []byte {
	if len(data) >= n {
		return append(tail[:0], data[len(data)-n:]...)
	}
	tail = append(tail, data...)
	if len(tail) > n {
		tail = append(tail[:0], tail[len(tail)-n:]...)
	}
	return tail
}

// openCodeRun is the opencode half of one stage in Manager.RunStage: it
// watches the child's stderr for auto-reject notices and, once the child has
// exited, completes the RunResult.
type openCodeRun struct {
	stream  *OpenCodeStream
	allowed map[string]bool
	// marker is the classification marker of the first auto-reject notice,
	// empty until one is read; it is set once and never replaced. Only the
	// stderr reader touches it.
	marker string
	// inNotice is set while the first notice spans lines: the lines up to
	// the one ending in openCodeAutoRejectEnd are the rejected call's input.
	// Only the stderr reader touches it.
	inNotice bool
	// fold starts the post-exit opencode processes; tests replace it.
	fold openCodeFold
}

func newOpenCodeRun(stream *OpenCodeStream, allowedTools []string) *openCodeRun {
	return &openCodeRun{
		stream:  stream,
		allowed: adapters.OpenCodeToolsAllowed(allowedTools),
		fold: openCodeFold{
			timeout:        openCodeHelperTimeout,
			budget:         openCodeFoldBudget,
			maxDescendants: openCodeMaxDescendants,
		},
	}
}

// stderrLine is what observeStderr made of one stderr line.
type stderrLine int

const (
	// stderrKept is an ordinary line, kept as it is.
	stderrKept stderrLine = iota
	// stderrNotice is the first line of the first auto-reject notice, kept
	// as openCodeKeptNotice.
	stderrNotice
	// stderrDropped is a line nothing of which is kept.
	stderrDropped
)

// observeStderr reads one stderr line exactly as the child printed it, before
// redaction, which may rewrite the end a notice is recognized by. start and
// end are the whole line, or the first and last bytes of a line forEachLine
// dropped for its length. It returns the kept notice for a stderrNotice line.
// Nothing of the line is stored here: the caller redacts what it keeps.
//
// The first auto-reject notice decides the stage's classification marker,
// for its permission, and is kept as openCodeKeptNotice: the call's input
// never reaches the stage's stderr, which failure classification reads. It
// is the one notice OpenCode printed before any rejected call's input could
// be printed, so it is the only one whose permission the model cannot have
// chosen. The lines it spans after its first are that input, so they are
// dropped, and none of them is read as a notice of its own. The input is
// printed unescaped, so a line of it that itself ends in
// openCodeAutoRejectEnd ends the notice early, and nothing tells the input's
// later lines from what OpenCode prints next, a later notice included. So
// from the first notice on, no other line is kept: a later notice, whether a
// subagent's real one or one the input forged, is dropped with a drift
// marker counting it and yields no classification marker, and every other
// line is dropped with a drift marker of its own. The one marker therefore
// ends the stage's stderr, where the scheduler's tail reads it.
func (r *openCodeRun) observeStderr(start, end string) (string, stderrLine) {
	if r.inNotice {
		r.inNotice = !strings.HasSuffix(openCodePlain(end), openCodeAutoRejectEnd)
		return "", stderrDropped
	}
	plain := openCodePlain(start)
	permission, ok := openCodeRejectedPermission(plain)
	switch {
	case !ok && r.marker == "":
		return "", stderrKept
	case !ok:
		if plain != "" {
			r.stream.Drift("a stderr line after an auto-reject notice was not kept: OpenCode prints the rejected call's input unescaped, so it cannot be told from that input")
		}
		return "", stderrDropped
	case r.marker != "":
		r.stream.Drift("an auto-reject notice after the first was not kept and decides no failure kind: it may be a subagent's, or one the first rejected call's input forged")
		return "", stderrDropped
	}
	r.inNotice = !strings.HasSuffix(openCodePlain(end), openCodeAutoRejectEnd)
	if permission == openCodeUnknownPermission {
		r.stream.Drift("an auto-reject line on stderr names no permission this parser recognizes")
	}
	r.marker = openCodeRejectionMarker(permission, r.allowed)
	return openCodeKeptNotice(permission), stderrNotice
}

// openCodeOutcome is what finish learned, applied to the RunResult.
type openCodeOutcome struct {
	exitedZero bool
	// recoverableExit is set when the process exited non-zero with only
	// errors the session recovered from (#2168); the scheduler then lets the
	// stage's post-condition gate decide.
	recoverableExit bool
	// marker is the classification marker, or empty.
	marker  string
	version string
	served  OpenCodeServedModel
	// endpoint is the id of the declared endpoint that served the model, or
	// empty when the served model's provider key names none.
	endpoint string
	cost     float64
	partial  bool
	drift    []string
}

// openCodeExit is how an opencode stage's child ended, and what finish needs
// to read the rest.
type openCodeExit struct {
	// bin is the stage's resolved binary, and env its environment, from
	// which the fold keeps only openCodeHelperEnv.
	bin string
	env []string
	// runRoot is the run's own root directory (ADR-022 § 8), the working
	// directory of every process the fold starts; never the worktree.
	runRoot string
	// exitCode is -1 when the child did not exit on its own.
	exitCode int
	// dispatched is the value the stage passed as -m (adapters.OpenCodeModelArg).
	dispatched string
	// stopped is set when the operator stopped the stage: then no process is
	// started after it.
	stopped bool
	// endpoints are the ids of the endpoints the run's config declares
	// (adapters.RunRoot.Endpoints).
	endpoints []string
}

// finish runs once the child has exited, before the RunResult is built from
// acc: it folds the subagent sessions' usage into acc, reads the served
// model and the CLI version, and closes the stream's drift checks. ctx is
// the dispatch's own context, not the stage's: a stage that ran out of time
// still has its usage read, and a cancelled dispatch stops the fold. A stage
// the operator stopped starts nothing: its usage is the stream's, marked
// partial when it had a session whose subagents went unread.
func (r *openCodeRun) finish(ctx context.Context, exit openCodeExit, acc *TokenAccumulator) openCodeOutcome {
	var res openCodeFoldResult
	if exit.stopped {
		res.served = ResolveOpenCodeServedModel("", "", exit.dispatched)
		if r.stream.SessionID != "" {
			res.partial = true
			r.stream.Drift("usage partial: the stage was stopped, so no opencode process was started to read its subagent sessions")
		}
	} else {
		f := r.fold
		f.bin, f.env, f.dir = exit.bin, openCodeHelperEnv(exit.env), exit.runRoot
		res = f.run(ctx, r.stream, exit.dispatched)
	}
	acc.addOpenCodeTokens(res.children)
	if r.inNotice {
		r.stream.Drift("an auto-reject notice on stderr did not end with %q", openCodeAutoRejectEnd)
	}
	marker := r.marker
	if r.stream.RejectedToolCalls > 0 && !r.stream.TerminalRejection() {
		// Every rejected call was recovered from (#2168): a later tool call
		// completed and its step finished. The rejection did not end the
		// run, so it is a warning, not adapter_permission_rejected, and a
		// stderr notice for it decides nothing either.
		fmt.Fprintf(os.Stderr, "[opencode-recovered] %d rejected tool call(s) recovered from; not classified as a permission rejection\n", r.stream.RecoveredRejections)
		marker = ""
	} else if r.stream.RejectedToolCalls > 0 && marker == "" {
		// OpenCode rejected the stage's own tool call, and stderr did not say
		// which permission: a "deny" match (this map's only rejection shape,
		// ADR-022 § 9) never prints the "auto-rejecting" notice a stderr-only
		// read depends on. The run still stopped there, so it still fails.
		// The rejected tool_use event's own part.tool (AC3, #1638 fix round)
		// is what names it instead, mapped through
		// openCodeToolRejectionPermission (write/apply_patch -> edit); an
		// event that named no tool at all still falls back to
		// openCodeUnknownPermission.
		r.stream.Drift("the stream shows a tool call OpenCode rejected, but stderr carried no auto-reject line naming its permission; classified from the rejected tool_use event's own tool name")
		tool := openCodeUnknownPermission
		if r.stream.RejectedTool != "" {
			tool = openCodeToolRejectionPermission(r.stream.RejectedTool)
		}
		marker = openCodeRejectionMarker(tool, r.allowed)
	}
	r.stream.Finish(exit.exitCode)
	var endpoint string
	if res.served.Key != "" && slices.Contains(exit.endpoints, res.served.Key) {
		endpoint = res.served.Key
	}
	return openCodeOutcome{
		exitedZero:      exit.exitCode == 0,
		recoverableExit: exit.exitCode > 0 && !exit.stopped && marker == "" && r.stream.RecoverableExit(),
		marker:          marker,
		version:         res.version,
		served:          res.served,
		endpoint:        endpoint,
		cost:            r.stream.ReportedCostUSD + res.childCost,
		partial:         res.partial,
		drift:           r.stream.DriftMarkers(),
	}
}

// openCodeHelperEnvNames are the variables every process the fold starts
// keeps from the stage's environment: what finds the binary and its
// temporary directory, and the four XDG directories that point it at the
// run's own session database. No credential is among them: the forge token,
// the provider's key and the server password stay with the stage.
var openCodeHelperEnvNames = map[string]bool{
	"PATH": true, "HOME": true, "TMPDIR": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true,
}

// openCodeHelperEnv returns the entries of env, a stage's KEY=VALUE
// environment, that a fold process keeps: openCodeHelperEnvNames and the
// OPENCODE_DISABLE_* switches.
func openCodeHelperEnv(env []string) []string {
	var kept []string
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if openCodeHelperEnvNames[key] || strings.HasPrefix(key, "OPENCODE_DISABLE_") {
			kept = append(kept, kv)
		}
	}
	return kept
}

// apply writes the outcome onto the RunResult. A rejected permission ends
// the run on 1.18.30 even though the process exits 0, so an exit-0 run with
// one is reported as exit 1 (ADR-022 § 9), and its marker is the last line of
// Stderr, where failure classification reads the reason.
func (o openCodeOutcome) apply(result *adapters.RunResult) {
	result.ServedModel = o.served.Model
	result.ModelProvider = o.served.Provider
	result.UpstreamModel = o.served.Upstream
	result.Endpoint = o.endpoint
	result.AdapterVersion = o.version
	result.AdapterReportedCostUSD = o.cost
	result.UsagePartial = o.partial
	result.DriftMarkers = o.drift
	result.RecoverableExit = o.recoverableExit
	if o.marker == "" {
		return
	}
	if result.Stderr != "" && !strings.HasSuffix(result.Stderr, "\n") {
		result.Stderr += "\n"
	}
	result.Stderr += o.marker + "\n"
	if o.exitedZero && result.ExitCode == 0 {
		result.ExitCode = 1
	}
}

// report logs the drift markers and streams every marker to the stage's
// output, the classification marker last, so an operator watching the stage
// sees them.
func (o openCodeOutcome) report(opts StageOptions) {
	for _, m := range o.drift {
		fmt.Fprintf(os.Stderr, "%s %s#%d %s: %s\n", OpenCodeDriftMarker, opts.Repo, opts.IssueNumber, opts.Stage,
			strings.TrimPrefix(m, OpenCodeDriftMarker+" "))
	}
	if opts.Streamer == nil {
		return
	}
	for _, m := range o.drift {
		opts.Streamer.OnOutput("stderr", []byte(m+"\n"))
	}
	if o.marker != "" {
		opts.Streamer.OnOutput("stderr", []byte(o.marker+"\n"))
	}
}

// OpenCodeServedModel is the model that served an opencode stage, recorded as
// ADR-022 § 1 and § 2 decide.
type OpenCodeServedModel struct {
	// Provider is the normalized provider: "lm-studio" for key lmstudio,
	// "other" for a key Nightgauge does not recognize.
	Provider string
	// Model is the recorded model: a registry model's bare id, any other
	// model as "<provider>/<id>", and an "other" model as its raw
	// provider-qualified id.
	Model string
	// Upstream is the raw -m value exactly as passed to OpenCode (ADR-022
	// § 2), the dispatched model trimmed of surrounding space, also when the
	// export shows that another model served the stage.
	Upstream string
	// Key is the OpenCode provider key of the model that served the stage,
	// as the export or -m spells it ("lmstudio"), before § 1 normalizes it.
	// It is what names a declared endpoint.
	Key string
}

// ResolveOpenCodeServedModel records the model that served a stage. The
// stream names no model, so Provider and Model come from the session export's
// assistant message (providerID and modelID) when there is one, and otherwise
// from the model the stage was dispatched with (-m). Upstream is always the
// dispatched -m.
func ResolveOpenCodeServedModel(providerID, modelID, dispatched string) OpenCodeServedModel {
	raw := dispatched
	if providerID != "" && modelID != "" {
		raw = providerID + "/" + modelID
	}
	if raw == "" {
		return OpenCodeServedModel{}
	}
	provider, bareID, _ := models.ParseOpenCodeModel(raw)
	served := OpenCodeServedModel{Provider: provider, Model: raw, Upstream: dispatched}
	if key, _, ok := strings.Cut(raw, "/"); ok {
		served.Key = key
	}
	if provider == "other" || bareID == "" {
		return served
	}
	for _, m := range models.All() {
		if m.ID == bareID && m.Provider == provider {
			served.Model = bareID
			return served
		}
	}
	served.Model = provider + "/" + bareID
	return served
}

// openCodeFold reads what the stream cannot tell: the CLI's version, the
// served model and the usage of every subagent session.
type openCodeFold struct {
	bin            string
	env            []string
	dir            string
	timeout        time.Duration
	budget         time.Duration
	maxDescendants int
}

// openCodeFoldResult is what a fold read.
type openCodeFoldResult struct {
	version   string
	served    OpenCodeServedModel
	children  OpenCodeTokens
	childCost float64
	partial   bool
}

// openCodeSessionIDRE is the shape of an OpenCode session id. An id is
// checked before it becomes an argument, so none can read as a flag.
var openCodeSessionIDRE = regexp.MustCompile(`^ses_[0-9A-Za-z]{1,64}$`)

// openCodeSessionTreeQuery lists every session that has a parent, from the
// run's own database. No value from the stream or the run is ever put into
// it: the tree is walked here, not in SQL.
var openCodeSessionTreeQuery = fmt.Sprintf(
	"SELECT id, parent_id FROM session WHERE parent_id IS NOT NULL ORDER BY time_created, id LIMIT %d",
	openCodeMaxSessionRows)

// run folds one stage. Its failures are drift markers on stream and a
// partial result, never an error.
func (f openCodeFold) run(ctx context.Context, stream *OpenCodeStream, dispatched string) openCodeFoldResult {
	ctx, cancel := context.WithTimeout(ctx, f.budget)
	defer cancel()
	var res openCodeFoldResult
	res.served = ResolveOpenCodeServedModel("", "", dispatched)

	if v, err := f.version(ctx); err != nil {
		stream.Drift("opencode --version: %v", err)
	} else {
		res.version = v
	}

	parent := stream.SessionID
	if parent == "" {
		return res
	}
	if !openCodeSessionIDRE.MatchString(parent) {
		stream.Drift("usage partial: the stream names a session id of an unrecognized shape, so no subagent session was folded")
		res.partial = true
		return res
	}

	if provider, model, err := f.servedModel(ctx, parent); err != nil {
		stream.Drift("the served model is the dispatched model: exporting the session failed: %v", err)
	} else if provider != "" && model != "" {
		res.served = ResolveOpenCodeServedModel(provider, model, dispatched)
	}

	children, truncated, err := f.descendants(ctx, parent)
	if err != nil {
		stream.Drift("usage partial: listing the subagent sessions failed: %v", err)
		res.partial = true
		return res
	}
	if truncated {
		stream.Drift("usage partial: the stage has more than %d subagent sessions, and only the first %d were folded", f.maxDescendants, f.maxDescendants)
		res.partial = true
	}
	for _, id := range children {
		tokens, cost, err := f.sessionUsage(ctx, id)
		if err != nil {
			stream.Drift("usage partial: exporting a subagent session failed: %v", err)
			res.partial = true
			continue
		}
		res.children.Input += nonNegative(tokens.Input)
		res.children.Output += nonNegative(tokens.Output)
		res.children.Reasoning += nonNegative(tokens.Reasoning)
		res.children.Cache.Read += nonNegative(tokens.Cache.Read)
		res.children.Cache.Write += nonNegative(tokens.Cache.Write)
		if cost > 0 {
			res.childCost += cost
		}
	}
	return res
}

// openCodeExportArgs is the argv of a session's export. --sanitize redacts
// the transcript; --pure loads no plugin. Observed on 1.18.30, `export`
// bootstraps a project from its working directory, loading its .opencode/
// config and plugins and installing their dependencies into it, so the fold
// also runs it from the run's root rather than the worktree.
func openCodeExportArgs(session string) []string {
	return []string{"export", session, "--sanitize", "--pure"}
}

// servedModel reads the provider and model of the session's last assistant
// message from its sanitized export. Nothing else of the export is read.
func (f openCodeFold) servedModel(ctx context.Context, session string) (string, string, error) {
	out, err := f.helper(ctx, openCodeExportArgs(session)...)
	if err != nil {
		return "", "", err
	}
	var export struct {
		Messages []struct {
			Info struct {
				Role       string `json:"role"`
				ProviderID string `json:"providerID"`
				ModelID    string `json:"modelID"`
			} `json:"info"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &export); err != nil {
		return "", "", fmt.Errorf("the export is not the JSON this parser reads")
	}
	for i := len(export.Messages) - 1; i >= 0; i-- {
		if info := export.Messages[i].Info; info.Role == "assistant" {
			return info.ProviderID, info.ModelID, nil
		}
	}
	return "", "", nil
}

// descendants lists the subagent sessions under session, breadth first, at
// most maxDescendants of them. The session table of the run's own database
// is the one record of which session started which: the stream carries only
// the stage's own session, `session list` lists only root sessions, and a
// sanitized export redacts the task tool's metadata that names a child.
func (f openCodeFold) descendants(ctx context.Context, session string) ([]string, bool, error) {
	out, err := f.helper(ctx, "db", openCodeSessionTreeQuery, "--format", "json", "--pure")
	if err != nil {
		return nil, false, err
	}
	var rows []struct {
		ID       string `json:"id"`
		ParentID string `json:"parent_id"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, false, fmt.Errorf("the session list is not the JSON this parser reads")
	}
	truncated := len(rows) >= openCodeMaxSessionRows
	children := map[string][]string{}
	for _, row := range rows {
		if openCodeSessionIDRE.MatchString(row.ID) {
			children[row.ParentID] = append(children[row.ParentID], row.ID)
		}
	}
	var found []string
	seen := map[string]bool{session: true}
	queue := []string{session}
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		for _, id := range children[next] {
			if seen[id] {
				continue
			}
			if len(found) == f.maxDescendants {
				return found, true, nil
			}
			seen[id] = true
			found = append(found, id)
			queue = append(queue, id)
		}
	}
	return found, truncated, nil
}

// sessionUsage reads a session's info.tokens and info.cost from its sanitized
// export, and nothing else of it. Nothing of the export is kept.
func (f openCodeFold) sessionUsage(ctx context.Context, session string) (OpenCodeTokens, float64, error) {
	out, err := f.helper(ctx, openCodeExportArgs(session)...)
	if err != nil {
		return OpenCodeTokens{}, 0, err
	}
	var export struct {
		Info struct {
			Tokens *OpenCodeTokens `json:"tokens"`
			Cost   float64         `json:"cost"`
		} `json:"info"`
	}
	if err := json.Unmarshal(out, &export); err != nil || export.Info.Tokens == nil {
		return OpenCodeTokens{}, 0, fmt.Errorf("the export has no info.tokens")
	}
	return *export.Info.Tokens, export.Info.Cost, nil
}

// openCodeVersions caches `opencode --version` per resolved binary path and
// modification time, so a stage runs it only when the binary changed.
var openCodeVersions = struct {
	sync.Mutex
	m map[openCodeVersionKey]string
}{m: map[openCodeVersionKey]string{}}

type openCodeVersionKey struct {
	path  string
	mtime time.Time
}

// openCodeVersionRE is the shape of a version `opencode --version` prints.
var openCodeVersionRE = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*$`)

// version returns the binary's version, from the cache when the binary's
// resolved path and modification time are unchanged.
func (f openCodeFold) version(ctx context.Context) (string, error) {
	resolved, err := filepath.EvalSymlinks(f.bin)
	if err != nil {
		return "", fmt.Errorf("resolve the binary: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat the binary: %w", err)
	}
	key := openCodeVersionKey{path: resolved, mtime: info.ModTime()}
	openCodeVersions.Lock()
	cached, ok := openCodeVersions.m[key]
	openCodeVersions.Unlock()
	if ok {
		return cached, nil
	}
	out, err := f.helper(ctx, "--version")
	if err != nil {
		return "", err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	v := strings.TrimSpace(first)
	if !openCodeVersionRE.MatchString(v) {
		return "", fmt.Errorf("printed no version")
	}
	openCodeVersions.Lock()
	openCodeVersions.m[key] = v
	openCodeVersions.Unlock()
	return v, nil
}

// errHelperTimedOut is a helper that outlived its timeout and was killed.
var errHelperTimedOut = errors.New("timed out and was killed")

// helper runs one opencode process: in its own process group, which is
// killed whole when the timeout fires or the process exits leaving children;
// with stdin closed; with the fold's environment (openCodeHelperEnv) and
// directory (the run's root); its output read into memory, and capped.
//
// Its stdout is an unnamed temporary file, never a pipe (#2165). OpenCode
// prints an export or a query result with a single write and then calls
// process.exit(). Into a pipe, its runtime writes only what the pipe accepts
// at once (64 KiB on macOS) and drops the rest at that exit, so the export of
// any session longer than that arrived cut short and failed to parse, on
// 1.18.30 and 1.18.32 alike. A write to a regular file is complete before the
// process exits. The file is unlinked before the process starts, so nothing
// of the output outlives this call under any name, and nothing is written
// under the run's root.
func (f openCodeFold) helper(ctx context.Context, args ...string) ([]byte, error) {
	timeout := f.timeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("opencode %s: the fold's time budget is spent", args[0])
	}
	out, err := os.CreateTemp("", "nightgauge-opencode-fold-*")
	if err != nil {
		return nil, fmt.Errorf("opencode %s: create its output file: %w", args[0], err)
	}
	defer out.Close()
	if err := os.Remove(out.Name()); err != nil {
		return nil, fmt.Errorf("opencode %s: unlink its output file: %w", args[0], err)
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(hctx, f.bin, args...)
	cmd.Env = f.env
	cmd.Dir = f.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	cmd.Stdout = out
	err = cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	info, statErr := out.Stat()
	switch {
	case errors.Is(hctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("opencode %s %w after %s", args[0], errHelperTimedOut, timeout.Round(time.Millisecond))
	case statErr != nil:
		return nil, fmt.Errorf("opencode %s: read its output: %w", args[0], statErr)
	case info.Size() > openCodeHelperMaxOutput:
		return nil, fmt.Errorf("opencode %s printed more than %d bytes", args[0], openCodeHelperMaxOutput)
	case err != nil:
		return nil, fmt.Errorf("opencode %s: %w", args[0], err)
	}
	// The process shares the file's offset, so the output is read from its
	// start rather than from where the process left the offset.
	printed := make([]byte, info.Size())
	if _, err := out.ReadAt(printed, 0); err != nil {
		return nil, fmt.Errorf("opencode %s: read its output: %w", args[0], err)
	}
	return printed, nil
}
