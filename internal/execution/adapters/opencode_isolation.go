package adapters

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/runstate"
)

// OpenCode run isolation (ADR-022 § 8, § 17, § 22).
//
// Every opencode spawn runs with its four XDG base directories inside a root
// private to the pipeline run, ~/.nightgauge/opencode/runs/<id>/, so OpenCode
// reads none of the operator's own OpenCode config, credentials, plugins or
// sessions, and the stage's transcript never lands in the operator's session
// database. The functions here are pure apart from the filesystem they are
// pointed at: home and the inherited environment are parameters, so a test
// never touches the real home directory, and the per-run config builder
// (#1625) and the SDK path (#1648) reuse them.
//
// Observed on opencode 1.18.30 (testdata/opencode-cli/README.md):
//
//   - The four XDG variables move `opencode debug paths` config, data, cache
//     and state into the root. home does not move, and tmp stays at
//     $TMPDIR/opencode.
//   - OpenCode also reads $HOME/.opencode as a config directory and
//     $HOME/.agents/skills (and $HOME/.claude/skills) for skills, whatever the
//     XDG variables say. OPENCODE_DISABLE_EXTERNAL_SKILLS=1 stops the skill
//     scans; nothing but HOME stops the $HOME/.opencode read, so PreDispatch
//     refuses a dispatch while that directory holds config
//     (openCodeHomeConfigRefusal).

// OpenCodeInheritUserConfigEnvVar layers the operator's own global OpenCode
// config back into pipeline runs when it is exactly "1" (ADR-022 § 8).
//
// Default OFF. ADR-020 requires every default-off switch to state its reason
// beside the flag, and the reason is SECURITY: the operator's config can name
// plugins, which run as in-process code, MCP servers, providers, permissions
// and models, and a pipeline run must behave the same on every machine. It is
// read from the process environment only, so a committed repository config can
// never turn it on. Stored logins are never inherited either way: they live in
// the data directory, which stays per run (§ 17).
const OpenCodeInheritUserConfigEnvVar = "NIGHTGAUGE_OPENCODE_INHERIT_USER_CONFIG"

// OpenCodeOrphanMaxAge is the age past which SweepOpenCodeRunRoots deletes a
// per-run root. A run deletes its own root when it ends (ADR-022 § 22); the
// sweep is the backstop for a run that crashed first. Every stage dispatch
// refreshes its root's modification time, so only a root no stage has used
// for this long is swept.
const OpenCodeOrphanMaxAge = 7 * 24 * time.Hour

// openCodeXDGDirs maps each XDG base directory variable to its directory in
// the per-run root.
var openCodeXDGDirs = []struct{ env, dir string }{
	{"XDG_CONFIG_HOME", "config"},
	{"XDG_DATA_HOME", "data"},
	{"XDG_CACHE_HOME", "cache"},
	{"XDG_STATE_HOME", "state"},
}

// openCodeDisableFlags are set to "1" on every spawn (ADR-022 § 10, § 11,
// § 15). The blanket OPENCODE_DISABLE_CLAUDE_CODE is deliberately not one of
// them: Nightgauge disables what it means to and no more. Nor is
// OPENCODE_DISABLE_PROJECT_CONFIG yet: on 1.18.30 it also hides the
// repository's AGENTS.md and CLAUDE.md, so it lands with the steering
// injection that replaces them (#1626, #1638).
var openCodeDisableFlags = []string{
	"OPENCODE_DISABLE_MODELS_FETCH",
	"OPENCODE_DISABLE_AUTOUPDATE",
	"OPENCODE_DISABLE_LSP_DOWNLOAD",
	"OPENCODE_DISABLE_DEFAULT_PLUGINS",
	"OPENCODE_DISABLE_SHARE",
	"OPENCODE_DISABLE_CLAUDE_CODE_PROMPT",
	"OPENCODE_DISABLE_CLAUDE_CODE_SKILLS",
	// Observed on 1.18.30: without it, the operator's $HOME/.agents/skills
	// load into every run whatever the XDG variables say.
	"OPENCODE_DISABLE_EXTERNAL_SKILLS",
}

// openCodeProviderCredentialEnv maps an OpenCode provider key to the API-key
// variables OpenCode 1.18.30's bundled catalog binds to it. A spawn keeps only
// the variables of the provider it dispatches to and inherits none of the
// others (OpenCodeWithholdsEnv), so a run on a local model sees no hosted
// provider's key, and a hosted run sees only its own provider's.
var openCodeProviderCredentialEnv = map[string][]string{
	"anthropic":  {"ANTHROPIC_API_KEY"},
	"openai":     {"OPENAI_API_KEY"},
	"xai":        {"XAI_API_KEY"},
	"google":     {"GOOGLE_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY", "GEMINI_API_KEY"},
	"openrouter": {"OPENROUTER_API_KEY"},
}

// openCodeCredentialEnv is every variable in openCodeProviderCredentialEnv,
// sorted.
var openCodeCredentialEnv = func() []string {
	var names []string
	for _, vars := range openCodeProviderCredentialEnv {
		names = append(names, vars...)
	}
	slices.Sort(names)
	return names
}()

// OpenCodeWithholdsEnv reports whether an inherited environment variable named
// key must not reach an opencode child dispatched to model. It decides on the
// name alone and never reads a value, so nothing it withholds can be logged.
//
//   - Every OPENCODE_* variable (ADR-022 § 8). An inherited one could change a
//     run's posture: OPENCODE_AUTH_CONTENT and OPENCODE_CONSOLE_TOKEN carry
//     logins (OpenCode reads the first in place of auth.json, and exports both
//     to the processes it starts), OPENCODE_DB redirects the session
//     database, OPENCODE_CONFIG, OPENCODE_CONFIG_DIR and
//     OPENCODE_CONFIG_CONTENT inject config, and OPENCODE_MODELS_PATH swaps
//     the provider catalog. A variable a later version adds goes with them.
//     The adapter's own OPENCODE_* exports are added after this filter, so
//     they survive it.
//   - Every hosted provider's API-key variable except those of the provider
//     key model names (openCodeProviderCredentialEnv). A local model gets
//     none of them.
func OpenCodeWithholdsEnv(model, key string) bool {
	if strings.HasPrefix(key, "OPENCODE_") {
		return true
	}
	if !slices.Contains(openCodeCredentialEnv, key) {
		return false
	}
	provider, _, _ := strings.Cut(strings.TrimSpace(model), "/")
	return !slices.Contains(openCodeProviderCredentialEnv[provider], key)
}

// OpenCodeRunsDir is the directory every OpenCode per-run root lives in.
func OpenCodeRunsDir(home string) string {
	return filepath.Join(home, ".nightgauge", "opencode", "runs")
}

// OpenCodeRunRoot returns the per-run root for id. id must be a run identity
// (runstate.IsIdentity), which is also the shape of an id minted for a
// dispatch without one, so a value holding a path separator or ".." can never
// name a directory outside OpenCodeRunsDir.
func OpenCodeRunRoot(home, id string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("opencode run root: the home directory %q is not an absolute path", home)
	}
	if !runstate.IsIdentity(id) {
		return "", fmt.Errorf("opencode run root: %q is not a run identity (a canonical lowercase UUIDv7)", id)
	}
	return filepath.Join(OpenCodeRunsDir(home), id), nil
}

// EnsureOpenCodeRunRoot creates the per-run root for id, or reuses it when an
// earlier stage of the run created it, and returns its path. created reports
// whether this call created it.
//
// The root and its config/, data/, cache/ and state/ are directories of mode
// 0700; an existing one with another mode is reset to 0700, and one that is a
// symbolic link or not a directory is refused, so nothing is written through a
// link planted in the root. Nothing else is created: in particular no
// auth.json, so the data directory starts empty (ADR-022 § 17).
//
// When the operator has an XDG git config directory ($XDG_CONFIG_HOME/git,
// else ~/.config/git, resolved from lookup), config/git is a symbolic link to
// it. git reads its XDG config, ignore, attributes and credentials files from
// $XDG_CONFIG_HOME/git, so with the link git inside the stage reads the same
// files, in the same order beside ~/.gitconfig, as it does outside. With no
// XDG git directory there is nothing to link: git reads ~/.gitconfig, which
// XDG does not move. An inherited GIT_CONFIG_GLOBAL passes through untouched.
//
// Every call refreshes the root's modification time, which is what
// SweepOpenCodeRunRoots ages.
func EnsureOpenCodeRunRoot(home, id string, lookup func(string) (string, bool)) (root string, created bool, err error) {
	root, err = OpenCodeRunRoot(home, id)
	if err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(OpenCodeRunsDir(home), 0o700); err != nil {
		return "", false, fmt.Errorf("opencode run root: %w", err)
	}
	if created, err = ensurePrivateDir(root); err != nil {
		return "", false, err
	}
	for _, x := range openCodeXDGDirs {
		if _, err := ensurePrivateDir(filepath.Join(root, x.dir)); err != nil {
			return "", false, err
		}
	}
	if err := linkOperatorGitDir(root, operatorXDGConfigHome(home, lookup)); err != nil {
		return "", false, err
	}
	now := time.Now()
	if err := os.Chtimes(root, now, now); err != nil {
		return "", false, fmt.Errorf("opencode run root: %w", err)
	}
	return root, created, nil
}

// ensurePrivateDir makes path a directory of mode 0700 and reports whether it
// created it. A path that exists as anything but a directory, a symbolic link
// included, is refused.
func ensurePrivateDir(path string) (bool, error) {
	mkErr := os.Mkdir(path, 0o700)
	if mkErr != nil && !errors.Is(mkErr, fs.ErrExist) {
		return false, fmt.Errorf("opencode run root: %w", mkErr)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return false, fmt.Errorf("opencode run root: %w", err)
	}
	if !fi.IsDir() {
		return false, fmt.Errorf("opencode run root: %s exists and is not a directory (a symbolic link is refused)", path)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return false, fmt.Errorf("opencode run root: %w", err)
		}
	}
	return mkErr == nil, nil
}

// linkOperatorGitDir points root/config/git at the operator's XDG git config
// directory when there is one. See EnsureOpenCodeRunRoot.
func linkOperatorGitDir(root, operatorConfigHome string) error {
	target := filepath.Join(operatorConfigHome, "git")
	if !filepath.IsAbs(target) {
		return nil
	}
	if fi, err := os.Stat(target); err != nil || !fi.IsDir() {
		return nil
	}
	link := filepath.Join(root, "config", "git")
	current, err := os.Readlink(link)
	switch {
	case err == nil && current == target:
		return nil
	case err == nil:
		// The operator's XDG directory moved since an earlier stage linked it.
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("opencode run root: %w", err)
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return fmt.Errorf("opencode run root: %s exists and is not a link to the operator's git config directory", link)
	}
	if err := os.Symlink(target, link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("opencode run root: %w", err)
	}
	// A parallel stage of the same run may have created it first.
	if current, err := os.Readlink(link); err != nil || current != target {
		return fmt.Errorf("opencode run root: %s does not link to the operator's git config directory", link)
	}
	return nil
}

// RemoveOpenCodeRunRoot deletes the per-run root for id. A root that does not
// exist is not an error.
//
// It refuses an id that is not a run identity, a root that is itself a
// symbolic link or not a directory, and a root that does not resolve to a
// directory directly under OpenCodeRunsDir. Inside the root nothing is
// followed: os.RemoveAll unlinks a symbolic link, such as config/git, and
// never deletes what it points at.
func RemoveOpenCodeRunRoot(home, id string) error {
	root, err := OpenCodeRunRoot(home, id)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("opencode run root: %w", err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("opencode run root: refusing to delete %s: it is a symbolic link", root)
	}
	if !fi.IsDir() {
		return fmt.Errorf("opencode run root: refusing to delete %s: it is not a directory", root)
	}
	runs, err := filepath.EvalSymlinks(OpenCodeRunsDir(home))
	if err != nil {
		return fmt.Errorf("opencode run root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("opencode run root: %w", err)
	}
	if filepath.Dir(resolved) != runs {
		return fmt.Errorf("opencode run root: refusing to delete %s: it resolves to %s, outside %s", root, resolved, runs)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("opencode run root: %w", err)
	}
	return nil
}

// SweepOpenCodeRunRoots deletes every per-run root whose modification time is
// more than maxAge before now, and returns the ids it deleted. It is the
// backstop for a run that crashed before it could delete its own root.
//
// Only a directory whose name is a run identity is a root. Anything else in
// OpenCodeRunsDir, a symbolic link named like one included, is left alone,
// because Nightgauge did not create it. A failure to delete one root does not
// stop the sweep; the failures are returned together.
func SweepOpenCodeRunRoots(home string, maxAge time.Duration, now time.Time) ([]string, error) {
	entries, err := os.ReadDir(OpenCodeRunsDir(home))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opencode run root sweep: %w", err)
	}
	var removed []string
	var errs []error
	for _, e := range entries {
		id := e.Name()
		if !runstate.IsIdentity(id) {
			continue
		}
		fi, err := os.Lstat(filepath.Join(OpenCodeRunsDir(home), id))
		if err != nil || !fi.IsDir() || now.Sub(fi.ModTime()) <= maxAge {
			continue
		}
		if err := RemoveOpenCodeRunRoot(home, id); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, id)
	}
	return removed, errors.Join(errs...)
}

// OpenCodeIsolation is the input to OpenCodeIsolationEnv.
type OpenCodeIsolation struct {
	// Root is the per-run root (EnsureOpenCodeRunRoot).
	Root string
	// Home is the operator's home directory.
	Home string
	// Lookup reads the environment the nightgauge process inherited, the
	// operator's (os.LookupEnv in production). Every value this change
	// re-pins is resolved from it, before any override applies.
	Lookup func(string) (string, bool)
	// GOOS decides the default Go build cache location (runtime.GOOS).
	GOOS string
	// MachineConfigDir is the directory of the operator's machine-tier
	// Nightgauge config (config.MachineConfigDir).
	MachineConfigDir string
	// InheritUserConfig is OpenCodeInheritUserConfigEnvVar's value.
	InheritUserConfig bool
}

// OpenCodeIsolationEnv returns the variables an opencode spawn sets to run in
// the per-run root (ADR-022 § 8):
//
//   - XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_CACHE_HOME and XDG_STATE_HOME at the
//     root's config/, data/, cache/ and state/;
//   - every flag in openCodeDisableFlags, set to "1";
//   - the tools the XDG move would otherwise take from the operator, pinned
//     back to what they resolve to outside the run: GH_CONFIG_DIR to the
//     operator's gh config directory (gh keeps its hosts and auth there);
//     NIGHTGAUGE_CONFIG_HOME to the machine-tier config directory; and, when
//     the operator has not set GOCACHE, GOCACHE to the Go build cache the
//     operator's go uses, so builds are not cold in every stage (the Linux
//     default moves with XDG_CACHE_HOME). git needs no variable: see
//     EnsureOpenCodeRunRoot;
//   - with InheritUserConfig, OPENCODE_CONFIG_DIR at the operator's own
//     OpenCode config directory, which layers its opencode.json,
//     opencode.jsonc, agents, commands, modes, plugins, tools and skills back
//     in. Observed on 1.18.30, OpenCode merges that directory above the
//     per-run config file and below OPENCODE_CONFIG_CONTENT.
//
// An inherited value of any of these names is replaced by the manager, never
// left beside the export.
func OpenCodeIsolationEnv(in OpenCodeIsolation) (map[string]string, error) {
	for name, dir := range map[string]string{"Root": in.Root, "Home": in.Home, "MachineConfigDir": in.MachineConfigDir} {
		if !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("opencode isolation: %s %q is not an absolute path", name, dir)
		}
	}
	if in.Lookup == nil {
		return nil, errors.New("opencode isolation: no inherited environment to resolve the operator's tools from")
	}
	env := map[string]string{}
	for _, x := range openCodeXDGDirs {
		env[x.env] = filepath.Join(in.Root, x.dir)
	}
	for _, flag := range openCodeDisableFlags {
		env[flag] = "1"
	}
	operatorConfigHome := operatorXDGConfigHome(in.Home, in.Lookup)
	if v, ok := in.Lookup("GH_CONFIG_DIR"); ok && v != "" {
		env["GH_CONFIG_DIR"] = v
	} else {
		env["GH_CONFIG_DIR"] = filepath.Join(operatorConfigHome, "gh")
	}
	env["NIGHTGAUGE_CONFIG_HOME"] = in.MachineConfigDir
	if v, ok := in.Lookup("GOCACHE"); !ok || v == "" {
		env["GOCACHE"] = filepath.Join(operatorUserCacheDir(in), "go-build")
	}
	if in.InheritUserConfig {
		env["OPENCODE_CONFIG_DIR"] = filepath.Join(operatorConfigHome, "opencode")
	}
	return env, nil
}

// operatorXDGConfigHome is the XDG config base the operator's gh, git and
// OpenCode read: XDG_CONFIG_HOME when it is set, else ~/.config.
func operatorXDGConfigHome(home string, lookup func(string) (string, bool)) string {
	if v, ok := lookup("XDG_CONFIG_HOME"); ok && v != "" {
		return v
	}
	return filepath.Join(home, ".config")
}

// operatorUserCacheDir is os.UserCacheDir as the operator's go resolves it,
// from the inherited environment rather than this process's.
func operatorUserCacheDir(in OpenCodeIsolation) string {
	switch in.GOOS {
	case "darwin", "ios":
		return filepath.Join(in.Home, "Library", "Caches")
	default:
		if v, ok := in.Lookup("XDG_CACHE_HOME"); ok && filepath.IsAbs(v) {
			return v
		}
		return filepath.Join(in.Home, ".cache")
	}
}

// openCodeHomeConfigEntries are what opencode 1.18.30 loads from a config
// directory, read from its bundled source: the two config files, and the
// agent, command, mode, plugin, tool and skill directories under either
// spelling.
var openCodeHomeConfigEntries = []string{
	"opencode.json", "opencode.jsonc",
	"agent", "agents", "command", "commands", "mode", "modes",
	"plugin", "plugins", "tool", "tools", "skill", "skills",
}

// openCodeHomeConfigRefusal refuses a dispatch while $HOME/.opencode holds
// config. Observed on 1.18.30: OpenCode reads $HOME/.opencode as a config
// directory on every run, and neither the XDG variables,
// OPENCODE_DISABLE_PROJECT_CONFIG nor OPENCODE_PURE stops it, so the per-run
// root cannot keep it out. A directory holding only what an install or
// OpenCode itself puts there (bin/, a package.json and its node_modules) is
// not config. The refusal names the entries and never reads them.
func openCodeHomeConfigRefusal(home string) error {
	dir := filepath.Join(home, ".opencode")
	var found []string
	for _, name := range openCodeHomeConfigEntries {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"%s holds OpenCode config (%s), and OpenCode reads that directory on every run whatever its XDG directories are, so a pipeline run cannot be isolated from it. "+
			"Move those entries into your XDG OpenCode config directory (%s), which your own OpenCode sessions still read and pipeline runs do not, "+
			"or set %s=1 to run pipeline stages with your OpenCode config. See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
		dir, strings.Join(found, ", "), filepath.Join("~", ".config", "opencode"), OpenCodeInheritUserConfigEnvVar)
}

// openCodeStoredLoginRefusal refuses a dispatch whose run root holds an
// auth.json. Nightgauge never writes or copies one, and a run authenticates
// only with its provider's API-key variable (ADR-022 § 17), so a stored login
// that appeared in the run's data directory, from something an earlier stage
// ran, must not be used by the next. Its content is never read.
func openCodeStoredLoginRefusal(root string) error {
	path := filepath.Join(root, "data", "opencode", "auth.json")
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	return fmt.Errorf(
		"the run's OpenCode data directory holds stored logins (%s), which a pipeline run never uses: a stage authenticates only with its provider's API-key variable. "+
			"Nightgauge never writes that file, so something a stage ran did; the run's root is deleted when the run ends. "+
			"See docs/decisions/022-opencode-multi-provider-adapter.md § 17", path)
}
