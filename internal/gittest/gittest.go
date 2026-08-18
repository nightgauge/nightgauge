// Package gittest is the one place Go tests stand up a real git repository.
//
// Before this package existed, every test file that needed a real git
// fixture copy-pasted its own tiny `exec.Command("git", ...)` wrapper (#680).
// Two independent hermeticity bugs lived in every one of those copies:
//
//   - #680: `git commit` (and several other porcelain commands) may trigger
//     `gc --auto`, which — because `gc.autoDetach` defaults to true — can
//     fork a background `git gc` and return immediately. That child keeps
//     writing into `.git` (new pack files, `gc.log`, deleting the loose
//     objects it just packed) after the foreground command, and often after
//     the whole test function, has returned. `t.TempDir()`'s deferred
//     `RemoveAll` then races that write and fails with ENOTEMPTY, producing
//     a red CI check on a diff that cannot possibly have caused it.
//   - #542: a bare `exec.Command("git", ...)` inherits the operator's (or the
//     CI image's) global and system git config. `commit.gpgsign=true` or a
//     `core.hooksPath` pointed at a refusing hook makes `git commit` fail for
//     reasons that have nothing to do with the code under test, so the
//     suite's greenness depends on the machine it runs on.
//
// Every function here runs git with both classes of ambient state
// neutralised, entirely at the environment level (never by writing to the
// repo's `.git/config`) so the very first command against a brand-new
// repository — including the `git init` that creates it — is already
// covered, and a new test cannot forget the step because there is no step to
// forget: it just calls Run.
package gittest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// gitBinary is resolved once, at package init — i.e. before any test has had
// a chance to narrow or repoint $PATH (several tests in internal/doctor do
// exactly that, deliberately, to exercise nightgauge's own binary-resolution
// cascade). Fixture setup must keep working under whatever the test under
// exercise does to its own environment, so every invocation below execs this
// resolved path directly rather than re-resolving "git" through the current
// $PATH on every call.
var gitBinary = "git"

func init() {
	if p, err := exec.LookPath("git"); err == nil {
		gitBinary = p
	}
}

// Env returns the process environment (os.Environ(), so PATH/HOME/etc. are
// still inherited normally) layered with the overrides every invocation in
// this package runs under:
//
//   - gc.auto=0 stops porcelain commands from ever invoking the gc --auto
//     heuristic in the first place — the actual fix for #680. This is
//     stronger than only disabling gc.autoDetach: a foreground gc still
//     costs wall-clock time on every command, and still mutates .git for as
//     long as it runs, which is a race window even without detaching.
//   - gc.autoDetach=false is belt-and-braces for the one case gc.auto=0
//     doesn't reach: something running `git gc --auto` (or plain `git gc`)
//     explicitly. If housekeeping runs anyway, it runs in the foreground and
//     is finished before the invoking command returns, instead of forking a
//     child that outlives it.
//   - maintenance.auto=false disarms the newer (Git 2.30+) scheduled
//     maintenance mechanism — a second, independent trigger with the exact
//     same "detach and keep writing" shape as gc --auto, and not covered by
//     gc.auto at all.
//   - core.fsmonitor=false stops git from spawning a fsmonitor--daemon to
//     watch the repository. A watcher holding an open handle into a
//     directory t.TempDir() is about to RemoveAll is the same class of bug
//     as the gc race, just with a different trigger.
//   - GIT_CONFIG_NOSYSTEM plus pointing GIT_CONFIG_GLOBAL/GIT_CONFIG_SYSTEM
//     at /dev/null makes every invocation blind to the operator's or CI
//     image's system and global git config — commit.gpgsign, core.hooksPath,
//     or whatever the next ambient setting turns out to be (#542). This is
//     deliberately a class fix (neutralise every config source outside this
//     package's control) rather than an enumeration of the two settings #542
//     happened to observe.
//   - GIT_OPTIONAL_LOCKS=0 stops git from opportunistically writing things
//     like a refreshed untracked-file cache during otherwise read-only
//     commands, which is the same "unexpected write after the command
//     returns" shape at a smaller scale.
//   - GIT_TERMINAL_PROMPT=0 turns a credential prompt that would otherwise
//     hang a test into an immediate failure.
//   - GIT_AUTHOR_*/GIT_COMMITTER_* give every commit a deterministic identity
//     so `git commit` never needs (and never silently falls back to)
//     user.name/user.email from any config source. A test that wants the
//     identity to also be visible via `git config user.name` (a few do, to
//     assert on it) can still set it locally with Run(t, dir, "config",
//     "user.name", ...) — env identity and repo-local config are not
//     exclusive, env identity just means config is never load-bearing.
//
// All of it is injected via GIT_CONFIG_COUNT/KEY_n/VALUE_n rather than an
// on-disk config file, so it applies before any file exists to write it into.
func Env() []string {
	return append(os.Environ(),
		"GIT_CONFIG_COUNT=4",
		"GIT_CONFIG_KEY_0=gc.auto", "GIT_CONFIG_VALUE_0=0",
		"GIT_CONFIG_KEY_1=gc.autoDetach", "GIT_CONFIG_VALUE_1=false",
		"GIT_CONFIG_KEY_2=maintenance.auto", "GIT_CONFIG_VALUE_2=false",
		"GIT_CONFIG_KEY_3=core.fsmonitor", "GIT_CONFIG_VALUE_3=false",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=nightgauge-test", "GIT_AUTHOR_EMAIL=nightgauge-test@example.invalid",
		"GIT_COMMITTER_NAME=nightgauge-test", "GIT_COMMITTER_EMAIL=nightgauge-test@example.invalid",
	)
}

// Command builds "git <args...>" run in dir with Env() applied. Use this
// directly (instead of Run) when a caller needs to inspect the error itself
// — e.g. asserting a ref does NOT exist — rather than failing the test on a
// non-zero exit.
func Command(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command(gitBinary, args...)
	cmd.Dir = dir
	cmd.Env = Env()
	return cmd
}

// Run executes "git <args...>" in dir, fails the test via t.Fatalf on a
// non-zero exit (including the git binary being missing), and returns
// combined stdout+stderr with surrounding whitespace trimmed. This is the
// one function nearly every fixture needs.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	out, err := Command(dir, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s (in %s): %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// InitRepo runs `git init <args...>` in dir and returns dir, so a fixture
// can write `repo := gittest.InitRepo(t, t.TempDir(), "-b", "main")` instead
// of a separate init call. Plain Run(t, dir, "init", ...) is equivalent —
// this exists for call-site readability only.
func InitRepo(t testing.TB, dir string, args ...string) string {
	t.Helper()
	Run(t, dir, append([]string{"init"}, args...)...)
	return dir
}
