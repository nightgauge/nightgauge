package adapters

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
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
// never touches the real home directory, and the per-run config
// (PrepareOpenCodeRun), which the SDK path reaches through `nightgauge
// opencode config`, reuses them.
//
// Observed on opencode 1.18.30 (testdata/opencode-cli/README.md):
//
//   - The four XDG variables move `opencode debug paths` config, data, cache
//     and state into the root. home does not move, and tmp stays at
//     $TMPDIR/opencode.
//   - OpenCode also reads $HOME/.opencode as a config directory and
//     $HOME/.agents/skills (and $HOME/.claude/skills) for skills, whatever the
//     XDG variables say. OPENCODE_DISABLE_EXTERNAL_SKILLS=1 stops the skill
//     scans; nothing but HOME stops the $HOME/.opencode read, so
//     PrepareOpenCodeRun refuses a dispatch while that directory holds config
//     (openCodeHomeConfigRefusal).
//   - The machine's managed OpenCode config merges above every other layer,
//     OPENCODE_CONFIG_CONTENT included, and nothing moves it, so
//     PrepareOpenCodeRun refuses a dispatch while it exists as well
//     (openCodeManagedConfigRefusal).

// openCodeInheritSetting is the machine-tier setting that layers the
// operator's own OpenCode config back into pipeline runs
// (config.OpenCodeConfig.InheritUserConfig, ADR-022 § 8). It is off by
// default, for the security reason recorded beside the field.
const openCodeInheritSetting = "opencode.inherit_user_config"

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
// injection that replaces them (#1626, #1638). Until then
// OPENCODE_DISABLE_CLAUDE_CODE_PROMPT already hides the repository's
// CLAUDE.md, as well as ~/.claude/CLAUDE.md, so a repository whose only
// steering is CLAUDE.md runs without it; the "repository steering" warning
// line says so.
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

// openCodeForgeEnv are the Git forge credentials: the stage's tools reach the
// forge with them, and the adapter exports GITHUB_TOKEN itself. The bundled
// catalog binds them to github-copilot and gitlab, two of the platform
// providers (openCodePlatformProviders), so a stage keeps them whatever
// provider it runs on, and the manager redacts their values from its output.
var openCodeForgeEnv = []string{"GITHUB_TOKEN", "GITLAB_TOKEN"}

// openCodePlatformProviders are the catalog providers whose variables belong
// to a general-purpose platform account: the forge, and the cloud and data
// platforms whose own CLIs, SDKs and infrastructure tools read the same
// variables for work that is not a model request. A stage keeps every variable
// the catalog binds to one of them, whatever provider it runs on, because its
// tools share the environment.
//
// The whole family stays, because removing part of it does not leave a tool
// without credentials: the tool moves to the next source in the platform's
// credential chain, which can be another account in another region, and
// nothing says so. With AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and
// AWS_REGION removed and AWS_SESSION_TOKEN left, the AWS CLI reads
// ~/.aws/credentials and ~/.aws/config instead. With
// GOOGLE_APPLICATION_CREDENTIALS removed and GOOGLE_CLOUD_PROJECT left,
// Google's clients use the operator's own application default credentials,
// and so does OpenCode's google-vertex provider, which still loads on
// GOOGLE_CLOUD_PROJECT (read from the 1.18.30 bundled source).
//
// OpenCode would therefore load these providers in any run that holds their
// variables. The per-run config's enabled_providers, narrowed to the
// dispatched provider key, is what keeps them out (BuildOpenCodeConfig):
// observed on 1.18.30, with AWS_REGION, GITHUB_TOKEN, GITLAB_TOKEN and
// GOOGLE_CLOUD_PROJECT set, a run loads amazon-bedrock, github-copilot, gitlab
// and google-vertex without it and only the dispatched provider with it. A
// dispatch that names one of them is refused (openCodePlatformProviderRefusal),
// so a stage never runs on the credentials it keeps for its tools.
var openCodePlatformProviders = []string{
	"amazon-bedrock",          // AWS
	"cloudflare-ai-gateway",   // Cloudflare
	"cloudflare-workers-ai",   // Cloudflare
	"databricks",              // Databricks
	"digitalocean",            // DigitalOcean
	"github-copilot",          // GITHUB_TOKEN, the forge
	"gitlab",                  // GITLAB_TOKEN, the forge
	"google-vertex",           // Google Cloud application default credentials
	"google-vertex-anthropic", // Google Cloud application default credentials
	"huggingface",             // the Hugging Face Hub
	"snowflake-cortex",        // Snowflake
	"vultr",                   // Vultr
	"wandb",                   // Weights & Biases
}

// openCodePlatformEnvNames is every variable openCodeCatalogEnv binds to a
// platform provider (openCodePlatformProviders).
var openCodePlatformEnvNames = func() map[string]bool {
	names := map[string]bool{}
	for _, provider := range openCodePlatformProviders {
		for _, v := range openCodeCatalogEnv[provider] {
			names[v] = true
		}
	}
	return names
}()

// openCodeEndpointEnv are the variables the Anthropic and OpenAI SDKs bundled
// in OpenCode 1.18.30 read as the provider's base URL when no config gives
// one. Observed: with ANTHROPIC_BASE_URL set, an anthropic/ run sent its
// request, and the API key with it, to that server, and OPENAI_BASE_URL did
// the same for an openai/ run. An inherited value could send a stage and its
// key anywhere, a proxy that serves a subscription included, so no spawn
// inherits one: a provider's endpoint comes from config only (ADR-022 § 8,
// § 17, § Endpoints).
var openCodeEndpointEnv = []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"}

// openCodeWithheldPrefix is the prefix of OpenCode's own variables, every one
// of which a spawn is kept from inheriting (OpenCodeWithholdsEnv).
const openCodeWithheldPrefix = "OPENCODE_"

// openCodeCatalogEnvNames is every variable openCodeCatalogEnv binds to any
// provider.
var openCodeCatalogEnvNames = func() map[string]bool {
	names := map[string]bool{}
	for _, vars := range openCodeCatalogEnv {
		for _, v := range vars {
			names[v] = true
		}
	}
	return names
}()

// openCodeDispatchProvider is the provider key model names, parsed the way
// OpenCodeModelArg parses it: trimmed, and cut at the first slash.
func openCodeDispatchProvider(model string) string {
	provider, _, _ := strings.Cut(strings.TrimSpace(model), "/")
	return provider
}

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
//   - The provider base-URL variables (openCodeEndpointEnv), whatever the
//     provider.
//   - Every variable the bundled catalog binds to a model service other than
//     the one model names (openCodeCatalogEnv). OpenCode loads a catalog
//     provider when any one of its variables is set, so a run on a local model
//     holds no hosted model service's key, only its own provider's
//     (lmstudio's LMSTUDIO_API_KEY).
//
// The variables of a platform provider (openCodePlatformProviders) are never
// withheld: the forge tokens, and the cloud and data platform credentials the
// stage's tools read, AWS's among them. So none of this decides every provider
// a run can reach: OpenCode could load a platform provider on the credentials
// the stage keeps, a provider's own loader can find credentials the catalog
// does not name, such as an AWS profile, and OpenCode's own hosted provider
// serves its free models with no key. The per-run config's enabled_providers
// decides it (BuildOpenCodeConfig).
//
// The stage's tools share the environment, so they lose the withheld
// variables too. A tool that needs one fails without it, or uses a login of
// its own, and PreDispatch names every one the environment holds
// (openCodeWithheldProviderEnv), so neither happens silently.
func OpenCodeWithholdsEnv(model, key string) bool {
	if strings.HasPrefix(key, openCodeWithheldPrefix) || slices.Contains(openCodeEndpointEnv, key) {
		return true
	}
	if !openCodeCatalogEnvNames[key] || openCodePlatformEnvNames[key] {
		return false
	}
	return !slices.Contains(openCodeCatalogEnv[openCodeDispatchProvider(model)], key)
}

// openCodeWithheldProviderEnv returns, sorted, the name of every variable in
// environ, a process environment of KEY=value entries, that holds a value and
// that a dispatch to model withholds, other than OpenCode's own OPENCODE_*
// variables: the other model services' catalog variables and the provider
// base URLs, which a stage's tools could have read. It returns names only and
// never a value.
func openCodeWithheldProviderEnv(model string, environ []string) []string {
	var names []string
	for _, kv := range environ {
		key, value, _ := strings.Cut(kv, "=")
		if value == "" || strings.HasPrefix(key, openCodeWithheldPrefix) || !OpenCodeWithholdsEnv(model, key) {
			continue
		}
		names = append(names, key)
	}
	slices.Sort(names)
	return slices.Compact(names)
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
// config/ holds a symbolic link to every entry of the operator's XDG config
// directory ($XDG_CONFIG_HOME, else ~/.config, resolved from lookup) except
// OpenCode's own, opencode/, which stays the run's. Every tool the stage
// starts inherits the run's XDG_CONFIG_HOME, so without the links each would
// lose the config it keeps there and fall back to its defaults: git its XDG
// config, ignore, attributes and credentials files, uv and pip a private
// package index, podman its registries. With them, each reads the operator's
// files as it does outside, and git reads them in the same order beside
// ~/.gitconfig; an inherited GIT_CONFIG_GLOBAL passes through untouched.
// Of that directory, OpenCode 1.18.30 loads its config, plugins, agents and
// skills from opencode/ alone. Every call links
// the entries the operator has added since, and re-points a link whose
// directory moved. An entry the run created itself, while the operator had
// none, is not a link and is left as it is.
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
	if err := linkOperatorConfig(root, operatorXDGConfigHome(home, lookup)); err != nil {
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

// linkOperatorConfig links every entry of the operator's XDG config directory
// except OpenCode's own into root/config. See EnsureOpenCodeRunRoot.
func linkOperatorConfig(root, operatorConfigHome string) error {
	if !filepath.IsAbs(operatorConfigHome) {
		return nil
	}
	if fi, err := os.Stat(operatorConfigHome); err != nil || !fi.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(operatorConfigHome)
	if err != nil {
		return fmt.Errorf("opencode run root: read the operator's XDG config directory: %w", err)
	}
	for _, e := range entries {
		// EqualFold: on a case-insensitive filesystem a link named OpenCode
		// would be the run's own opencode/ directory.
		if strings.EqualFold(e.Name(), "opencode") {
			continue
		}
		link := filepath.Join(root, "config", e.Name())
		if err := linkOperatorConfigEntry(link, filepath.Join(operatorConfigHome, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// linkOperatorConfigEntry makes link a symbolic link to target, unless
// something that is not a link is already there.
func linkOperatorConfigEntry(link, target string) error {
	fi, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("opencode run root: %w", err)
	case fi.Mode()&fs.ModeSymlink == 0:
		// The run created it while the operator had no such entry.
		return nil
	default:
		current, err := os.Readlink(link)
		if err == nil && current == target {
			return nil
		}
		// The operator's XDG config directory moved since an earlier stage
		// linked it.
		if err := os.Remove(link); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("opencode run root: %w", err)
		}
	}
	if err := os.Symlink(target, link); err != nil && !errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("opencode run root: %w", err)
	}
	// A parallel stage of the same run may have created it first.
	if current, err := os.Readlink(link); err != nil || current != target {
		return fmt.Errorf("opencode run root: %s does not link to the operator's %s", link, target)
	}
	return nil
}

// RemoveOpenCodeRunRoot deletes the per-run root for id. A root that does not
// exist is not an error.
//
// It refuses an id that is not a run identity, a root that is itself a
// symbolic link or not a directory, and a root that does not resolve to a
// directory directly under OpenCodeRunsDir. Inside the root nothing is
// followed: os.RemoveAll unlinks a symbolic link, such as a link in config/
// to the operator's config, and never deletes what it points at.
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
	// InheritUserConfig is the machine-tier opencode.inherit_user_config.
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
//     default moves with XDG_CACHE_HOME). git, and every other tool that
//     reads its config from the XDG config directory, needs no variable: see
//     EnsureOpenCodeRunRoot;
//   - with InheritUserConfig, OPENCODE_CONFIG_DIR at the operator's own
//     OpenCode config directory, which layers its opencode.json,
//     opencode.jsonc, agents, commands, modes, plugins, tools and skills back
//     in. Observed on 1.18.30, OpenCode merges that directory above the
//     per-run config file and below OPENCODE_CONFIG_CONTENT, and the machine's
//     managed config above both.
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
			"or set %s: true in your machine-tier config (~/.nightgauge/config.yaml) to run pipeline stages with your OpenCode config. See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
		dir, strings.Join(found, ", "), filepath.Join("~", ".config", "opencode"), openCodeInheritSetting)
}

// openCodeManagedConfigFiles are the machine-wide managed config files
// opencode 1.18.30 reads on goos, from its bundled source: opencode.json and
// opencode.jsonc in the managed config directory, /etc/opencode on Linux and
// /Library/Application Support/opencode on macOS, and on macOS the
// managed-preferences profile, the user's and then the machine's. username is
// the current user's name. OpenCode merges these after
// OPENCODE_CONFIG_CONTENT, so a key in them wins over every key a run is
// given, and no XDG or HOME variable moves them.
func openCodeManagedConfigFiles(goos, username string) []string {
	dir := "/etc/opencode"
	if goos == "darwin" {
		dir = "/Library/Application Support/opencode"
	}
	files := []string{filepath.Join(dir, "opencode.json"), filepath.Join(dir, "opencode.jsonc")}
	if goos == "darwin" {
		const profile = "ai.opencode.managed.plist"
		files = append(files,
			filepath.Join("/Library/Managed Preferences", username, profile),
			filepath.Join("/Library/Managed Preferences", profile))
	}
	return files
}

// openCodeUsername is the user name OpenCode reads its per-user managed
// preferences under: the current user's, or "user" when it has none.
func openCodeUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "user"
}

// openCodeManagedConfigRefusal refuses a dispatch while any of files, the
// machine's managed OpenCode config (openCodeManagedConfigFiles), exists.
// Observed on 1.18.30, a key in the managed config directory wins over the
// same key in OPENCODE_CONFIG_CONTENT, the layer Nightgauge's locked keys go in
// (ADR-022 § 8, § 15), so a run cannot be isolated from it. The refusal names
// the files and never reads them.
func openCodeManagedConfigRefusal(files []string) error {
	var found []string
	for _, f := range files {
		if _, err := os.Lstat(f); err == nil {
			found = append(found, f)
		}
	}
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"this machine has managed OpenCode config (%s), which OpenCode merges above every config a pipeline run is given, Nightgauge's own included, whatever the run's directories are, so a pipeline run cannot be isolated from it. "+
			"Remove it, or set %s: true in your machine-tier config (~/.nightgauge/config.yaml) to run pipeline stages with it and the rest of your OpenCode config. See docs/decisions/022-opencode-multi-provider-adapter.md § 8",
		strings.Join(found, ", "), openCodeInheritSetting)
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
