package tools

import (
	"context"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// The file tools serve what every participant of the conversation could
// read. A reply goes to the whole conversation, and the turn command names
// none of its participants, so every turn, the host operator's own included,
// is served only the files git tracks in the checkout of a repository the
// turn command lists as readable: the rule the run tools follow (#2472). An
// untracked or ignored file (a local .env, a credential, .nightgauge runtime
// state, a nested clone of another repository) is the operator's alone, and
// no turn is shown it, names it in a listing or matches it in a search.

// trackedListTimeout bounds one checkout's git ls-files.
const trackedListTimeout = 30 * time.Second

var (
	// errNotServed is every refusal that could otherwise tell an excluded
	// file that exists from one that does not: a missing path, an untracked
	// or ignored one, and anything a symlink leads to that is not served.
	errNotServed = fail(CodeNotFound, "no file this conversation may read is at that path: the file "+
		"tools serve only the files git tracks in a repository the turn may read")
	errUntrackable = fail(CodeNotFound, "git could not list that repository's tracked files on this "+
		"host, so none of its files is served")
)

// trackedFiles is one checkout's tracked paths as git's index lists them,
// slash-separated and relative to the checkout, and every directory holding
// one. err is set when git could not list them: then none is served.
type trackedFiles struct {
	files map[string]bool
	dirs  map[string]bool
	err   error
}

// listTracked lists the files git tracks in the checkout at dir. It reads
// the index and nothing else: ls-files neither refreshes the index nor runs
// a hook, and the variables that would point git at another repository are
// dropped.
func listTracked(dir string) *trackedFiles {
	ctx, cancel := context.WithTimeout(context.Background(), trackedListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z")
	cmd.Dir = dir
	cmd.Env = withoutGitLocation(os.Environ())
	out, err := cmd.Output()
	t := &trackedFiles{files: map[string]bool{}, dirs: map[string]bool{}, err: err}
	if err != nil {
		return t
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" {
			continue
		}
		t.files[p] = true
		for d := path.Dir(p); d != "." && !t.dirs[d]; d = path.Dir(d) {
			t.dirs[d] = true
		}
	}
	return t
}

// gitLocationVars point git at a repository other than the one its working
// directory is in.
var gitLocationVars = []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_COMMON_DIR=", "GIT_INDEX_FILE=",
	"GIT_OBJECT_DIRECTORY="}

func withoutGitLocation(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		keep := true
		for _, v := range gitLocationVars {
			if strings.HasPrefix(kv, v) {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, kv)
		}
	}
	return out
}

// trackedIn returns the checkout's tracked files, listed once per turn.
func (s *Toolset) trackedIn(slug, dir string) *trackedFiles {
	s.trackedMu.Lock()
	defer s.trackedMu.Unlock()
	if t, ok := s.tracked[slug]; ok {
		return t
	}
	t := listTracked(dir)
	s.tracked[slug] = t
	return t
}

// checkoutOf returns the innermost checkout that holds abs, an absolute
// path under the symlink-resolved root.
func (s *Toolset) checkoutOf(abs string) (slug, dir string) {
	best := -1
	for sl, d := range s.checkouts {
		if (abs == d || strings.HasPrefix(abs, d+string(filepath.Separator))) && len(d) > best {
			slug, dir, best = sl, d, len(d)
		}
	}
	return slug, dir
}

// leadsToReadable reports whether abs is a directory on the way to the
// checkout of a repository the turn may read, so a listing or a search
// can reach it.
func (s *Toolset) leadsToReadable(abs string) bool {
	for slug, d := range s.checkouts {
		owner, repo, _ := strings.Cut(slug, "/")
		if strings.HasPrefix(d, abs+string(filepath.Separator)) && s.canReadRepo(owner, repo) {
			return true
		}
	}
	return false
}

// served reports whether the turn may be shown the path at abs, judged by
// its name alone, never by what is on disk: the innermost checkout that
// holds it must be a repository the turn command lists as readable, and the
// path must be that checkout's root, a file git tracks there, a directory
// holding one, or a directory on the way to another readable checkout.
func (s *Toolset) served(abs string) *Error {
	slug, dir := s.checkoutOf(abs)
	if slug == "" {
		if s.leadsToReadable(abs) {
			return nil
		}
		return errRepoNotReadable
	}
	owner, repo, _ := strings.Cut(slug, "/")
	if !s.canReadRepo(owner, repo) {
		return errRepoNotReadable
	}
	if abs == dir || s.leadsToReadable(abs) {
		return nil
	}
	t := s.trackedIn(slug, dir)
	if t.err != nil {
		return errUntrackable
	}
	rel := filepath.ToSlash(strings.TrimPrefix(abs, dir+string(filepath.Separator)))
	if t.files[rel] || t.dirs[rel] {
		return nil
	}
	return errNotServed
}

// locate resolves a file tool's path and checks that the turn may be served
// it. The path is judged as named first (outside the root, secret-bearing,
// in a repository the turn may not read, or not tracked there) before
// anything on disk is consulted, so each of those refusals depends on the
// name alone. Then its symlinks are followed and where they lead is judged
// the same way; any refusal there is errNotServed, the refusal a missing
// file gets, because a target's own refusal would say that it exists.
func (s *Toolset) locate(p string) (*workspaceFS, string, *Error) {
	w, err := s.workspace()
	if err != nil {
		return nil, "", err
	}
	named, err := w.lexical(p)
	if err != nil {
		return nil, "", err
	}
	if err := s.served(w.abs(named)); err != nil {
		return nil, "", err
	}
	rel, err := w.resolve(named)
	if err != nil {
		return nil, "", errNotServed
	}
	if rel != named && s.served(w.abs(rel)) != nil {
		return nil, "", errNotServed
	}
	return w, rel, nil
}
