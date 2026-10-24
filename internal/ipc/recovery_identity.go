package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Recovery rejection codes. Like the run-identity codes they lead the error
// message, because the transport collapses a handler error into one string.
const (
	codeRecoveryInvalid         = "recovery_request_invalid"
	codeRecoveryClaimHeld       = "claim_held"
	codeRecoverySnapshotChanged = "snapshot_changed"
	codeRecoveryNoClaim         = "claim_invalid"
	codeRecoveryTampered        = "journal_tampered"
	codeRecoveryStepFailed      = "recovery_step_failed"
)

// Recovery actions a transaction can execute.
const (
	RecoveryActionRestart = "restart"
	RecoveryActionDiscard = "discard"
	RecoveryActionResume  = "resume"
	RecoveryActionExecute = "execute"
)

// recoveryRunIDRegexp is deliberately narrower than "anything a filename can
// hold": a run id selects a snapshot file and a journal file, and it arrives on
// a socket ADR-015 documents as unauthenticated.
var recoveryRunIDRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var (
	recoverySHARegexp    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	recoveryRepoRegexp   = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	recoveryBranchRegexp = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	recoveryStageRegexp  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
)

// fsOps is the filesystem surface path containment needs, injectable so tests
// can prove that hostile values are refused before any filesystem call.
type fsOps interface {
	EvalSymlinks(path string) (string, error)
	Lstat(path string) (os.FileInfo, error)
}

type osFS struct{}

func (osFS) EvalSymlinks(p string) (string, error) { return filepath.EvalSymlinks(p) }
func (osFS) Lstat(p string) (os.FileInfo, error)   { return os.Lstat(p) }

// validateRecoveryRequest is the lexical gate: it performs no filesystem call.
func validateRecoveryRequest(r RecoveryRequest) error {
	bad := func(field, why string) error {
		return fmt.Errorf("%s: %s is %s", codeRecoveryInvalid, field, why)
	}
	if !recoveryRepoRegexp.MatchString(r.Repo) || strings.Contains(r.Repo, "..") {
		return bad("repo", "not an owner/name slug")
	}
	if r.Issue <= 0 {
		return bad("issue", "not a positive issue number")
	}
	if !recoveryRunIDRegexp.MatchString(r.RunID) || strings.Contains(r.RunID, "..") {
		return bad("runId", "not a canonical run identity")
	}
	if !recoveryBranchRegexp.MatchString(r.Branch) || strings.Contains(r.Branch, "..") ||
		strings.HasSuffix(r.Branch, "/") || strings.HasSuffix(r.Branch, ".lock") {
		return bad("branch", "not a valid branch name")
	}
	if !recoveryStageRegexp.MatchString(r.Stage) {
		return bad("stage", "not a stage name")
	}
	if r.Phase != "" && !recoveryStageRegexp.MatchString(r.Phase) {
		return bad("phase", "not a phase name")
	}
	if !recoverySHARegexp.MatchString(r.SnapshotSHA256) {
		return bad("snapshotSha256", "not a lowercase hex SHA-256")
	}
	if r.Worktree != "" {
		if err := lexicalPathCheck(r.Worktree); err != nil {
			return bad("worktree", err.Error())
		}
	}
	return nil
}

// lexicalPathCheck refuses relative paths and any ".." component before the
// path touches the filesystem.
func lexicalPathCheck(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("not an absolute path")
	}
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return fmt.Errorf("contains a parent-directory component")
		}
	}
	if filepath.Clean(p) != p {
		return fmt.Errorf("not canonical")
	}
	return nil
}

// containedPath resolves p through fs and requires the result to be a strict
// descendant of one of roots (each resolved the same way). The lexical check
// runs first, so a relative or ".." path costs zero filesystem calls.
func containedPath(fs fsOps, p string, roots []string) (string, error) {
	if err := lexicalPathCheck(p); err != nil {
		return "", fmt.Errorf("%s: path %q: %v", codeRecoveryInvalid, p, err)
	}
	resolved, err := fs.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("%s: resolve %q: %v", codeRecoveryInvalid, p, err)
	}
	for _, root := range roots {
		rr, err := fs.EvalSymlinks(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rr, resolved)
		if err != nil || rel == "." || rel == ".." ||
			strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			continue
		}
		return resolved, nil
	}
	return "", fmt.Errorf("%s: path %q resolves to %q, outside the approved roots", codeRecoveryInvalid, p, resolved)
}
