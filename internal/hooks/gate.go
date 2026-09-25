package hooks

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

// GateDecision is the result of evaluating a workflow gate.
type GateDecision struct {
	Decision string `json:"decision"` // "allow" or "block"
	Reason   string `json:"reason,omitempty"`
}

// GateInput represents the JSON input from Claude Code's PreToolUse hook.
type GateInput struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
	Cwd       string          `json:"cwd,omitempty"`
}

// BashToolInput is the parsed tool_input for Bash tool calls.
type BashToolInput struct {
	Command string `json:"command"`
}

// FileToolInput is the parsed tool_input for Edit/Write tool calls.
type FileToolInput struct {
	FilePath string `json:"file_path"`
}

// Allow returns an allow decision.
func Allow() GateDecision {
	return GateDecision{Decision: "allow"}
}

// Block returns a block decision with the given reason.
func Block(reason string) GateDecision {
	return GateDecision{Decision: "block", Reason: reason}
}

// EvaluateGate evaluates the workflow gate for a given tool use.
// It parses the JSON input and applies all gate rules.
// The mode parameter controls sanitization enforcement:
//   - "warn": log pattern matches but allow through
//   - "block": block pattern matches (legacy default)
//   - "disabled": skip sanitization pattern checks entirely
//
// EvaluateGate applies the shipped push-gate defaults: no protected branches
// and no force-push block, so it blocks no push. Use EvaluateGateWithPushGate
// to apply a repository's hooks.push_gate configuration.
func EvaluateGate(inputJSON []byte, mode config.SanitizationMode) GateDecision {
	return EvaluateGateWithPushGate(inputJSON, mode, nil)
}

// EvaluateGateWithPushGate is EvaluateGate with the repository's
// hooks.push_gate policy (#2124). A nil policy is the shipped default: the
// hook blocks no push and repository rulesets decide.
func EvaluateGateWithPushGate(inputJSON []byte, mode config.SanitizationMode, pushGate *config.PushGateConfig) GateDecision {
	var input GateInput
	if err := json.Unmarshal(inputJSON, &input); err != nil {
		// If we can't parse input, allow through (fail open)
		return Allow()
	}

	switch input.ToolName {
	case "Bash":
		return evaluateBashGate(input, mode, pushGate)
	case "Edit", "Write":
		return evaluateFileGate(input.ToolInput)
	default:
		return Allow()
	}
}

// skipWorkflowGateEnv, when set to "1", short-circuits the operation-classification
// gates (configurable push gate, destructive-git) and the sanitization scan.
// It is an explicit, text-free escape hatch (issue #4069 AC: "an override path
// that does NOT require rewording human-readable text") for the rare case where
// the parser still misclassifies a legitimate command. Secret read/write and
// pre-push validation gates remain ALWAYS on — those have no prose-false-positive
// problem and silencing them would hide real risk. Developer/manual use only:
// it MUST NOT be set in skillRunner/orchestrator environments.
const skipWorkflowGateEnv = "NIGHTGAUGE_SKIP_WORKFLOW_GATE"

// evaluateBashGate applies all bash command gates. As of #4069 the git-operation
// gates parse the command's real argv (per pipeline segment) rather than
// substring-matching the raw command string, so words inside echoes, commit
// messages, `--body` payloads, and heredocs no longer trigger false positives.
func evaluateBashGate(input GateInput, mode config.SanitizationMode, pushGate *config.PushGateConfig) GateDecision {
	var toolInput BashToolInput
	if err := json.Unmarshal(input.ToolInput, &toolInput); err != nil || toolInput.Command == "" {
		return Allow()
	}

	cmd := toolInput.Command
	// Expand command wrappers (bash -c, sudo, env, xargs, …) so a dangerous
	// command hidden behind one is surfaced and classified, not bypassed.
	segments := ExpandWrappers(SplitSegments(cmd))
	skipOpGates := os.Getenv(skipWorkflowGateEnv) == "1"

	if !skipOpGates {
		// Gate 1: configurable push gate (hooks.push_gate, #2124). Off by
		// default: push policy belongs to server-side rulesets, and this gate
		// is an opt-in local fallback for repositories without one.
		if reason := pushGateReason(segments, gateCwd(input.Cwd), pushGate); reason != "" && !isModelUpdatePush(cmd) {
			return Block(reason)
		}

		// Gate 3: Block destructive git operations
		if reason := destructiveGitReason(segments); reason != "" {
			return Block("Destructive git operation blocked (" + reason + "). This could result in data loss.")
		}
	}

	// Gate 4: Block reading secrets via cat/less/more/head/tail
	if isSecretRead(cmd) {
		return Block("Reading sensitive files via shell blocked. Use proper secrets management.")
	}

	// Gate 5: Block writing to sensitive files via echo/printf
	if isSecretWrite(cmd) {
		return Block("Writing to sensitive files via shell blocked. Use proper secrets management.")
	}

	// Gate 6: Output sanitization — check destructive/exfiltration/escalation/traversal patterns.
	// Scans the quoted-stripped command (heredoc bodies + quoted spans removed) so
	// destructive regexes match real operations, never echoed/heredoc/commit-message prose.
	// Respects sanitization mode: disabled skips, warn allows + logs, block blocks.
	if !skipOpGates && mode != config.SanitizationModeDisabled {
		if match := sanitizeMatch(segments); match != nil {
			switch mode {
			case config.SanitizationModeWarn:
				logWarnEvent(match, cmd)
				return Allow()
			default: // block
				return Block(fmt.Sprintf("Command blocked by sanitization (%s): %s", match.Category, match.Pattern))
			}
		}
	}

	// Gate 7: Pre-push validation gate — check context file for pipeline branches.
	// If a pre-push context file exists with a failed status, block the push.
	// If no context file exists, allow (feature-validate Phase 2.7 handles validation).
	if isGitPush(segments) {
		if decision := checkPrePushContext(); decision.Decision == "block" {
			return decision
		}
	}

	return Allow()
}

// sanitizeMatch runs the sanitization regexes against each EXPANDED segment
// individually: a segment's quoted spans are stripped so prose inside an echo /
// commit message / heredoc cannot match, while wrapper-expanded inner commands
// (e.g. `bash -c "rm -rf /"`) are surfaced as real operative text. Scanning per
// segment (not a joined string) preserves end-of-string anchors like the
// `rm -rf /$` pattern that a segment-boundary newline would otherwise break.
func sanitizeMatch(segments []Segment) *PatternMatch {
	for _, seg := range segments {
		text := stripQuotedSpans(seg.Raw)
		if match := MatchPatterns(text,
			CategoryDestructive,
			CategoryExfiltration,
			CategoryPrivilegeEscalation,
			CategoryPathTraversal,
		); match != nil {
			return match
		}
	}
	return nil
}

// logWarnEvent writes a single NDJSON line to .nightgauge/logs/sanitization.log.
func logWarnEvent(match *PatternMatch, command string) {
	logDir := ".nightgauge/logs"
	_ = os.MkdirAll(logDir, 0o755)

	logPath := filepath.Join(logDir, "sanitization.log")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warn: failed to open sanitization log: %v\n", err)
		return
	}
	defer f.Close()

	entry := map[string]string{
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"event":     "warned",
		"category":  string(match.Category),
		"pattern":   match.Pattern,
		"command":   command,
	}
	data, _ := json.Marshal(entry)
	fmt.Fprintf(f, "%s\n", data)
}

// evaluateFileGate applies file edit/write gates.
func evaluateFileGate(rawInput json.RawMessage) GateDecision {
	var toolInput FileToolInput
	if err := json.Unmarshal(rawInput, &toolInput); err != nil || toolInput.FilePath == "" {
		return Allow()
	}

	filePath := toolInput.FilePath
	filename := filepath.Base(filePath)

	// Gate 1: Protect sensitive files
	if IsSensitiveFile(filename) {
		return Block(fmt.Sprintf("Modifying sensitive file '%s' blocked. Use proper secrets management.", filename))
	}

	// Gate 2: Protect git internals
	if strings.Contains(filePath, ".git/") || strings.Contains(filePath, ".git\\") {
		return Block("Modifying .git internals blocked.")
	}

	return Allow()
}

// gitArgvs returns a normalized argv for every segment that is a `git`
// invocation (after stripping leading env assignments). `gh` and other commands
// are excluded, so `gh pr create --base main` never yields a git argv. Each
// returned argv is normalized to `[git, <verb>, <args…>]` by skipping git's
// leading global options (`-C dir`, `-c k=v`, `--no-pager`, `--git-dir=…`, …) so
// the verb is always at index 1 regardless of global flags.
func gitArgvs(segments []Segment) [][]string {
	var out [][]string
	for _, seg := range segments {
		argv := seg.CommandArgv()
		if len(argv) > 0 && baseName(argv[0]) == "git" {
			out = append(out, normalizeGitArgv(argv))
		}
	}
	return out
}

// gitGlobalValueOpts are git global options that consume a following value when
// given in space form (e.g. `git -C /repo push`).
var gitGlobalValueOpts = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--super-prefix": true, "--config-env": true,
}

// normalizeGitArgv returns [git, verb, args…] by skipping git's leading global
// options so the subcommand is always at index 1. Without this, `git -C dir push
// origin main` would leave argv[1]=="-C" and defeat every git-operation gate.
func normalizeGitArgv(argv []string) []string {
	i := 1
	for i < len(argv) {
		a := argv[i]
		if !strings.HasPrefix(a, "-") {
			break // first non-flag token is the subcommand
		}
		if gitGlobalValueOpts[a] && !strings.Contains(a, "=") {
			i += 2 // option + its value (space form)
			continue
		}
		i++ // boolean option or =form (value attached)
	}
	if i >= len(argv) {
		return []string{"git"}
	}
	return append([]string{"git"}, argv[i:]...)
}

// gitPushArgs returns the positional refspec arguments of a `git push` argv
// (everything after `push`, with flags and the leading remote name removed), and
// whether the segment is a git push at all. The remote name is the first
// positional; refspecs follow.
func gitPushArgs(argv []string) (refs []string, isPush bool) {
	if len(argv) < 2 || argv[1] != "push" {
		return nil, false
	}
	var positionals []string
	for _, a := range argv[2:] {
		if strings.HasPrefix(a, "-") {
			continue // flag
		}
		positionals = append(positionals, a)
	}
	if len(positionals) > 0 {
		positionals = positionals[1:] // drop the remote name
	}
	return positionals, true
}

// baseName returns the final path component of a command word so `/usr/bin/git`
// is recognised as `git`.
func baseName(cmd string) string {
	if i := strings.LastIndexByte(cmd, '/'); i >= 0 {
		return cmd[i+1:]
	}
	return cmd
}

// pushGateReason applies hooks.push_gate to every real `git push` among the
// segments and returns a block reason, or "" to allow. With a nil or empty
// policy it always allows.
//
// Semantics:
//   - protected_branches: a push whose refspec names a protected branch is
//     blocked, forced or not. A forced push with no refspec (or a bare `HEAD`
//     refspec) is blocked when the current branch or its upstream is
//     protected. A forced `--all`/`--branches`/`--mirror` is blocked because
//     its targets cannot be resolved (fail closed).
//   - block_force_push: with protected_branches set, it additionally blocks
//     every forced push to a protected branch (already implied above, so it
//     changes nothing there); with protected_branches empty it blocks every
//     forced push to any branch.
//
// The working directory starts at cwd and follows `cd X` segments and
// `git -C X`, so the current branch is read from the repository the push
// actually runs in.
func pushGateReason(segments []Segment, cwd string, policy *config.PushGateConfig) string {
	if policy == nil {
		return ""
	}
	protected := policy.ProtectedBranches
	if len(protected) == 0 && !policy.BlockForcePush {
		return ""
	}
	blockAllForced := policy.BlockForcePush && len(protected) == 0
	dir := cwd
	for _, seg := range segments {
		argv := seg.CommandArgv()
		if len(argv) == 0 {
			continue
		}
		if argv[0] == "cd" {
			if len(argv) > 1 {
				dir = joinDir(dir, argv[1])
			}
			continue
		}
		if baseName(argv[0]) != "git" {
			continue
		}
		gitDir := gitCOption(argv, dir)
		norm := normalizeGitArgv(argv)
		refs, isPush := gitPushArgs(norm)
		if !isPush {
			continue
		}
		forced := pushIsForced(norm[2:])
		if forced && blockAllForced {
			return "Force push blocked by hooks.push_gate.block_force_push."
		}
		if len(protected) == 0 {
			continue
		}
		for _, f := range refs {
			if b := refTargetsProtected(f, protected); b != "" {
				return protectedReason(b)
			}
			if forced && strings.TrimPrefix(f, "+") == "HEAD" {
				if b := currentBranchProtected(gitDir, protected); b != "" {
					return protectedReason(b)
				}
			}
		}
		if forced && hasAnyLongFlag(norm[2:], "--all", "--branches", "--mirror") {
			return "Forced --all/--branches/--mirror push blocked: hooks.push_gate.protected_branches is set and the targets cannot be resolved."
		}
		if forced && len(refs) == 0 {
			if b := currentBranchProtected(gitDir, protected); b != "" {
				return protectedReason(b)
			}
		}
	}
	return ""
}

func protectedReason(branch string) string {
	return "Direct push to protected branch '" + branch + "' blocked by hooks.push_gate.protected_branches. Use the PR workflow with /nightgauge:pr-create."
}

// pushIsForced reports whether `git push` args force the update: -f/--force,
// --force-with-lease[=…], --force-if-includes, or a leading-`+` refspec.
func pushIsForced(args []string) bool {
	for _, a := range args {
		switch {
		case a == "--force", a == "--force-if-includes", strings.HasPrefix(a, "--force-with-lease"):
			return true
		case isShortFlagCluster(a) && strings.ContainsRune(shortCluster(a), 'f'):
			return true
		case strings.HasPrefix(a, "+") && !strings.HasPrefix(a, "++"):
			return true
		}
	}
	return false
}

func hasAnyLongFlag(args []string, flags ...string) bool {
	for _, f := range flags {
		if hasLongFlag(args, f) {
			return true
		}
	}
	return false
}

// gitCOption applies every leading `git -C <path>` to dir, as git does.
func gitCOption(argv []string, dir string) string {
	for i := 1; i < len(argv) && strings.HasPrefix(argv[i], "-"); i++ {
		if argv[i] == "-C" && i+1 < len(argv) {
			dir = joinDir(dir, argv[i+1])
			i++
		} else if gitGlobalValueOpts[argv[i]] {
			i++
		}
	}
	return dir
}

func joinDir(base, p string) string {
	if filepath.IsAbs(p) || base == "" {
		return p
	}
	return filepath.Join(base, p)
}

// gateCwd is the directory the hooked command runs in: the hook payload's cwd,
// or this process's working directory when the payload has none.
func gateCwd(cwd string) string {
	if cwd != "" {
		return cwd
	}
	wd, _ := os.Getwd()
	return wd
}

// currentBranchProtected returns the protected branch that dir's checked-out
// branch, or the branch its upstream merges into, names; "" when neither is
// protected. A detached HEAD or a directory outside a repository has no
// current branch, so it cannot push one implicitly.
func currentBranchProtected(dir string, protected []string) string {
	branch := gitOutput(dir, "symbolic-ref", "--short", "-q", "HEAD")
	if branch == "" {
		return ""
	}
	if b := refTargetsProtected(branch, protected); b != "" {
		return b
	}
	merge := gitOutput(dir, "config", "--get", "branch."+branch+".merge")
	if merge == "" {
		return ""
	}
	return refTargetsProtected(merge, protected)
}

func gitOutput(dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// refTargetsProtected returns the protected branch a push refspec resolves
// to, or "". It handles a bare `main`/`+main`, the destination side of a
// `src:dst` refspec, and fully-qualified `refs/heads/<b>` forms
// (`HEAD:refs/heads/main`, `+refs/heads/main`). Matching is case-insensitive.
func refTargetsProtected(f string, protected []string) string {
	target := f
	if i := strings.Index(target, ":"); i >= 0 {
		target = target[i+1:] // destination side of src:dst
	}
	target = strings.TrimPrefix(target, "+")
	target = strings.TrimPrefix(target, "refs/heads/")
	for _, p := range protected {
		p = strings.TrimPrefix(strings.TrimSpace(p), "refs/heads/")
		if p != "" && strings.EqualFold(target, p) {
			return p
		}
	}
	return ""
}

// isModelUpdatePush checks if a push is a legitimate complexity model update.
// Only whitelists "git push origin HEAD" pattern.
func isModelUpdatePush(cmd string) bool {
	// This is a simplified check — the full check also verifies last commit message
	// and changed files, but those require git access. The CLI version delegates
	// that check to the caller or skips it for now.
	return false
}

// destructiveGitReason returns a short reason naming the destructive git
// operation if a segment performs one, or "" otherwise. It matches on the actual
// verb + flags (not substrings), so destructive words inside prose never trip it.
func destructiveGitReason(segments []Segment) string {
	for _, argv := range gitArgvs(segments) {
		if len(argv) < 2 {
			continue
		}
		verb := argv[1]
		rest := argv[2:]
		switch verb {
		case "reset":
			if hasLongFlag(rest, "--hard") {
				return "git reset --hard"
			}
		case "clean":
			if hasForceFlag(rest) {
				return "git clean -f"
			}
		case "checkout", "restore":
			for _, a := range rest {
				if a == "." {
					return "git " + verb + " ."
				}
			}
		case "branch":
			if hasShortLetter(rest, 'D') || (hasLongFlag(rest, "--delete") && hasForceFlag(rest)) {
				return "git branch -D"
			}
		case "worktree":
			if len(rest) > 0 && rest[0] == "remove" && hasForceFlag(rest[1:]) {
				return "git worktree remove --force"
			}
		case "update-ref":
			if hasShortLetter(rest, 'd') || hasLongFlag(rest, "--delete") {
				return "git update-ref -d"
			}
		}
	}
	return ""
}

// isShortFlagCluster reports whether a word is a short-flag cluster like `-f` or
// `-fd` (single leading dash, not a `--long` flag).
func isShortFlagCluster(a string) bool {
	return strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && len(a) > 1
}

// shortCluster returns the letters of a short-flag cluster (without the leading
// dash), stopping at any `=value` suffix.
func shortCluster(a string) string {
	c := strings.TrimPrefix(a, "-")
	if i := strings.IndexByte(c, '='); i >= 0 {
		c = c[:i]
	}
	return c
}

// hasForceFlag reports whether args contain --force or a short cluster with `f`.
func hasForceFlag(args []string) bool {
	for _, a := range args {
		if a == "--force" {
			return true
		}
		if isShortFlagCluster(a) && strings.ContainsRune(shortCluster(a), 'f') {
			return true
		}
	}
	return false
}

// hasShortLetter reports whether args contain a short-flag cluster with the
// given letter (case-sensitive, e.g. 'D' for `branch -D`).
func hasShortLetter(args []string, letter rune) bool {
	for _, a := range args {
		if isShortFlagCluster(a) && strings.ContainsRune(shortCluster(a), letter) {
			return true
		}
	}
	return false
}

// hasLongFlag reports whether args contain the exact long flag (e.g. --hard),
// accepting a `--flag=value` form too.
func hasLongFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag || strings.HasPrefix(a, flag+"=") {
			return true
		}
	}
	return false
}

// isGitPush detects any `git push` invocation among the segments.
func isGitPush(segments []Segment) bool {
	for _, argv := range gitArgvs(segments) {
		if len(argv) >= 2 && argv[1] == "push" {
			return true
		}
	}
	return false
}

// isSecretRead detects reading sensitive files via cat/less/more/head/tail.
func isSecretRead(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return false
	}

	readCmds := map[string]bool{"cat": true, "less": true, "more": true, "head": true, "tail": true}
	if !readCmds[fields[0]] {
		return false
	}

	secretExts := []string{".env", ".pem", ".key", ".secret"}
	for _, f := range fields[1:] {
		lower := strings.ToLower(f)
		for _, ext := range secretExts {
			if strings.HasSuffix(lower, ext) || strings.Contains(lower, ext+".") {
				return true
			}
		}
	}
	return false
}

// checkPrePushContext reads the current branch, extracts the issue number,
// and checks if a pre-push context file exists with a failed status.
// Returns Allow() if no context file exists or if it passed.
// Returns Block() only if the context file exists and has failed status.
func checkPrePushContext() GateDecision {
	workDir, err := os.Getwd()
	if err != nil {
		return Allow()
	}

	// Read branch from .git/HEAD (no subprocess — testable and fast)
	branch, err := readCurrentBranch(workDir)
	if err != nil {
		return Allow() // Can't determine branch — allow
	}

	issueNum := extractIssueFromBranch(branch)
	if issueNum == 0 {
		return Allow() // Not a pipeline branch — allow
	}

	ctx := ReadPrePushContext(workDir, issueNum)
	if ctx == nil {
		return Allow() // No context file — allow (feature-validate will handle)
	}

	if ctx.OverallStatus == "passed" {
		return Allow()
	}

	return Block(fmt.Sprintf("Pre-push validation failed (status: %s). Fix issues before pushing. See .nightgauge/pipeline/pre-push-%d.json for details.", ctx.OverallStatus, issueNum))
}

// readCurrentBranch reads the current branch name from .git/HEAD without spawning a subprocess.
// Returns the branch name (e.g. "feat/42-test") or an error if HEAD is detached or unreadable.
func readCurrentBranch(workDir string) (string, error) {
	headPath := filepath.Join(workDir, ".git", "HEAD")
	data, err := os.ReadFile(headPath)
	if err != nil {
		return "", err
	}
	head := strings.TrimSpace(string(data))
	const refPrefix = "ref: refs/heads/"
	if !strings.HasPrefix(head, refPrefix) {
		return "", fmt.Errorf("detached HEAD")
	}
	return strings.TrimPrefix(head, refPrefix), nil
}

// isSecretWrite detects writing to sensitive files via echo/printf.
func isSecretWrite(cmd string) bool {
	lower := strings.ToLower(cmd)
	if !strings.HasPrefix(lower, "echo") && !strings.HasPrefix(lower, "printf") {
		return false
	}

	if !strings.Contains(cmd, ">") {
		return false
	}

	secretExts := []string{".env", ".pem", ".key", ".secret"}
	// Check the part after > for sensitive file extensions
	parts := strings.SplitN(cmd, ">", 2)
	if len(parts) < 2 {
		return false
	}
	target := strings.TrimSpace(parts[1])
	target = strings.TrimPrefix(target, ">") // handle >>
	target = strings.TrimSpace(target)

	lower = strings.ToLower(target)
	for _, ext := range secretExts {
		if strings.HasSuffix(lower, ext) || strings.Contains(lower, ext+".") {
			return true
		}
	}
	return false
}
