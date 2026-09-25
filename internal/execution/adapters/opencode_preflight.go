package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
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
// be read. A version newer than max-tested is never refused (ADR-022 § 20,
// 2026-09-25 amendment).
type OpenCodeIncompatibleError struct {
	// Binary is the binary the dispatch would spawn.
	Binary string
	// Version is its version, "" when it could not be read.
	Version string
	// MinVersion and MaxTested are the compat manifest's. MaxTested is only
	// the newest version Nightgauge was verified against, never a ceiling.
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

// openCodeProbeTimeout bounds every probe spawn: the version read and the
// doctor's catalog probe. A variable only so a test can shorten it.
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
	return p, nil
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
//   - newer than max-tested is not a condition at all: max-tested only says
//     how far Nightgauge was verified, and a newer build dispatches exactly
//     like a tested one, with no warning (ADR-022 § 20, 2026-09-25 amendment);
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
	// Without a home directory there is nowhere to keep the dispatch record,
	// which is not a reason to refuse the dispatch.
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
	// APIKeyEnv names the environment variable holding the endpoint's
	// credential (OpenCodeEndpoint.APIKeyEnv), or is empty. When set, the
	// generic probe sends its value as a Bearer token, the same credential a
	// run sends (#2158); the value is never reported.
	APIKeyEnv string
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
	// SlotsInUse is how many of those slots a running scheduler's
	// endpoint-aware dispatch holds right now (#1679), read from the ledger
	// it publishes (OpenCodeEndpointSlots); nil when no live scheduler has
	// published one.
	SlotsInUse *int `json:"slots_in_use,omitempty"`
}

// ProbeOpenCodeEndpoint checks one model server the operator runs: whether it
// answers, whether model (the id after the endpoint's key; "" to check only
// the server) is on it and loaded, the context it has loaded, and whether
// injectedContext, the limit.context the per-run config gives model on the
// endpoint (0 when the caller knows none), fits that context. It is one
// endpoint per call, so a caller with
// several endpoints (#1678) probes each.
//
// Only the server behind target.BaseURL is requested, with no proxy, no redirect followed, no retry, and OpenCodeReadinessTimeout per
// request, and with no credential except the endpoint's own api_key_env on
// the generic probe (#2158). LM Studio is read from GET /api/v0/models (state and
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
		// The generic probe addresses the API under base_url's own path
		// (".../v1/models"), unlike the legacy probes, which read the
		// server's native API at its root (#2158).
		apiRoot := root + strings.TrimRight(u.Path, "/")
		bearer := ""
		if target.APIKeyEnv != "" {
			bearer = os.Getenv(target.APIKeyEnv)
			if bearer == "" {
				r.Problem = fmt.Sprintf("endpoint %s declares api_key_env %s, but that variable is not set in this environment", target.ID, target.APIKeyEnv)
				return r
			}
		}
		probeOpenAICompatible(client, apiRoot, bearer, target.ID, &r)
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
func probeOpenAICompatible(client *http.Client, root, bearer, id string, r *OpenCodeEndpointReadiness) {
	status, body, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/models", nil, bearer)
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
// bearer, when non-empty, is sent as the Authorization header and never
// appears in any returned string.
func openCodeReadinessRequest(client *http.Client, method, target string, body io.Reader, bearer string) (int, []byte, string) {
	req, err := http.NewRequest(method, target, body)
	if err != nil {
		return 0, nil, "the request could not be built"
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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
	status, body, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/api/v0/models", nil, "")
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
		status, _, failure := openCodeReadinessRequest(client, http.MethodGet, root+"/api/tags", nil, "")
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
	status, body, failure := openCodeReadinessRequest(client, http.MethodPost, root+"/api/show", bytes.NewReader(payload), "")
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
	if status, body, _ := openCodeReadinessRequest(client, http.MethodGet, root+"/api/ps", nil, ""); status == http.StatusOK {
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
