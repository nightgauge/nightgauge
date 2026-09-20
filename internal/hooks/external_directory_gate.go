package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/opencodeallow"
)

// externalDirectoryGateInput is the PreToolUse payload this gate needs, the
// same Claude-shaped envelope every gate in this package reads.
type externalDirectoryGateInput struct {
	ToolName  string          `json:"tool_name"`
	CWD       string          `json:"cwd"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// externalDirectoryGateFileInput is tool_input's shape for a Read call.
type externalDirectoryGateFileInput struct {
	FilePath string `json:"file_path"`
}

// EvaluateExternalDirectoryGate resolves symlinks and refuses a Read whose
// resolved directory falls outside the worktree and outside every
// OpenCode external_directory allow-listed root (#1816). It exists because
// opencode 1.18.30's own permission map matches a tool call's filePath
// lexically, never resolving symlinks at match time (ADR-022's "the
// permission map's pattern matching" amendment): a symlink planted inside an
// allow-listed directory AFTER the config is written, pointing outside every
// allow-listed root, is matched on its own lexical path and the read
// completes. This gate runs at dispatch time, when the actual filesystem
// (and any symlink a stage planted during its own run) is available, which a
// one-time, config-generation-time resolution (openCodeDirPatterns) cannot
// be.
//
// Fails open (Allow) on a parse error, a non-Read tool, or an empty
// file_path — matching every other gate's contract: a gate that cannot
// evaluate must not wedge the session. Fails CLOSED (Block) on every
// containment ambiguity past that point, per ADR-022's existing "any
// ambiguity blocks" convention for this plugin's gates.
func EvaluateExternalDirectoryGate(inputJSON []byte) GateDecision {
	var in externalDirectoryGateInput
	if err := json.Unmarshal(inputJSON, &in); err != nil {
		return Allow()
	}
	if in.ToolName != "Read" {
		return Allow()
	}

	var ti externalDirectoryGateFileInput
	if err := json.Unmarshal(in.ToolInput, &ti); err != nil || ti.FilePath == "" {
		return Allow()
	}

	worktree := in.CWD
	if worktree == "" {
		if wd, err := os.Getwd(); err == nil {
			worktree = wd
		}
	}

	resolvedDir := externalDirectoryGateResolveDir(ti.FilePath)

	if externalDirectoryGateContained(worktree, resolvedDir) {
		return Allow()
	}
	for _, root := range opencodeallow.RootsFromEnv(worktree) {
		if externalDirectoryGateContained(root, resolvedDir) {
			return Allow()
		}
	}

	return Block("external_directory: " + resolvedDir + " is outside the worktree and every allow-listed directory")
}

// externalDirectoryGateResolveDir resolves file's real containing directory,
// following a symlink anywhere in the path INCLUDING file's own last
// component — the case a config-generation-time-only resolution
// (openCodeDirPatterns, applied once when the permission map is built) can
// never catch for a symlink planted after that config was written. Falls
// back to the unresolved directory when resolution fails (a broken link, or
// a path that does not yet exist): the resolve step fails open — it never
// blocks just because EvalSymlinks errored — while the containment decision
// that follows still runs normally against whatever directory this returns,
// so "cannot resolve" is never silently read as "assume safe" either.
func externalDirectoryGateResolveDir(file string) string {
	if r, err := filepath.EvalSymlinks(file); err == nil {
		return filepath.Dir(r)
	}
	dir := filepath.Dir(file)
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		return r
	}
	return dir
}

// externalDirectoryGateContained reports whether dir is root itself or lies
// under it, checking root's own given and symlink-resolved form (the same
// given/resolved dual-form reasoning openCodeDirPatterns and
// openCodeInsideWorktree already use — /tmp -> /private/tmp on macOS, for
// instance). dir itself is always the single, already fully-resolved form
// externalDirectoryGateResolveDir produced: trying an unresolved form of dir
// here too would reopen exactly the lexical-match hole this gate exists to
// close — a symlink's own (unresolved) directory can sit inside the
// worktree even when what it points to does not.
func externalDirectoryGateContained(root, dir string) bool {
	if root == "" || dir == "" {
		return false
	}
	rootForms := []string{filepath.Clean(root)}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		if c := filepath.Clean(r); c != rootForms[0] {
			rootForms = append(rootForms, c)
		}
	}
	d := filepath.Clean(dir)
	for _, r := range rootForms {
		if d == r || strings.HasPrefix(d, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
