package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
)

// OpenCode's version policy, binary pin, endpoint readiness, and the OpenCode
// config on the machine a run cannot be isolated from (ADR-022 § 7, § 8,
// § 20, § Endpoints).
//
// These are the checks a dispatch and `nightgauge doctor` share. They live
// here rather than in the doctor because the doctor imports the execution
// packages, so the adapter can reach them and the doctor can call them, and
// there is one implementation of each: PreDispatch enforces the version
// policy before anything is created, and the doctor reports the same verdict.
//
// Every opencode process started here is a probe: it runs in a throwaway
// directory that is its HOME, TMPDIR and four XDG base directories, with an
// environment built from nothing but PATH, the switches every spawn sets and
// project config off, in its own process group, under openCodeProbeTimeout.
// It reads and writes none of the operator's OpenCode state, none of a
// pipeline run's, and no config in a directory above its own.

// OpenCodeIncompatible is the kind of a dispatch refused because the opencode
// binary it would spawn cannot serve it (ADR-022 § 20).
const OpenCodeIncompatible = "adapter_incompatible"

// OpenCodeIncompatibleError refuses a dispatch the installed opencode binary
// cannot serve: its version is below the compat manifest's floor or could not
// be read, or it is newer than max-tested and either failed the self-test or
// would run a model server the operator runs.
type OpenCodeIncompatibleError struct {
	// Binary is the binary the dispatch would spawn.
	Binary string
	// Version is its version, "" when it could not be read.
	Version string
	// MinVersion and MaxTested are the compat manifest's.
	MinVersion string
	MaxTested  string
	// Reason says why the binary cannot serve the dispatch, and Remediation
	// what to do. Neither ends with a period.
	Reason      string
	Remediation string
}

func (e *OpenCodeIncompatibleError) Error() string {
	return OpenCodeIncompatible + ": " + e.Reason + ". " + e.Remediation +
		". See docs/decisions/022-opencode-multi-provider-adapter.md § 20"
}

// Kind is the refusal's kind, OpenCodeIncompatible.
func (e *OpenCodeIncompatibleError) Kind() string { return OpenCodeIncompatible }

// openCodeBinaryName is the command an unpinned dispatch runs from PATH.
const openCodeBinaryName = "opencode"

// openCodeCatalogVersion is the opencode version openCodeCatalogEnv and
// openCodeAnthropicModels were read from. Credential withholding, output
// redaction and the reserved endpoint ids rest on those snapshots matching the
// binary a run starts, so the compat manifest's max_tested rises only with
// them: re-read both from the new binary (TestOpenCodeCatalogEnvMatchesTheBinary
// and TestOpenCodeAnthropicModelsMatchTheBinary, build tag
// opencode_integration), then raise this.
// TestOpenCodeCatalogSnapshotIsTheMaxTestedVersion holds the two together.
const openCodeCatalogVersion = "1.18.30"

// openCodeManifest is the opencode compat manifest (#1613), the single source
// of the floor, the max-tested version and the install package.
func openCodeManifest() (adaptercompat.Manifest, error) {
	if m, ok := adaptercompat.Get(openCodeBinaryName); ok {
		return m, nil
	}
	if _, err := adaptercompat.Load(); err != nil {
		return adaptercompat.Manifest{}, fmt.Errorf("the adapter compat manifests did not load, so opencode's version floor is unknown: %w", err)
	}
	return adaptercompat.Manifest{}, errors.New("the adapter compat manifests hold no opencode manifest, so opencode's version floor is unknown")
}

// openCodeToolsPrefix is where the managed install puts a tested opencode.
const openCodeToolsPrefix = "~/.nightgauge/tools/opencode"

// OpenCodeManagedInstall returns, for printing and never for running, the
// command that installs the manifest's max-tested opencode beside Nightgauge
// and the path to pin opencode.binary to once it has run. home, when set,
// makes the pin the absolute path a pin must be.
func OpenCodeManagedInstall(m adaptercompat.Manifest, home string) (command, pin string) {
	pkg := m.Install.NPM
	if pkg == "" {
		pkg = openCodeBinaryName
	}
	command = "npm i --prefix " + openCodeToolsPrefix + " " + pkg + "@" + m.MaxTested
	pin = openCodeToolsPrefix + "/node_modules/.bin/" + openCodeBinaryName
	if home != "" {
		pin = filepath.Join(home, ".nightgauge", "tools", "opencode", "node_modules", ".bin", openCodeBinaryName)
	}
	return command, pin
}

// openCodeInstallRemedy is the remediation that ends every version refusal.
func openCodeInstallRemedy(m adaptercompat.Manifest, home string) string {
	command, pin := OpenCodeManagedInstall(m, home)
	return fmt.Sprintf("Install the tested build with `%s` and pin it with opencode.binary: %s in ~/.nightgauge/config.yaml", command, pin)
}

// OpenCodeBinary is the opencode binary a dispatch spawns and the doctor
// checks.
type OpenCodeBinary struct {
	// Path is absolute: opencode.binary as written, or the opencode on PATH.
	Path string
	// Pinned is true when opencode.binary names it.
	Pinned bool
}

// ResolveOpenCodeBinary returns the binary pin names (the machine-tier
// opencode.binary), or the opencode lookPath finds on PATH when pin is empty.
//
// A pin must be the absolute path of an executable file. A relative one, a
// bare command name included, is refused and never looked up on PATH: a pin
// exists so that the binary a dispatch runs does not move when PATH, or the
// file on it, does, as a PATH install does when OpenCode's TUI updates itself.
func ResolveOpenCodeBinary(pin string, lookPath func(string) (string, error)) (OpenCodeBinary, error) {
	if pin != "" {
		if !filepath.IsAbs(pin) {
			return OpenCodeBinary{}, fmt.Errorf(
				"opencode.binary %q is not an absolute path, and a pinned binary is never looked up on PATH: set it to the binary's absolute path in ~/.nightgauge/config.yaml, or remove it to run the opencode on PATH",
				pin)
		}
		fi, err := os.Stat(pin)
		switch {
		case err != nil:
			return OpenCodeBinary{}, fmt.Errorf("opencode.binary %s cannot be run: %v", pin, err)
		case !fi.Mode().IsRegular():
			return OpenCodeBinary{}, fmt.Errorf("opencode.binary %s is not a file", pin)
		case fi.Mode().Perm()&0o111 == 0:
			return OpenCodeBinary{}, fmt.Errorf("opencode.binary %s is not executable", pin)
		}
		return OpenCodeBinary{Path: filepath.Clean(pin), Pinned: true}, nil
	}
	path, err := lookPath(openCodeBinaryName)
	if err != nil {
		m, merr := openCodeManifest()
		if merr != nil {
			return OpenCodeBinary{}, errors.New("opencode is not on PATH: install it and put it on PATH, or pin it with opencode.binary in ~/.nightgauge/config.yaml")
		}
		return OpenCodeBinary{}, fmt.Errorf("opencode is not on PATH. %s", openCodeInstallRemedy(m, ""))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return OpenCodeBinary{}, fmt.Errorf("resolve the opencode on PATH: %w", err)
	}
	return OpenCodeBinary{Path: abs}, nil
}

// openCodeProbeTimeout bounds every probe spawn: the version read, each half
// of the self-test and the doctor's catalog probe. A variable only so a test
// can shorten it.
var openCodeProbeTimeout = 20 * time.Second

// openCodeProbeMaxOutput caps what a probe keeps of each stream. `debug
// config` prints a few kilobytes, and `models` a few hundred lines.
const openCodeProbeMaxOutput = 4 << 20

// openCodeDisableProjectConfig turns off OpenCode's project config: the
// opencode.json, opencode.jsonc and .opencode directories it looks for from
// the working directory up to the repository's root, and the plugins they
// name. A probe's directory is in no repository, so without it OpenCode 1.18.30
// looks in every directory above the probe's up to /, a world-writable /tmp
// included, and loads what it finds there as the operator
// (testdata/opencode-cli/README.md). A probe checks the per-run config alone,
// so every probe sets it. A stage run does not yet (openCodeDisableFlags),
// because it also hides the repository's AGENTS.md, and a probe has no
// repository.
const openCodeDisableProjectConfig = "OPENCODE_DISABLE_PROJECT_CONFIG"

// openCodeProbePlaceholder is the value a probe gives a provider variable in
// place of the credential (OpenCodeProviderVars). OpenCode loads a catalog
// provider when any one of its variables is set, whatever the value, and
// `opencode models` sends no request, so the listing is the one a dispatch
// holding the real value gets.
const openCodeProbePlaceholder = "nightgauge-probe-placeholder"

// OpenCodeProbe is the throwaway directory an opencode probe spawn runs in.
type OpenCodeProbe struct{ root string }

// NewOpenCodeProbe creates a probe directory under the system's temporary
// directory. Close removes it.
func NewOpenCodeProbe() (*OpenCodeProbe, error) {
	root, err := os.MkdirTemp("", "nightgauge-opencode-probe-")
	if err != nil {
		return nil, fmt.Errorf("opencode probe: %w", err)
	}
	for _, dir := range []string{"home", "tmp", "config", "data", "cache", "state"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("opencode probe: %w", err)
		}
	}
	return &OpenCodeProbe{root: root}, nil
}

// Root is the probe's directory. A per-run config built for a probe names it
// as its run root, so the files the config refers to are written inside it.
func (p *OpenCodeProbe) Root() string { return p.root }

// Close removes the probe's directory and everything a probe wrote in it.
func (p *OpenCodeProbe) Close() error { return os.RemoveAll(p.root) }

// OpenCodeProbeResult is how a probe spawn ended.
type OpenCodeProbeResult struct {
	// ExitCode is the process's exit code, -1 when a signal ended it.
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Run starts bin with args in the probe's directory and waits for it.
//
// content, when set, is OPENCODE_CONFIG_CONTENT, and files are the files it
// refers to (OpenCodeRunConfig.Files), written into the probe's directory
// first. The environment is built from nothing: PATH, the probe's HOME,
// TMPDIR and four XDG directories, the switches every spawn sets
// (openCodeDisableFlags), OPENCODE_DISABLE_MODELS_FETCH among them, and
// OPENCODE_DISABLE_PROJECT_CONFIG (openCodeDisableProjectConfig). No
// credential and no inherited OPENCODE_* variable reaches it.
// providerVars, when given, are variables OpenCode's catalog binds to a model
// provider (OpenCodeProviderVars), each set to a placeholder and never to its
// value; any other name is refused. It runs in its own process group with
// stdin closed, and the whole group is killed at openCodeProbeTimeout, when
// ctx is done, and again once it exits, so nothing it started outlives it.
// Once ctx is done it starts nothing (#1627).
//
// A non-zero exit is a result, not an error; an error is a process that could
// not start, ran past the timeout, was stopped by ctx, or printed more than
// openCodeProbeMaxOutput. An error ctx caused wraps ctx's error.
func (p *OpenCodeProbe) Run(ctx context.Context, bin string, args []string, content string, files map[string]string, providerVars ...string) (OpenCodeProbeResult, error) {
	label := "`" + strings.Join(append([]string{openCodeBinaryName}, args...), " ") + "`"
	if err := ctx.Err(); err != nil {
		return OpenCodeProbeResult{ExitCode: -1}, fmt.Errorf("%s not started: %w", label, err)
	}
	for _, name := range providerVars {
		if !openCodeProbeMaySet(name) {
			return OpenCodeProbeResult{ExitCode: -1}, fmt.Errorf("%s: a probe sets only a model provider's catalog variables, and %q is not one", label, name)
		}
	}
	if err := writeOpenCodeRunFiles(p.root, files); err != nil {
		return OpenCodeProbeResult{ExitCode: -1}, err
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + filepath.Join(p.root, "home"),
		"TMPDIR=" + filepath.Join(p.root, "tmp"),
	}
	for _, x := range openCodeXDGDirs {
		env = append(env, x.env+"="+filepath.Join(p.root, x.dir))
	}
	for _, flag := range openCodeDisableFlags {
		env = append(env, flag+"=1")
	}
	env = append(env, openCodeDisableProjectConfig+"=1")
	for _, name := range providerVars {
		env = append(env, name+"="+openCodeProbePlaceholder)
	}
	if content != "" {
		env = append(env, openCodeConfigContentEnvVar+"="+content)
	}

	runCtx, cancel := context.WithTimeout(ctx, openCodeProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, bin, args...)
	cmd.Dir = p.root
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	stdout := &openCodeProbeBuffer{max: openCodeProbeMaxOutput}
	stderr := &openCodeProbeBuffer{max: openCodeProbeMaxOutput}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	res := OpenCodeProbeResult{ExitCode: -1, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	var exitErr *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return res, fmt.Errorf("%s was stopped: %w", label, ctx.Err())
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return res, fmt.Errorf("%s ran past %s and was killed", label, openCodeProbeTimeout)
	case stdout.overflow || stderr.overflow:
		return res, fmt.Errorf("%s printed more than %d bytes", label, openCodeProbeMaxOutput)
	case errors.Is(err, exec.ErrWaitDelay):
		return res, fmt.Errorf("%s exited but left a process holding its output, which was killed", label)
	case err != nil && !errors.As(err, &exitErr):
		return res, fmt.Errorf("%s could not run: %w", label, err)
	}
	return res, nil
}

// openCodeProbeMaySet reports whether a probe may set name to the
// placeholder: a variable the bundled catalog binds to a model provider, and
// none of OpenCode's own, a provider base URL or a platform account's
// (openCodePlatformProviders), whose providers' loaders follow a credential
// chain of their own.
func openCodeProbeMaySet(name string) bool {
	return openCodeCatalogEnvNames[name] && !openCodePlatformEnvNames[name] &&
		!strings.HasPrefix(name, openCodeWithheldPrefix) && !slices.Contains(openCodeEndpointEnv, name)
}

// OpenCodeProviderVars returns, sorted, the variables OpenCode's bundled
// catalog binds to the provider model names that a dispatch to model keeps
// (those OpenCodeWithholdsEnv lets through, other than a platform account's),
// split into those lookup holds a non-empty value for and those it does not.
// OpenCode loads a catalog provider only when one of its variables is set, so
// a stage on a provider that has variables, none of them set, finds none of
// its models unless the per-run config declares the provider's block. It
// returns names only and reads no value beyond whether it is empty.
func OpenCodeProviderVars(model string, lookup func(string) (string, bool)) (set, unset []string) {
	for _, name := range openCodeCatalogEnv[openCodeDispatchProvider(model)] {
		if OpenCodeWithholdsEnv(model, name) || !openCodeProbeMaySet(name) {
			continue
		}
		if v, ok := lookup(name); ok && v != "" {
			set = append(set, name)
		} else {
			unset = append(unset, name)
		}
	}
	slices.Sort(set)
	slices.Sort(unset)
	return set, unset
}

// openCodeProbeBuffer keeps at most max bytes and notes that more arrived.
type openCodeProbeBuffer struct {
	bytes.Buffer
	max      int
	overflow bool
}

func (b *openCodeProbeBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); len(p) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(p[:room])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// openCodeVersionRE is the shape of what `opencode --version` prints.
var openCodeVersionRE = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)[0-9A-Za-z.+-]*$`)

// OpenCodeVersionOf runs `bin --version` as a probe under ctx and returns the
// version it prints. The output is never quoted back: a binary that prints
// something else is reported as printing no version.
func OpenCodeVersionOf(ctx context.Context, bin string) (string, error) {
	probe, err := NewOpenCodeProbe()
	if err != nil {
		return "", err
	}
	defer func() { _ = probe.Close() }()
	res, err := probe.Run(ctx, bin, []string{"--version"}, "", nil)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("`%s --version` exited %d", bin, res.ExitCode)
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(res.Stdout)), "\n")
	line = strings.TrimSpace(line)
	if !openCodeVersionRE.MatchString(line) {
		return "", fmt.Errorf("`%s --version` printed no version", bin)
	}
	return strings.TrimPrefix(line, "v"), nil
}

// compareOpenCodeVersions compares the MAJOR.MINOR.PATCH of two versions
// openCodeVersionRE accepts, -1, 0 or 1. A pre-release or build suffix does
// not count.
func compareOpenCodeVersions(a, b string) int {
	pa, pb := openCodeVersionRE.FindStringSubmatch(a), openCodeVersionRE.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		var x, y int
		if pa != nil {
			x, _ = strconv.Atoi(pa[i])
		}
		if pb != nil {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// OpenCodeVersionPolicy is where a binary's version stands against the compat
// manifest (ADR-022 § 20).
type OpenCodeVersionPolicy struct {
	Binary     OpenCodeBinary
	Version    string
	MinVersion string
	MaxTested  string
	// BelowFloor is set for a version below MinVersion under a warn floor,
	// which reports it and lets the dispatch run. Under a fail_closed floor,
	// the opencode manifest's, CheckOpenCodeVersion refuses it instead.
	BelowFloor bool
	// AboveMaxTested is set for a version newer than MaxTested: a dispatch
	// warns, refuses a model server the operator runs, and runs the
	// self-test before its first stage on the version.
	AboveMaxTested bool
}

// CheckOpenCodeVersion applies the compat manifest's version policy to bin,
// whose `--version` printed version or failed with versionErr. A version below
// a fail_closed floor, and one that could not be read, is refused with an
// *OpenCodeIncompatibleError naming both versions and the managed install.
// home, when set, makes the remediation's pin absolute.
func CheckOpenCodeVersion(bin OpenCodeBinary, version string, versionErr error, home string) (OpenCodeVersionPolicy, error) {
	m, err := openCodeManifest()
	if err != nil {
		return OpenCodeVersionPolicy{}, &OpenCodeIncompatibleError{
			Binary: bin.Path, Version: version,
			Reason:      err.Error(),
			Remediation: "Rebuild nightgauge from a tree whose internal/adaptercompat/manifests load",
		}
	}
	p := OpenCodeVersionPolicy{Binary: bin, Version: version, MinVersion: m.MinVersion, MaxTested: m.MaxTested}
	incompatible := func(reason string) error {
		return &OpenCodeIncompatibleError{
			Binary: bin.Path, Version: version, MinVersion: m.MinVersion, MaxTested: m.MaxTested,
			Reason: reason, Remediation: openCodeInstallRemedy(m, home),
		}
	}
	if versionErr != nil || version == "" {
		why := "it printed none"
		if versionErr != nil {
			why = versionErr.Error()
		}
		return p, incompatible(fmt.Sprintf(
			"the version of %s could not be read (%s), so it cannot be held to the minimum tested version %s", bin.Path, why, m.MinVersion))
	}
	if m.MinVersion != "" && compareOpenCodeVersions(version, m.MinVersion) < 0 {
		if m.FloorPolicy != adaptercompat.FloorFailClosed {
			p.BelowFloor = true
			return p, nil
		}
		return p, incompatible(fmt.Sprintf(
			"opencode %s (%s) is below the minimum tested version %s, and no OpenCode behaviour this adapter relies on was observed below it",
			version, bin.Path, m.MinVersion))
	}
	if m.MaxTested != "" && compareOpenCodeVersions(version, m.MaxTested) > 0 {
		p.AboveMaxTested = true
	}
	return p, nil
}

// openCodeCanaryRelax is nil in every production build. The #1639 canary leg
// exists to run the INSTALLED latest release of opencode through this exact
// refusal path, not just warn about it — otherwise the daily run can never
// tell "new and working" from "new and broken" for a model server endpoint.
// Only opencode_preflight_canary.go, gated behind the "canary" build tag that
// reaches nothing but `go test -tags canary` (scripts/adapter-canary.sh's own
// invocation; no production build ever adds that tag — see
// scripts/clean-install-e2e.sh and the cmd/nightgauge build), sets this at
// init(), and even then only relaxes once the explicit NIGHTGAUGE_CANARY=true
// signal is read from the environment at call time. A production binary
// therefore cannot reach the relaxation by any dispatch input: the var stays
// nil regardless of environment. TestOpenCodeAboveMaxTestedRefusesAnEndpoint
// EvenWithTheCanaryEnvSet (opencode_preflight_test.go, no build tag, run by
// the default `go test ./...`) proves it.
var openCodeCanaryRelax func(model string) bool

// OpenCodeEndpointAboveMaxTested refuses a dispatch of model to a model server
// the operator runs (a declared endpoint, or the lmstudio or ollama key) on a
// binary newer than max-tested (ADR-022 § 20, § Endpoints), and returns nil
// for any other model. Which provider keys a binary bundles, and so which
// endpoint ids are safe, is read from the max-tested binary's catalog, and no
// self-test can re-check it. The caller has established p.AboveMaxTested.
func OpenCodeEndpointAboveMaxTested(p OpenCodeVersionPolicy, model string, endpoints []OpenCodeEndpoint, home string) error {
	if openCodeCanaryRelax != nil && openCodeCanaryRelax(model) {
		fmt.Fprintf(os.Stderr, "[opencode] WARNING: canary relaxation lets opencode %s (%s) dispatch model %q above the max-tested %s: the self-test below still runs\n",
			p.Version, p.Binary.Path, model, p.MaxTested)
		return nil
	}
	key := openCodeDispatchProvider(model)
	if _, declared := findOpenCodeEndpoint(endpoints, key); !declared && !openCodeIsLocalKey(model) {
		return nil
	}
	m, _ := openCodeManifest()
	return &OpenCodeIncompatibleError{
		Binary: p.Binary.Path, Version: p.Version, MinVersion: p.MinVersion, MaxTested: p.MaxTested,
		Reason: fmt.Sprintf(
			"opencode %s (%s) is newer than the max-tested %s, and model %q runs on endpoint %s, a model server you run: which provider keys a build bundles, and so which endpoint ids are safe, is read from the max-tested build, so no endpoint is dispatched above it",
			p.Version, p.Binary.Path, p.MaxTested, model, key),
		Remediation: openCodeInstallRemedy(m, home),
	}
}

// openCodeSelfTestWorktree stands in for the worktree when the self-test asks
// BuildCommand which flags it emits, so --dir is always among them.
const openCodeSelfTestWorktree = "/nightgauge-self-test-worktree"

// openCodeSelfTestDir holds one file per passed self-test, named by the hash
// of the binary, its version and the per-run config it passed with.
func openCodeSelfTestDir(home string) string {
	return filepath.Join(home, ".nightgauge", "opencode", "self-test")
}

// openCodeSelfTestKey names a self-test: the binary, its version, and the
// per-run config with the probe's directory taken out of it, so the same
// dispatch on the same binary hashes the same wherever its probe ran.
func openCodeSelfTestKey(bin, version, content, probeRoot string) string {
	sum := sha256.Sum256([]byte(bin + "\x00" + version + "\x00" + strings.ReplaceAll(content, probeRoot, "<run-root>")))
	return hex.EncodeToString(sum[:])
}

// openCodeSelfTestPass is what a passed self-test's file records.
type openCodeSelfTestPass struct {
	Binary   string    `json:"binary"`
	Version  string    `json:"version"`
	Key      string    `json:"key"`
	PassedAt time.Time `json:"passed_at"`
}

func openCodeSelfTestPassed(home, key string) bool {
	_, err := os.Stat(filepath.Join(openCodeSelfTestDir(home), key+".pass"))
	return err == nil
}

// runOpenCodeSelfTest is the self-test a dispatch runs on a binary newer than
// max-tested before its first stage with this per-run config (ADR-022 § 20):
//
//   - `opencode debug config` under the per-run config a dispatch of opts gets,
//     built by BuildOpenCodeConfig as `nightgauge opencode config` builds it,
//     must exit 0 and print a merged config that holds every key the per-run
//     config sets (EvaluateOpenCodeDebugConfig). No model is called.
//   - `opencode run --help` must define every flag BuildCommand emits, and
//     list each value it passes among the option's choices
//     (CheckOpenCodeRunHelp).
//
// A pass is recorded under home and not repeated for the same binary,
// version and per-run config; a failure is an *OpenCodeIncompatibleError and
// is tried again on the next dispatch. A probe ctx stopped is neither: its
// error wraps ctx's, and nothing is recorded.
func (a *OpenCodeAdapter) runOpenCodeSelfTest(ctx context.Context, home string, p OpenCodeVersionPolicy, opts RunOptions, settings config.OpenCodeConfig) error {
	probe, err := NewOpenCodeProbe()
	if err != nil {
		return err
	}
	defer func() { _ = probe.Close() }()
	input, err := OpenCodeConfigInputFor(settings, opts, probe.Root(), os.LookupEnv)
	if err != nil {
		return err
	}
	built, err := BuildOpenCodeConfig(input)
	if err != nil {
		return err
	}
	key := openCodeSelfTestKey(p.Binary.Path, p.Version, built.Content, probe.Root())
	if home != "" && openCodeSelfTestPassed(home, key) {
		return nil
	}

	m, _ := openCodeManifest()
	failed := func(reason string) error {
		return &OpenCodeIncompatibleError{
			Binary: p.Binary.Path, Version: p.Version, MinVersion: p.MinVersion, MaxTested: p.MaxTested,
			Reason: fmt.Sprintf("opencode %s (%s) is newer than the max-tested %s, and its self-test failed: %s",
				p.Version, p.Binary.Path, p.MaxTested, reason),
			Remediation: openCodeInstallRemedy(m, home),
		}
	}
	probeFailed := func(err error) error {
		if ctx.Err() != nil {
			return err
		}
		return failed(err.Error())
	}
	redact := slices.Collect(maps.Values(built.Files))
	res, err := probe.Run(ctx, p.Binary.Path, []string{"debug", "config"}, built.Content, built.Files)
	if err != nil {
		return probeFailed(err)
	}
	if err := EvaluateOpenCodeDebugConfig(built.Content, res, redact); err != nil {
		return failed(err.Error())
	}
	_, argv, _ := a.BuildCommand(RunOptions{Model: opts.Model, WorktreeDir: openCodeSelfTestWorktree})
	help, err := probe.Run(ctx, p.Binary.Path, []string{"run", "--help"}, "", nil)
	if err != nil {
		return probeFailed(err)
	}
	if err := CheckOpenCodeRunHelp(help, argv); err != nil {
		return failed(err.Error())
	}

	if home != "" {
		pass := openCodeSelfTestPass{Binary: p.Binary.Path, Version: p.Version, Key: key, PassedAt: time.Now().UTC()}
		if err := writeOpenCodeStateJSON(filepath.Join(openCodeSelfTestDir(home), key+".pass"), pass); err != nil {
			fmt.Fprintf(os.Stderr, "[opencode] the self-test passed, but the pass could not be recorded, so the next dispatch runs it again: %v\n", err)
		}
	}
	fmt.Fprintf(os.Stderr, "[opencode] self-test passed on opencode %s (%s): it accepts the per-run config and every flag the adapter passes\n",
		p.Version, p.Binary.Path)
	return nil
}

// EvaluateOpenCodeDebugConfig decides whether `opencode debug config`, run
// with content as OPENCODE_CONFIG_CONTENT, accepted it: the exit code must be
// 0, and the merged config it prints must hold every key content sets, with
// the value content sets.
//
// The exit code alone is not enough. Observed on 1.18.30
// (internal/doctor/testdata/opencode-capture), a value of the wrong type
// exits 1, but an unknown key exits 0 and is dropped without a word, so a key
// a newer version stops accepting would pass. A value that is an {env:...} or
// {file:...} reference is resolved in the output, so only its key is
// required.
//
// The error names keys, never a value, and quotes at most two lines of
// OpenCode's stderr with every string in redact removed from them, such as an
// endpoint's base URL.
func EvaluateOpenCodeDebugConfig(content string, res OpenCodeProbeResult, redact []string) error {
	if res.ExitCode != 0 {
		return fmt.Errorf("`opencode debug config` exited %d on the per-run config: %s",
			res.ExitCode, openCodeStderrSummary(res.Stderr, redact))
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(content), &want); err != nil {
		return fmt.Errorf("the per-run config is not a JSON object: %w", err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Stdout, &got); err != nil {
		return errors.New("`opencode debug config` exited 0 but printed no JSON config")
	}
	var lost []string
	openCodeConfigDiff(want, got, nil, &lost)
	if len(lost) == 0 {
		return nil
	}
	slices.Sort(lost)
	shown := lost
	if len(shown) > 10 {
		shown = append(slices.Clone(shown[:10]), fmt.Sprintf("and %d more", len(lost)-10))
	}
	return fmt.Errorf("`opencode debug config` dropped or changed %d key(s) of the per-run config, so this version would not run a stage the way the config says: %s",
		len(lost), strings.Join(shown, ", "))
}

// openCodeConfigDiff appends to lost the path of every key of want that got
// lacks or holds another value for. An empty object in want sets no key.
func openCodeConfigDiff(want, got any, path []string, lost *[]string) {
	wm, isObject := want.(map[string]any)
	if isObject {
		if len(wm) == 0 {
			return
		}
		gm, _ := got.(map[string]any)
		for k, wv := range wm {
			p := append(slices.Clone(path), k)
			gv, ok := gm[k]
			if !ok {
				if sub, empty := wv.(map[string]any); empty && len(sub) == 0 {
					continue
				}
				*lost = append(*lost, strings.Join(p, "."))
				continue
			}
			openCodeConfigDiff(wv, gv, p, lost)
		}
		return
	}
	if s, ok := want.(string); ok && (strings.Contains(s, "{env:") || strings.Contains(s, "{file:")) {
		return
	}
	if !reflect.DeepEqual(want, got) {
		*lost = append(*lost, strings.Join(path, "."))
	}
}

// openCodeANSIRE matches a terminal colour sequence.
var openCodeANSIRE = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// openCodeStderrSummary is at most the first two non-empty lines of stderr,
// colour removed, each at most 200 bytes, with every string in redact (and
// the host of each that is a URL) replaced.
func openCodeStderrSummary(stderr []byte, redact []string) string {
	var lines []string
	for _, line := range strings.Split(openCodeANSIRE.ReplaceAllString(string(stderr), ""), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 200 {
			line = line[:200] + "..."
		}
		lines = append(lines, line)
		if len(lines) == 2 {
			break
		}
	}
	if len(lines) == 0 {
		return "it printed nothing on stderr"
	}
	return openCodeRedact(strings.Join(lines, " "), redact)
}

// openCodeRedact replaces every string in secrets, and the host of each that
// is a URL, with "[redacted]".
func openCodeRedact(text string, secrets []string) string {
	var olds []string
	for _, s := range secrets {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		olds = append(olds, s)
		if u, err := url.Parse(s); err == nil && u.Host != "" {
			olds = append(olds, u.Host)
			if h := u.Hostname(); h != "" {
				olds = append(olds, h)
			}
		}
	}
	// Longest first, so a URL is replaced whole before its host.
	slices.SortFunc(olds, func(a, b string) int { return len(b) - len(a) })
	for _, s := range olds {
		text = strings.ReplaceAll(text, s, "[redacted]")
	}
	return text
}

// openCodeHelpChoicesRE and openCodeHelpChoiceRE read an option's declared
// choices from yargs help: [choices: "default", "json"].
var (
	openCodeHelpChoicesRE = regexp.MustCompile(`\[choices: ([^\]]*)\]`)
	openCodeHelpChoiceRE  = regexp.MustCompile(`"([^"]*)"`)
)

// parseOpenCodeRunHelp reads the options section of `opencode run --help`
// (testdata/opencode-cli/run-help.txt holds 1.18.30's): each option's names,
// such as -m and --model, mapped to its declared choices, nil when it
// declares none. A yargs option wraps its type and choices onto the next line
// when they do not fit, so a line that starts no option belongs to the one
// before it.
func parseOpenCodeRunHelp(help string) map[string][]string {
	options := map[string][]string{}
	inOptions := false
	var current []string
	var text strings.Builder
	flush := func() {
		if len(current) == 0 {
			return
		}
		var choices []string
		if m := openCodeHelpChoicesRE.FindStringSubmatch(text.String()); m != nil {
			for _, c := range openCodeHelpChoiceRE.FindAllStringSubmatch(m[1], -1) {
				choices = append(choices, c[1])
			}
		}
		for _, name := range current {
			options[name] = choices
		}
		current = nil
		text.Reset()
	}
	for _, line := range strings.Split(help, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "Options:":
			inOptions = true
			continue
		case !inOptions:
			continue
		case strings.HasPrefix(trimmed, "-"):
			flush()
			spec, rest, _ := strings.Cut(trimmed, "  ")
			for _, name := range strings.Split(spec, ",") {
				if name = strings.TrimSpace(name); strings.HasPrefix(name, "-") {
					current = append(current, name)
				}
			}
			text.WriteString(rest)
		case trimmed == "":
			flush()
		default:
			text.WriteString(" " + trimmed)
		}
	}
	flush()
	return options
}

// CheckOpenCodeRunHelp checks `opencode run --help` against argv, the
// arguments BuildCommand passes: the help must exit 0, define every flag argv
// holds, and list each value argv gives an option with declared choices among
// them. A flag the binary no longer defines would make `run` exit 1 and print
// its help instead of running the stage.
//
// 1.18.30 prints its help on stderr and nothing on stdout
// (testdata/opencode-cli/README.md), so the options are read from stdout when
// it holds them, and otherwise from stderr.
func CheckOpenCodeRunHelp(res OpenCodeProbeResult, argv []string) error {
	if res.ExitCode != 0 {
		return fmt.Errorf("`opencode run --help` exited %d", res.ExitCode)
	}
	options := parseOpenCodeRunHelp(string(res.Stdout))
	if len(options) == 0 {
		options = parseOpenCodeRunHelp(string(res.Stderr))
	}
	if len(options) == 0 {
		return errors.New("`opencode run --help` printed no options on stdout or stderr")
	}
	var missing, refused []string
	for i, arg := range argv {
		if i == 0 && arg == "run" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			continue
		}
		choices, ok := options[arg]
		if !ok {
			missing = append(missing, arg)
			continue
		}
		if len(choices) > 0 && i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "-") && !slices.Contains(choices, argv[i+1]) {
			refused = append(refused, fmt.Sprintf("%s %s (choices: %s)", arg, argv[i+1], strings.Join(choices, ", ")))
		}
	}
	var problems []string
	if len(missing) > 0 {
		problems = append(problems, "`opencode run` does not define "+strings.Join(missing, ", ")+", which the adapter passes")
	}
	if len(refused) > 0 {
		problems = append(problems, "`opencode run` does not accept "+strings.Join(refused, "; "))
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// OpenCodeMachineConfigRefusals are the refusals PrepareOpenCodeRun(req) makes
// from the machine rather than the stage, before it creates anything: none
// while req.Settings opts into the operator's own OpenCode config
// (opencode.inherit_user_config), and otherwise the machine's managed
// OpenCode config (openCodeManagedConfigRefusal), whose files
// req.ManagedConfigFiles replaces as it does for PrepareOpenCodeRun. Only
// req.Settings, req.GOOS and req.ManagedConfigFiles are read. The refusal
// names the files it found and reads none of them.
//
// $HOME/.opencode no longer refuses a dispatch: a non-inheriting run gets its
// own per-run HOME (OpenCodeIsolationEnv), which never contains .opencode, so
// the condition the old refusal checked can no longer be observed true.
//
// The doctor reports the refusal, so its opencode row, and cap recovery with
// it, never calls usable a machine that refuses every dispatch.
// PrepareOpenCodeRun makes the same check on the same fields;
// TestOpenCodeMachineConfigRefusalsAreTheDispatchs holds the two together.
func OpenCodeMachineConfigRefusals(req OpenCodeRunRequest) []error {
	if req.Settings.InheritUserConfig {
		return nil
	}
	managed := req.ManagedConfigFiles
	if managed == nil {
		managed = openCodeManagedConfigFiles(req.GOOS, openCodeUsername())
	}
	var refusals []error
	if err := openCodeManagedConfigRefusal(managed); err != nil {
		refusals = append(refusals, err)
	}
	return refusals
}

// OpenCodeDispatchRecord is the binary and version the last opencode dispatch
// on this machine was checked against. The doctor compares the binary it
// resolves now with it, which is how an opencode that changed under the
// pipeline, as a PATH install does when its TUI updates itself, is reported.
type OpenCodeDispatchRecord struct {
	Binary     string    `json:"binary"`
	Version    string    `json:"version"`
	RecordedAt time.Time `json:"recorded_at"`
}

// OpenCodeDispatchRecordPath is where the record is kept, beside the per-run
// roots.
func OpenCodeDispatchRecordPath(home string) string {
	return filepath.Join(home, ".nightgauge", "opencode", "last-dispatch.json")
}

// RecordOpenCodeDispatch replaces the record.
func RecordOpenCodeDispatch(home string, rec OpenCodeDispatchRecord) error {
	return writeOpenCodeStateJSON(OpenCodeDispatchRecordPath(home), rec)
}

// ReadOpenCodeDispatchRecord returns the record, and false when there is none.
func ReadOpenCodeDispatchRecord(home string) (OpenCodeDispatchRecord, bool, error) {
	data, err := os.ReadFile(OpenCodeDispatchRecordPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return OpenCodeDispatchRecord{}, false, nil
	}
	if err != nil {
		return OpenCodeDispatchRecord{}, false, err
	}
	var rec OpenCodeDispatchRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return OpenCodeDispatchRecord{}, false, fmt.Errorf("%s is not a dispatch record: %w", OpenCodeDispatchRecordPath(home), err)
	}
	return rec, true, nil
}

// writeOpenCodeStateJSON writes v as JSON to path, mode 0600 in a 0700
// directory, through a temporary file renamed into place.
func writeOpenCodeStateJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".write-*")
	if err != nil {
		return err
	}
	_, writeErr := tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// checkVersionPolicy is ADR-022 § 20 at dispatch, reached from PreDispatch
// once the enable gate is open, so a refusal spawns no stage and creates
// nothing:
//
//   - the binary is opencode.binary's pin, which must be absolute and
//     executable, or the opencode on PATH (ResolveOpenCodeBinary);
//   - below the compat manifest's floor, or with a version that cannot be
//     read, the dispatch is refused as adapter_incompatible, naming both
//     versions and the managed install (CheckOpenCodeVersion);
//   - newer than max-tested, a stage on a model server the operator runs is
//     refused the same way, and any other stage warns and runs only once the
//     self-test has passed for this binary, version and per-run config;
//   - the binary and version the dispatch passed with are recorded for the
//     doctor's drift check.
//
// Every probe runs under ctx, the stage's context: once it is done no probe
// starts, a running one is killed, and the error wraps ctx's rather than
// calling the binary incompatible (#1627).
func (a *OpenCodeAdapter) checkVersionPolicy(ctx context.Context, opts RunOptions) error {
	settings, err := a.loadSettings(opts.WorktreeDir)
	if err != nil {
		return err
	}
	bin, err := ResolveOpenCodeBinary(settings.Binary, exec.LookPath)
	if err != nil {
		return err
	}
	// Without a home directory there is nowhere to keep a self-test pass or
	// the dispatch record, so the self-test runs every time and nothing is
	// recorded; neither is a reason to refuse the dispatch.
	home, _ := os.UserHomeDir()
	version, versionErr := OpenCodeVersionOf(ctx, bin.Path)
	if versionErr != nil && ctx.Err() != nil {
		return versionErr
	}
	p, err := CheckOpenCodeVersion(bin, version, versionErr, home)
	if err != nil {
		return err
	}
	if p.BelowFloor {
		fmt.Fprintf(os.Stderr, "[opencode] WARNING: opencode %s (%s) is below the minimum tested version %s\n", p.Version, bin.Path, p.MinVersion)
	}
	if p.AboveMaxTested {
		endpoints, err := OpenCodeEndpoints(settings)
		if err != nil {
			return err
		}
		if err := OpenCodeEndpointAboveMaxTested(p, opts.Model, endpoints, home); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[opencode] WARNING: opencode %s (%s) is newer than the max-tested %s: a stage runs on it only once a self-test of its per-run config and run flags has passed on this version\n",
			p.Version, bin.Path, p.MaxTested)
		if err := a.runOpenCodeSelfTest(ctx, home, p, opts, settings); err != nil {
			return err
		}
	}
	if home != "" {
		rec := OpenCodeDispatchRecord{Binary: bin.Path, Version: p.Version, RecordedAt: time.Now().UTC()}
		if err := RecordOpenCodeDispatch(home, rec); err != nil {
			fmt.Fprintf(os.Stderr, "[opencode] the version this dispatch runs could not be recorded, so the doctor cannot compare the next opencode with it: %v\n", err)
		}
	}
	return nil
}

// OpenCodeReadinessTimeout bounds each request the readiness probe sends.
const OpenCodeReadinessTimeout = 2 * time.Second

// openCodeReadinessMaxBody bounds a readiness response. A model listing is a
// few kilobytes.
const openCodeReadinessMaxBody = 1 << 20

// OpenCodeEndpointTarget is one model server ProbeOpenCodeEndpoint checks.
type OpenCodeEndpointTarget struct {
	// ID is the endpoint id, the OpenCode provider key a stage names on -m.
	// It is the only name any result or message gives the endpoint.
	ID string
	// Kind is lm-studio, ollama or openai-compatible.
	Kind string
	// BaseURL is the server's OpenAI-compatible API root as the machine-tier
	// config declares it. The probe requests it and never reports it.
	BaseURL string
	// Legacy is true only for the one endpoint the flat machine-tier keys
	// describe (OpenCodeEndpoint.Legacy). It alone gets the LM-Studio/Ollama
	// -specific probe; every declared opencode.endpoints[] entry, whatever
	// its Kind label, gets the generic OpenAI-compatible probe (2026-09-20
	// scope narrowing, ADR-022 § Endpoints).
	Legacy bool
}

// OpenCodeEndpointReadiness is what the readiness probe found at one
// endpoint. It names the endpoint by id and holds no address.
type OpenCodeEndpointReadiness struct {
	Endpoint string `json:"endpoint"`
	Kind     string `json:"kind"`
	// Model is the model id on the endpoint (the -m value after its key),
	// empty when only the server was checked.
	Model     string `json:"model,omitempty"`
	Reachable bool   `json:"reachable"`
	// Loaded is whether the server has Model loaded; nil when unknown.
	Loaded *bool `json:"loaded,omitempty"`
	// LoadedContext is the context the server has loaded Model with (LM
	// Studio's loaded_context_length, not its maximum), or will load it with
	// (Ollama's num_ctx); 0 when unknown.
	LoadedContext int `json:"loaded_context,omitempty"`
	// InjectedContext is the limit.context the per-run config gives Model
	// on the endpoint: the machine-tier override, clamped to the loaded
	// window, or the window discovered from the server. 0 when the caller
	// knows none, which the probe then does not compare.
	InjectedContext int `json:"injected_context"`
	// Ready is true when a stage on Model can run: the server answers and
	// has the model, loaded where the server does not load it on demand.
	Ready bool `json:"ready"`
	// Problem says why the endpoint is not ready, and Warning what does not
	// stop a stage but will hurt it, such as a context limit above the loaded
	// window.
	Problem string `json:"problem,omitempty"`
	Warning string `json:"warning,omitempty"`
	// Slots is the endpoint's declared capacity (OpenCodeEndpoint.
	// MaxConcurrency), never measured or probed. 0 means not declared.
	Slots int `json:"slots,omitempty"`
}

// ProbeOpenCodeEndpoint checks one model server the operator runs: whether it
// answers, whether model (the id after the endpoint's key; "" to check only
// the server) is on it and loaded, the context it has loaded, and whether
// injectedContext, the limit.context the per-run config gives model on the
// endpoint (0 when the caller knows none), fits that context. It is one
// endpoint per call, so a caller with
// several endpoints (#1678) probes each.
//
// Only the server behind target.BaseURL is requested, with no credential, no
// proxy, no redirect followed, no retry, and OpenCodeReadinessTimeout per
// request. LM Studio is read from GET /api/v0/models (state and
// loaded_context_length); Ollama from POST /api/show (num_ctx) and GET
// /api/ps (whether the model is loaded). client nil uses such a client.
func ProbeOpenCodeEndpoint(client *http.Client, target OpenCodeEndpointTarget, model string, injectedContext int) OpenCodeEndpointReadiness {
	r := OpenCodeEndpointReadiness{Endpoint: target.ID, Kind: target.Kind, Model: model, InjectedContext: injectedContext}
	if client == nil {
		client = openCodeReadinessClient()
	}
	u, err := url.Parse(target.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		r.Problem = fmt.Sprintf("endpoint %s has no http or https base_url to probe", target.ID)
		return r
	}
	root := u.Scheme + "://" + u.Host
	switch {
	case !target.Legacy:
		// Every declared opencode.endpoints[] entry gets the generic
		// OpenAI-compatible probe regardless of its Kind label: lm-studio and
		// ollama are optional labels there, with no protocol-specific probe
		// of their own (2026-09-20 scope narrowing, ADR-022 § Endpoints).
		probeOpenAICompatible(client, root, target.ID, &r)
	case target.Kind == "lm-studio":
		probeLMStudio(client, root, target.ID, &r)
	case target.Kind == "ollama":
		probeOllama(client, root, target.ID, &r)
	default:
		r.Problem = fmt.Sprintf("endpoint %s is of kind %q, which the readiness probe does not know", target.ID, target.Kind)
	}
	return r
}

// probeOpenAICompatible checks a generic OpenAI-compatible server: GET
// {root}/models. Reachable is set on any response, Ready on HTTP 200 (and,
// when a model is named, on that model appearing in the listing). Unlike
// probeLMStudio it never reads a loaded-context field: no standard
// OpenAI-compatible /models response carries one, and the 2026-09-20 scope
// narrowing drops the loaded-vs-declared-context comparison for this probe
// rather than half-trust a server-reported number (LoadedContext stays 0).
func probeOpenAICompatible(client *http.Client, root, id string, r *OpenCodeEndpointReadiness) {
	status, body, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/models", nil)
	switch {
	case status == 0:
		r.Problem = fmt.Sprintf("endpoint %s is not answering (%s): start the model server, or correct its base_url", id, failure)
		return
	case failure != "":
		r.Reachable = true
		r.Problem = fmt.Sprintf("endpoint %s answered, but %s", id, failure)
		return
	case status != http.StatusOK:
		r.Reachable = true
		r.Problem = fmt.Sprintf("endpoint %s answered its model listing with HTTP %d: it may not be an OpenAI-compatible server, or its API may have changed", id, status)
		return
	}
	r.Reachable = true
	if r.Model == "" {
		r.Ready = true
		return
	}
	var listing struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		r.Problem = fmt.Sprintf("endpoint %s answered its model listing with something other than the OpenAI-compatible JSON shape", id)
		return
	}
	for _, m := range listing.Data {
		if m.ID == r.Model {
			loaded := true
			r.Loaded = &loaded
			r.Ready = true
			return
		}
	}
	r.Problem = fmt.Sprintf("model %s is not on endpoint %s's model listing", r.Model, id)
}

// openCodeReadinessClient requests only the URL it is given: no proxy from
// the environment, and no redirect followed.
func openCodeReadinessClient() *http.Client {
	return &http.Client{
		Timeout: OpenCodeReadinessTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DialContext:       (&net.Dialer{Timeout: OpenCodeReadinessTimeout}).DialContext,
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// openCodeReadinessRequest sends one request and returns the status and body.
// A transport failure is described by its kind alone, because Go's error text
// quotes the URL.
func openCodeReadinessRequest(client *http.Client, method, target string, body io.Reader) (int, []byte, string) {
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return 0, nil, "the request could not be built"
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, openCodeTransportFailure(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, openCodeReadinessMaxBody+1))
	if err != nil {
		return resp.StatusCode, nil, "the response could not be read"
	}
	if len(data) > openCodeReadinessMaxBody {
		return resp.StatusCode, nil, "the response was larger than a model listing can be"
	}
	return resp.StatusCode, data, ""
}

// openCodeTransportFailure names the kind of a transport error without its
// text.
func openCodeTransportFailure(err error) string {
	var netErr net.Error
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused"
	case errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Sprintf("no answer within %s", OpenCodeReadinessTimeout)
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return "no route to the host"
	default:
		return "the request failed"
	}
}

// lmStudioModel is one entry of LM Studio's GET /api/v0/models.
type lmStudioModel struct {
	ID    string `json:"id"`
	State string `json:"state"`
	// LoadedContextLength is the context the model is loaded with, which is
	// what a session has; max_context_length is only what it could be.
	LoadedContextLength int `json:"loaded_context_length"`
}

func probeLMStudio(client *http.Client, root, id string, r *OpenCodeEndpointReadiness) {
	status, body, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/api/v0/models", nil)
	switch {
	case status == 0:
		r.Problem = fmt.Sprintf("endpoint %s is not answering (%s): start LM Studio's server, or correct opencode.base_url", id, failure)
		return
	case failure != "":
		r.Reachable = true
		r.Problem = fmt.Sprintf("endpoint %s answered, but %s", id, failure)
		return
	case status != http.StatusOK:
		r.Reachable = true
		r.Problem = fmt.Sprintf("endpoint %s answered its model listing with HTTP %d: it may not be LM Studio, or its API may have changed", id, status)
		return
	}
	r.Reachable = true
	var listing struct {
		Data []lmStudioModel `json:"data"`
	}
	if err := json.Unmarshal(body, &listing); err != nil {
		r.Problem = fmt.Sprintf("endpoint %s answered its model listing with something other than LM Studio's JSON", id)
		return
	}
	if r.Model == "" {
		r.Ready = true
		return
	}
	i := slices.IndexFunc(listing.Data, func(m lmStudioModel) bool { return m.ID == r.Model })
	if i < 0 {
		r.Problem = fmt.Sprintf("model %s is not on endpoint %s: download it with `lms get %s`", r.Model, id, r.Model)
		return
	}
	loaded := listing.Data[i].State == "loaded"
	r.Loaded = &loaded
	if !loaded {
		// Loaded on demand, LM Studio would use its own default context, not
		// the limit the per-run config gives OpenCode.
		load := "lms load " + r.Model
		if r.InjectedContext > 0 {
			load += " --context-length " + strconv.Itoa(r.InjectedContext)
		}
		r.Problem = fmt.Sprintf("model %s is not loaded on endpoint %s: load it with `%s`", r.Model, id, load)
		return
	}
	r.LoadedContext = listing.Data[i].LoadedContextLength
	r.Ready = true
	r.Warning = openCodeContextWarning(id, r.Model, r.InjectedContext, r.LoadedContext)
}

func probeOllama(client *http.Client, root, id string, r *OpenCodeEndpointReadiness) {
	if r.Model == "" {
		status, _, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/api/tags", nil)
		if status == 0 {
			r.Problem = fmt.Sprintf("endpoint %s is not answering (%s): start Ollama, or correct opencode.base_url", id, failure)
			return
		}
		r.Reachable = true
		r.Ready = status == http.StatusOK
		if !r.Ready {
			r.Problem = fmt.Sprintf("endpoint %s answered its model listing with HTTP %d: it may not be Ollama", id, status)
		}
		return
	}
	payload, _ := json.Marshal(map[string]string{"model": r.Model})
	status, body, failure := openCodeReadinessRequest(client, http.MethodPost, root+"/api/show", bytes.NewReader(payload))
	if status == 0 {
		r.Problem = fmt.Sprintf("endpoint %s is not answering (%s): start Ollama, or correct opencode.base_url", id, failure)
		return
	}
	r.Reachable = true
	switch {
	case status == http.StatusNotFound:
		r.Problem = fmt.Sprintf("model %s is not on endpoint %s: pull it with `ollama pull %s`", r.Model, id, r.Model)
		return
	case failure != "":
		r.Problem = fmt.Sprintf("endpoint %s answered, but %s", id, failure)
		return
	case status != http.StatusOK:
		r.Problem = fmt.Sprintf("endpoint %s answered its model details with HTTP %d: it may not be Ollama", id, status)
		return
	}
	var show struct {
		Parameters string `json:"parameters"`
	}
	_ = json.Unmarshal(body, &show)
	numCtx := 0
	if m := ollamaNumCtxRE.FindStringSubmatch(show.Parameters); m != nil {
		numCtx, _ = strconv.Atoi(m[1])
	}

	// Ollama loads a model on its first request, so a model that is not
	// loaded does not stop a stage; the context it loads with does matter.
	if status, body, _ := openCodeReadinessRequest(client, http.MethodGet, root+"/api/ps", nil); status == http.StatusOK {
		var ps struct {
			Models []struct {
				Name          string `json:"name"`
				Model         string `json:"model"`
				ContextLength int    `json:"context_length"`
			} `json:"models"`
		}
		if json.Unmarshal(body, &ps) == nil {
			loaded := false
			for _, m := range ps.Models {
				if m.Name == r.Model || m.Model == r.Model || m.Name == r.Model+":latest" {
					loaded = true
					if m.ContextLength > 0 {
						r.LoadedContext = m.ContextLength
					}
				}
			}
			r.Loaded = &loaded
		}
	}
	if r.LoadedContext == 0 {
		r.LoadedContext = numCtx
	}
	r.Ready = true
	if r.LoadedContext == 0 {
		limit := "the context limit a dispatch gives OpenCode"
		if r.InjectedContext > 0 {
			limit = fmt.Sprintf("the %d-token context limit a dispatch gives OpenCode", r.InjectedContext)
		}
		r.Warning = fmt.Sprintf("endpoint %s sets no num_ctx for %s, so Ollama loads it with its own default context, which can be far below %s: set num_ctx for the model, or OLLAMA_CONTEXT_LENGTH, at or above it",
			id, r.Model, limit)
		return
	}
	r.Warning = openCodeContextWarning(id, r.Model, r.InjectedContext, r.LoadedContext)
}

// ollamaNumCtxRE reads num_ctx from the parameters /api/show returns, one
// "name value" pair per line.
var ollamaNumCtxRE = regexp.MustCompile(`(?m)^\s*num_ctx\s+(\d+)\s*$`)

// openCodeContextWarning is the warning for an injected context limit larger
// than the context the endpoint has loaded, or "" when it fits or either is
// unknown. The injected limit is the one a dispatch resolved, which is
// already clamped to the window discovery saw (resolveLimit), so it is larger
// only when the server's window is not the one the dispatch resolved against.
// An unknown injected limit is no finding here: a dispatch whose limits do
// not resolve is refused, and that refusal is the finding.
func openCodeContextWarning(id, model string, injected, loaded int) string {
	if injected <= 0 || loaded <= 0 || injected <= loaded {
		return ""
	}
	return fmt.Sprintf("the %d-token context limit a dispatch gives OpenCode for %s is larger than the %d tokens endpoint %s has loaded it with: OpenCode compacts only past the limit, so the server runs out of context first; set opencode.limit.context at or below %d, or load the model with a larger context",
		injected, model, loaded, id, loaded)
}
