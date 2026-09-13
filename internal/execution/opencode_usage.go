// opencode_usage.go completes an opencode stage's RunResult after the process
// exits (ADR-022 § 1-3, § 9, § 22): the usage of subagent sessions, which the
// stream never carries; the served model, which the stream does not name; the
// CLI's version; the markers for permissions OpenCode rejected on its own; and
// the redaction of credentials from every line the child prints.
//
// Every opencode process started here (--version, db, export) runs in its own
// process group under a timeout, in the stage's own environment, so it reads
// the run's per-run root and nothing of the operator's. A failure never fails
// the stage: it marks usage partial and leaves a drift marker.
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

// openCodeAutoRejectRE is the line opencode 1.18.30's run command prints to
// stderr when it rejects a permission request, for its own session and for
// every subagent session: `! permission requested: <permission> (<patterns>);
// auto-rejecting`. The patterns are the tool's input, so only the permission
// name is ever taken from the line.
var openCodeAutoRejectRE = regexp.MustCompile(`^!\s*permission requested: (\S+) \(.*\); auto-rejecting$`)

// openCodePermissionRE is the shape of an OpenCode permission name.
var openCodePermissionRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// OpenCodeAutoRejectMarker reads one stderr line. When it is OpenCode's
// auto-reject line it returns the classification marker for the rejected
// permission: PermissionRejectedMarker when allowedTools (Claude Code tool
// names, as RunOptions.AllowedTools holds them) grant it, and
// PermissionDeniedMarker otherwise. A line naming no recognizable permission
// yields "tool=unknown".
func OpenCodeAutoRejectMarker(line string, allowedTools []string) (string, bool) {
	permission, ok := openCodeRejectedPermission(line)
	if !ok {
		return "", false
	}
	return openCodeRejectionMarker(permission, adapters.OpenCodeToolsAllowed(allowedTools)), true
}

// openCodeRejectedPermission returns the permission an auto-reject line names,
// "unknown" when it names none this parser recognizes.
func openCodeRejectedPermission(line string) (string, bool) {
	plain := strings.TrimSpace(ansiEscapeRE.ReplaceAllString(line, ""))
	m := openCodeAutoRejectRE.FindStringSubmatch(plain)
	if m == nil {
		return "", false
	}
	if !openCodePermissionRE.MatchString(m[1]) {
		return "unknown", true
	}
	return m[1], true
}

func openCodeRejectionMarker(permission string, allowed map[string]bool) string {
	if allowed[permission] {
		return PermissionRejectedMarker + " tool=" + permission
	}
	return PermissionDeniedMarker + " tool=" + permission
}

// credentialPatterns are the credential shapes removed from every line an
// opencode child prints, after the values of the variables the adapter names
// (envValueRedactor), so a secret the child read from a file or inherited is
// removed too (ADR-022 § 22). Each shape is specific to credentials: a
// provider's key prefix, a forge token prefix, a bearer or authorization
// credential, a URL's user:password, a credential query parameter. No pattern
// matches a quote or a backslash, and no replacement holds one, so a JSON
// event stays valid JSON.
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
		regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{20,}|xai-[A-Za-z0-9_-]{20,}|AIza[0-9A-Za-z_-]{35}|(?:AKIA|ASIA)[0-9A-Z]{16}|gsk_[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{30,})`),
		"[REDACTED:api-key]"},
	// GitHub tokens of every kind, and GitLab personal access tokens.
	{[]string{"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-"},
		regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{22,}|glpat-[A-Za-z0-9_-]{20,})`),
		"[REDACTED:forge-token]"},
	// A bearer credential, and the credential of an Authorization header.
	{[]string{"bearer"}, regexp.MustCompile(`(?i)\b(bearer\s+)[A-Za-z0-9._~+/-]{16,}=*`), "${1}[REDACTED:bearer-token]"},
	{[]string{"authorization"},
		regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*(?:basic|token)\s+)[A-Za-z0-9._~+/-]{8,}=*`),
		"${1}[REDACTED:authorization]"},
	// user:password in a URL.
	{[]string{"://"}, regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9+.-]*://)[^\s/@:"'\\]+:[^\s/@"'\\]+@`), "${1}[REDACTED:userinfo]@"},
	// A credential in a query string.
	{[]string{"key=", "token=", "secret=", "passw", "pwd=", "sig=", "signature=", "credential="},
		regexp.MustCompile(`(?i)([?&](?:api[_-]?key|apikey|key|access[_-]?token|auth[_-]?token|token|client[_-]?secret|secret|password|passwd|pwd|sig|signature|x-amz-signature|x-amz-credential|x-amz-security-token|x-goog-signature|x-goog-credential)=)[^&#\s"'\\]+`),
		"${1}[REDACTED:query-credential]"},
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

// forEachLine calls onLine with every line r holds, without its line ending,
// the way bufio.Scanner splits lines. A line longer than limit bytes is not
// delivered: it is read to its end and discarded, and onOversize is called,
// so one oversized line costs a marker instead of ending the read and
// leaving the child blocked on a full pipe. The slice onLine receives is
// valid only until it returns.
func forEachLine(r io.Reader, limit int, onLine func([]byte), onOversize func()) error {
	br := bufio.NewReaderSize(r, 64*1024)
	var line []byte
	size, over := 0, false
	for {
		chunk, err := br.ReadSlice('\n')
		complete := err == nil
		data := chunk
		if complete {
			data = chunk[:len(chunk)-1]
		}
		size += len(data)
		if !over {
			if size > limit {
				over, line = true, line[:0]
			} else {
				line = append(line, data...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if complete || size > 0 {
			if over {
				onOversize()
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

// openCodeRun is the opencode half of one stage in Manager.RunStage: it
// watches the child's stderr for auto-reject lines and, once the child has
// exited, completes the RunResult.
type openCodeRun struct {
	stream  *OpenCodeStream
	allowed map[string]bool
	// markers are the classification markers, one per rejected permission,
	// in the order first seen. Only the stderr reader appends to it.
	markers []string
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

// observeStderr reads one redacted stderr line.
func (r *openCodeRun) observeStderr(line string) {
	permission, ok := openCodeRejectedPermission(line)
	if !ok {
		return
	}
	if permission == "unknown" {
		r.stream.Drift("an auto-reject line on stderr names no permission this parser recognizes")
	}
	marker := openCodeRejectionMarker(permission, r.allowed)
	for _, m := range r.markers {
		if m == marker {
			return
		}
	}
	r.markers = append(r.markers, marker)
}

// openCodeOutcome is what finish learned, applied to the RunResult.
type openCodeOutcome struct {
	exitedZero bool
	markers    []string
	version    string
	served     OpenCodeServedModel
	cost       float64
	partial    bool
	drift      []string
}

// finish runs once the child has exited, before the RunResult is built from
// acc: it folds the subagent sessions' usage into acc, reads the served
// model and the CLI version, and closes the stream's drift checks. bin, env
// and dir are the stage's resolved binary, environment and working
// directory; exitCode is -1 when the child did not exit on its own. ctx is
// the dispatch's own context, not the stage's: a stage that ran out of time
// still has its usage read, and a cancelled dispatch stops the fold.
func (r *openCodeRun) finish(ctx context.Context, bin string, env []string, dir string, exitCode int, acc *TokenAccumulator, dispatched string) openCodeOutcome {
	f := r.fold
	f.bin, f.env, f.dir = bin, env, dir
	res := f.run(ctx, r.stream, dispatched)
	acc.addOpenCodeTokens(res.children)
	if r.stream.RejectedToolCalls > 0 && len(r.markers) == 0 {
		r.stream.Drift("the stream shows a tool call OpenCode rejected, but stderr carried no auto-reject line")
	}
	r.stream.Finish(exitCode)
	return openCodeOutcome{
		exitedZero: exitCode == 0,
		markers:    r.markers,
		version:    res.version,
		served:     res.served,
		cost:       r.stream.ReportedCostUSD + res.childCost,
		partial:    res.partial,
		drift:      r.stream.DriftMarkers(),
	}
}

// apply writes the outcome onto the RunResult. A rejected permission ends
// the run on 1.18.30 even though the process exits 0, so an exit-0 run with
// one is reported as exit 1 (ADR-022 § 9), and its markers end Stderr, where
// failure classification reads the reason.
func (o openCodeOutcome) apply(result *adapters.RunResult) {
	result.ServedModel = o.served.Model
	result.ModelProvider = o.served.Provider
	result.UpstreamModel = o.served.Upstream
	result.AdapterVersion = o.version
	result.AdapterReportedCostUSD = o.cost
	result.UsagePartial = o.partial
	result.DriftMarkers = o.drift
	if len(o.markers) == 0 {
		return
	}
	if result.Stderr != "" && !strings.HasSuffix(result.Stderr, "\n") {
		result.Stderr += "\n"
	}
	result.Stderr += strings.Join(o.markers, "\n") + "\n"
	if o.exitedZero && result.ExitCode == 0 {
		result.ExitCode = 1
	}
}

// report logs the drift markers and streams every marker to the stage's
// output, so an operator watching the stage sees them.
func (o openCodeOutcome) report(opts StageOptions) {
	for _, m := range o.drift {
		fmt.Fprintf(os.Stderr, "%s %s#%d %s: %s\n", OpenCodeDriftMarker, opts.Repo, opts.IssueNumber, opts.Stage,
			strings.TrimPrefix(m, OpenCodeDriftMarker+" "))
	}
	if opts.Streamer == nil {
		return
	}
	for _, m := range append(append([]string{}, o.drift...), o.markers...) {
		opts.Streamer.OnOutput("stderr", []byte(m+"\n"))
	}
}

// OpenCodeServedModel is the model that served an opencode stage, recorded as
// ADR-022 § 1 and § 2 decide.
type OpenCodeServedModel struct {
	// Provider is the normalized provider: "lm-studio" for key lmstudio,
	// "other" for a key Nightgauge does not recognize.
	Provider string
	// Model is the recorded model: a registry model's bare id, any other
	// model as "<provider>/<id>", and an "other" model as Upstream.
	Model string
	// Upstream is the provider-qualified id as OpenCode names it.
	Upstream string
}

// ResolveOpenCodeServedModel records the model that served a stage. The
// stream names no model, so it comes from the session export's assistant
// message (providerID and modelID) when there is one, and otherwise from the
// model the stage was dispatched with (-m).
func ResolveOpenCodeServedModel(providerID, modelID, dispatched string) OpenCodeServedModel {
	raw := dispatched
	if providerID != "" && modelID != "" {
		raw = providerID + "/" + modelID
	}
	if raw == "" {
		return OpenCodeServedModel{}
	}
	provider, bareID, upstream := models.ParseOpenCodeModel(raw)
	served := OpenCodeServedModel{Provider: provider, Model: raw, Upstream: upstream}
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

// servedModel reads the provider and model of the session's last assistant
// message from its sanitized export. Nothing else of the export is read.
func (f openCodeFold) servedModel(ctx context.Context, session string) (string, string, error) {
	out, err := f.helper(ctx, "export", session, "--sanitize")
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
	out, err := f.helper(ctx, "db", openCodeSessionTreeQuery, "--format", "json")
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
// export, and nothing else of it. The export is held in memory only.
func (f openCodeFold) sessionUsage(ctx context.Context, session string) (OpenCodeTokens, float64, error) {
	out, err := f.helper(ctx, "export", session, "--sanitize")
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
// with stdin closed; in the stage's environment and directory; its output
// held in memory only, and capped.
func (f openCodeFold) helper(ctx context.Context, args ...string) ([]byte, error) {
	timeout := f.timeout
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < timeout {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("opencode %s: the fold's time budget is spent", args[0])
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(hctx, f.bin, args...)
	cmd.Env = f.env
	cmd.Dir = f.dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out := &cappedBuffer{max: openCodeHelperMaxOutput}
	cmd.Stdout = out
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	switch {
	case errors.Is(hctx.Err(), context.DeadlineExceeded):
		return nil, fmt.Errorf("opencode %s %w after %s", args[0], errHelperTimedOut, timeout.Round(time.Millisecond))
	case out.overflow:
		return nil, fmt.Errorf("opencode %s printed more than %d bytes", args[0], out.max)
	case err != nil:
		return nil, fmt.Errorf("opencode %s: %w", args[0], err)
	}
	return out.Bytes(), nil
}

// cappedBuffer keeps at most max bytes and notes that more arrived.
type cappedBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
