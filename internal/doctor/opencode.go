package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// The opencode adapter's doctor checks (#1627, ADR-022 § 17, § 20,
// § Endpoints).
//
// The row answers what a dispatch would meet on this machine, in the order a
// dispatch meets it: the experimental enable gate, the machine-tier
// `opencode:` block and the per-run config it gives opencode.model, limits
// discovered from the server included, the binary (opencode.binary's pin or
// the opencode on PATH), the compat manifest's version policy, the catalog
// `opencode models` lists under the per-run config, the machine-wide managed
// OpenCode config a run cannot be isolated from, and the readiness of every
// model server the block declares, against the context limit a dispatch
// gives OpenCode. It also prints what a run is isolated into, the offline
// posture the per-run config sets, stored logins OpenCode holds for
// Anthropic, and whether the binary changed since the last dispatch.
//
// A blocking finding makes the row not OK, which is also what cap recovery
// reads (orchestrator.AdapterUsableForCapHop calls CheckAdapters): the
// remediation of each is joined into the row's. Every other finding is a
// warning, which degrades the doctor's verdict without failing the adapter.
//
// The version policy, the binary pin, the probe spawns, the machine config
// refusals and the endpoint readiness probe are the adapter's own functions
// (adapters.CheckOpenCodeVersion, ResolveOpenCodeBinary, OpenCodeProbe,
// OpenCodeMachineConfigRefusals, ProbeOpenCodeEndpoint), the ones its
// PreDispatch and PrepareRunRoot enforce, so the doctor and a dispatch cannot
// disagree. A model server is named by its endpoint id and never by its
// address.

// OpenCodeHealth is the opencode adapter's section of its doctor row.
type OpenCodeHealth struct {
	// MaxTested and FloorPolicy are the compat manifest's; the floor is the
	// row's MinVersion.
	MaxTested   string `json:"max_tested"`
	FloorPolicy string `json:"floor_policy"`
	// Enabled is whether NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1 is set. While it
	// is not, no other check runs: nothing is dispatched to check.
	Enabled bool `json:"enabled"`
	// Pinned is whether opencode.binary pins the binary.
	Pinned bool `json:"pinned"`
	// AboveMaxTested is set when the binary is newer than MaxTested.
	AboveMaxTested bool `json:"above_max_tested,omitempty"`
	// LastDispatchVersion is the version the last dispatch on this machine
	// was checked against, when one was recorded.
	LastDispatchVersion string `json:"last_dispatch_version,omitempty"`
	// Dirs are the OpenCode directories a pipeline run uses.
	Dirs *OpenCodeDirs `json:"dirs,omitempty"`
	// Offline describes the offline posture of the per-run config. It is a
	// statement about configuration only.
	Offline string `json:"offline,omitempty"`
	// Endpoints is the readiness of every model server the block declares,
	// by endpoint id.
	Endpoints []adapters.OpenCodeEndpointReadiness `json:"endpoints,omitempty"`
	// StoredLogins are the subscription or OAuth logins for anthropic that
	// OpenCode holds, by source; never their content.
	StoredLogins []OpenCodeStoredLogin `json:"stored_logins,omitempty"`
}

// OpenCodeDirs are the OpenCode directories of a pipeline run: OpenCode's own
// directory under each of the four XDG base directories the run's isolation
// sets (adapters.OpenCodeIsolationEnv), in a root per run.
type OpenCodeDirs struct {
	Config string `json:"config"`
	Data   string `json:"data"`
	Cache  string `json:"cache"`
	State  string `json:"state"`
	// OperatorConfig is the operator's own OpenCode config directory, which
	// a run also reads when opencode.inherit_user_config is on.
	OperatorConfig string `json:"operator_config,omitempty"`
}

// OpenCodeStoredLogin is one stored login the doctor flags: where OpenCode
// keeps it, the provider, and the entry's type. Nothing else of the entry is
// read.
type OpenCodeStoredLogin struct {
	Source   string `json:"source"`
	Provider string `json:"provider"`
	Type     string `json:"type"`
}

// openCodeRunIDPlaceholder stands for a run's id in the directories printed.
const openCodeRunIDPlaceholder = "<run-id>"

// openCodeProbe bundles the OpenCode check's side effects, so a test drives
// it without the machine's config, binaries or servers.
type openCodeProbe struct {
	getenv           func(string) string
	lookupEnv        func(string) (string, bool)
	home             func() (string, error)
	machineConfigDir func() (string, error)
	settings         func() (config.OpenCodeConfig, error)
	lookPath         func(string) (string, error)
	// version is the binary's `--version` (adapters.OpenCodeVersionOf).
	version func(bin string) (string, error)
	// models is the stdout of `opencode models` under the per-run config a
	// dispatch of model gets on a machine whose block is settings, with each
	// of the provider's variables this environment holds set to a
	// placeholder (runOpenCodeModels).
	models func(bin string, settings config.OpenCodeConfig, model string) (string, error)
	// endpoint is the readiness probe (adapters.ProbeOpenCodeEndpoint).
	endpoint func(target adapters.OpenCodeEndpointTarget, model string, injectedContext int) adapters.OpenCodeEndpointReadiness
	readFile func(string) ([]byte, error)
	glob     func(string) ([]string, error)
	goos     string
	// managedConfigFiles replaces the machine's managed OpenCode config files
	// (adapters.OpenCodeRunRequest.ManagedConfigFiles); nil means this
	// machine's. Only tests set it, because the real files are outside any
	// directory a test may write.
	managedConfigFiles []string
}

// newOpenCodeProbe builds the OpenCode check's dependencies for
// defaultAdapterProbe. A variable so a test can drive CheckAdapters, the
// function cap recovery calls, without the machine's.
var newOpenCodeProbe = defaultOpenCodeProbe

func defaultOpenCodeProbe() openCodeProbe {
	return openCodeProbe{
		getenv:           os.Getenv,
		lookupEnv:        os.LookupEnv,
		home:             os.UserHomeDir,
		machineConfigDir: config.MachineConfigDir,
		settings:         func() (config.OpenCodeConfig, error) { return config.LoadOpenCodeConfig("") },
		lookPath:         exec.LookPath,
		version:          func(bin string) (string, error) { return adapters.OpenCodeVersionOf(context.Background(), bin) },
		models:           runOpenCodeModels,
		endpoint: func(target adapters.OpenCodeEndpointTarget, model string, injected int) adapters.OpenCodeEndpointReadiness {
			return adapters.ProbeOpenCodeEndpoint(nil, target, model, injected)
		},
		readFile: os.ReadFile,
		glob:     filepath.Glob,
		goos:     runtime.GOOS,
	}
}

// runOpenCodeModels runs `opencode models` as a probe, under the per-run
// config a dispatch of model gets. The config refers to files in the run's
// root, so it is built for the probe's own directory, which the files are
// written to and which is removed afterwards. OpenCode lists a configured
// endpoint's model without asking the server (observed on 1.18.30,
// testdata/opencode-capture), so the probe needs no server.
//
// OpenCode lists a hosted provider's models only when one of the provider's
// variables is set, and a probe inherits none. So each of the dispatched
// provider's variables that this environment holds, and a dispatch keeps
// (adapters.OpenCodeProviderVars), is set in the probe to a placeholder: the
// probe lists what a dispatch from here would, and never holds a credential.
func runOpenCodeModels(bin string, settings config.OpenCodeConfig, model string) (string, error) {
	probe, err := adapters.NewOpenCodeProbe()
	if err != nil {
		return "", err
	}
	defer func() { _ = probe.Close() }()
	input, err := adapters.OpenCodeConfigInputFor(settings, adapters.RunOptions{Model: model, Stage: "doctor"}, probe.Root(), os.LookupEnv)
	if err != nil {
		return "", err
	}
	built, err := adapters.BuildOpenCodeConfig(input)
	if err != nil {
		return "", err
	}
	set, _ := adapters.OpenCodeProviderVars(model, os.LookupEnv)
	res, err := probe.Run(context.Background(), bin, []string{"models"}, built.Content, built.Files, set...)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("`opencode models` exited %d", res.ExitCode)
	}
	return string(res.Stdout), nil
}

// parseOpenCodeCatalog parses `opencode models`: one provider/model per line
// (testdata/opencode-1.18.30-models-*.txt). ok is false when no line has that
// shape, an output change the check must not read as an empty catalog.
func parseOpenCodeCatalog(output string) (ids []string, defaultID string, ok bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if key, id, qualified := strings.Cut(line, "/"); qualified && key != "" && id != "" && !strings.ContainsAny(line, " \t") {
			ids = append(ids, line)
		}
	}
	return ids, "", len(ids) > 0
}

// openCodeGateRemediation is the remediation of the row while the enable gate
// is closed.
const openCodeGateRemediation = "the opencode adapter is experimental and dispatches only with " +
	adapters.ExperimentalOpenCodeEnvVar + "=1 in the environment, so it is not usable, and no other OpenCode check ran. " +
	"Set it for this doctor run to check an OpenCode dispatch. See docs/decisions/022-opencode-multi-provider-adapter.md"

// checkOpenCode is the opencode row. See the comment at the top of the file.
func checkOpenCode(name string, spec adapterSpec, probe adapterProbe) AdapterHealth {
	h := AdapterHealth{
		Adapter:    strings.TrimSpace(name),
		Kind:       string(kindCLI),
		Binary:     spec.binary,
		MinVersion: spec.minVersion,
	}
	m, _ := adaptercompat.Get("opencode")
	oc := &OpenCodeHealth{MaxTested: m.MaxTested, FloorPolicy: spec.floorPolicy}
	h.OpenCode = oc
	h.Notes = append(h.Notes, fmt.Sprintf("version floor %s (%s), max-tested %s, from internal/adaptercompat/manifests/opencode.json",
		orUnset(spec.minVersion), orUnset(spec.floorPolicy), orUnset(m.MaxTested)))

	p := probe.opencode
	if p.getenv == nil || p.getenv(adapters.ExperimentalOpenCodeEnvVar) != "1" {
		h.Remediation = openCodeGateRemediation
		return h
	}
	oc.Enabled = true

	var blocking []string
	block := func(s string) { blocking = append(blocking, s) }
	warn := func(s string) { h.Warnings = append(h.Warnings, s) }

	home, homeErr := p.home()
	if homeErr != nil {
		home = ""
		warn("the home directory could not be resolved, so the run directories, the stored logins, and the last dispatch were not checked")
	}
	settings, settingsErr := p.settings()
	if settingsErr != nil {
		block(settingsErr.Error())
	}

	if home != "" {
		oc.StoredLogins = openCodeStoredLogins(p, home)
		for _, login := range oc.StoredLogins {
			warn(openCodeStoredLoginFinding(login))
		}
		oc.Dirs = openCodeRunDirs(p, home, settings.InheritUserConfig)
		if oc.Dirs != nil {
			h.Notes = append(h.Notes, fmt.Sprintf("run directories: config %s, data %s, cache %s, state %s",
				oc.Dirs.Config, oc.Dirs.Data, oc.Dirs.Cache, oc.Dirs.State))
			if oc.Dirs.OperatorConfig != "" {
				h.Notes = append(h.Notes, "opencode.inherit_user_config is on: a run also reads your OpenCode config in "+oc.Dirs.OperatorConfig)
			}
		}
	}
	if settingsErr != nil {
		h.OK = false
		h.Remediation = strings.Join(blocking, "; ")
		return h
	}

	endpoints, endpointErr := adapters.OpenCodeEndpoints(settings)
	if endpointErr != nil {
		block(endpointErr.Error())
	}
	oc.Offline = openCodeOfflinePosture(settings.Model, endpoints)
	h.Notes = append(h.Notes, oc.Offline)

	// run is the per-run config a dispatch of opencode.model gets, nil when
	// no model is set or the adapter refuses to build it. The refusal
	// blocks, and what the build changed from the machine-tier config, such
	// as a context limit clamped to the server's loaded window, warns.
	var run *adapters.OpenCodeRunConfig
	if settings.Model != "" && endpointErr == nil {
		built, err := openCodePerRunConfig(p, settings, settings.Model)
		if err != nil {
			block(err.Error())
		} else {
			run = &built
			for _, w := range built.Warnings {
				warn(w)
			}
		}
	}

	oc.Pinned = settings.Binary != ""
	bin, binErr := adapters.ResolveOpenCodeBinary(settings.Binary, p.lookPath)
	if binErr != nil {
		block(binErr.Error())
	} else {
		h.Installed = true
		h.Path = bin.Path
		version, versionErr := p.version(bin.Path)
		h.Version = version
		policy, policyErr := adapters.CheckOpenCodeVersion(bin, version, versionErr, home)
		if policyErr != nil {
			block(policyErr.Error())
		} else {
			h.VersionOK = !policy.BelowFloor
			if policy.BelowFloor {
				warn(fmt.Sprintf("opencode %s is below the minimum tested version %s", version, policy.MinVersion))
			}
			if policy.AboveMaxTested {
				oc.AboveMaxTested = true
				checkOpenCodeAboveMaxTested(policy, settings.Model, endpoints, home, block, warn)
			}
			checkOpenCodeDrift(p, home, bin, version, oc, warn)
			if settings.Model != "" && endpointErr == nil {
				h.Model = settings.Model
				if run != nil {
					checkOpenCodeCatalog(&h, p, bin, settings, *run, block, warn)
				}
			}
		}
	}
	if settings.Model == "" {
		h.Notes = append(h.Notes, "opencode.model is not set, so the catalog probe did not run: a stage names its own <provider>/<model>")
	}

	// While opencode.inherit_user_config is off, PrepareOpenCodeRun refuses
	// every dispatch on a machine that has managed OpenCode config, whatever
	// the stage. A populated ~/.opencode no longer refuses a dispatch: a
	// non-inheriting run gets its own per-run HOME, which never contains
	// .opencode.
	if home != "" {
		for _, refusal := range adapters.OpenCodeMachineConfigRefusals(adapters.OpenCodeRunRequest{
			Home:               home,
			Settings:           settings,
			GOOS:               p.goos,
			ManagedConfigFiles: p.managedConfigFiles,
		}) {
			block(refusal.Error())
		}
	}

	if endpointErr == nil {
		checkOpenCodeEndpoints(p, settings, endpoints, run, oc, block, warn)
	}

	h.OK = len(blocking) == 0
	h.Remediation = strings.Join(blocking, "; ")
	return h
}

func orUnset(s string) string {
	if s == "" {
		return "unset"
	}
	return s
}

// checkOpenCodeAboveMaxTested reports a binary newer than max-tested as a
// dispatch meets it: the adapter's refusal when opencode.model runs on a model
// server the operator runs, and otherwise a warning that a dispatch runs the
// self-test before its first stage.
func checkOpenCodeAboveMaxTested(policy adapters.OpenCodeVersionPolicy, model string, endpoints []adapters.OpenCodeEndpoint, home string, block, warn func(string)) {
	if model != "" {
		if err := adapters.OpenCodeEndpointAboveMaxTested(policy, model, endpoints, home); err != nil {
			block(err.Error())
			return
		}
	}
	warn(fmt.Sprintf("opencode %s is newer than the max-tested %s: a dispatch runs a self-test of its per-run config and run flags on this version before its first stage, and refuses a model server you run",
		policy.Version, policy.MaxTested))
}

// checkOpenCodeDrift warns when the binary resolved now is not the version
// the last dispatch on this machine was checked against, as a PATH install
// that updated itself is not.
func checkOpenCodeDrift(p openCodeProbe, home string, bin adapters.OpenCodeBinary, version string, oc *OpenCodeHealth, warn func(string)) {
	if home == "" {
		return
	}
	rec, ok, err := adapters.ReadOpenCodeDispatchRecord(home)
	switch {
	case err != nil:
		warn("the last OpenCode dispatch's record could not be read: " + err.Error())
		return
	case !ok:
		return
	}
	oc.LastDispatchVersion = rec.Version
	if rec.Version == version {
		return
	}
	where := ""
	if rec.Binary != bin.Path {
		where = fmt.Sprintf(" from %s", rec.Binary)
	}
	pinAdvice := "pin opencode.binary to a tested build so it cannot change under the pipeline"
	if bin.Pinned {
		pinAdvice = "the pinned binary itself changed"
	}
	warn(fmt.Sprintf("the opencode the next dispatch runs (%s) is %s, and the last OpenCode dispatch on this machine ran %s%s: the binary changed since, as a PATH install does when OpenCode's TUI updates itself; %s",
		bin.Path, orUnset(version), rec.Version, where, pinAdvice))
}

// checkOpenCodeCatalog runs `opencode models` under built, the per-run config
// for opencode.model, and reports whether the model is in it. A config the
// adapter would refuse to build is checkOpenCode's blocking finding, so this
// runs only on one it builds; a probe that could not run is a warning.
//
// What the listing shows depends on whether the per-run config declares the
// model:
//
//   - A declared endpoint's model and an anthropic model are declared: the
//     config writes the model's own entry into the provider's block, so the
//     listing holds it by construction. It shows that this opencode loads the
//     per-run config, not that the provider serves the model; the endpoint
//     check asks the server, and the adapter dispatches only anthropic models
//     the bundled catalog lists. The row says so in a note.
//   - Any other hosted provider's model is not: OpenCode lists it only when
//     the bundled catalog does and one of the provider's variables is set.
//     The probe sets the ones this environment holds to a placeholder
//     (runOpenCodeModels), so the listing is a dispatch's. When the provider
//     has variables and this environment holds none, OpenCode lists nothing
//     for it, and a stage would find no model, which blocks and names them.
//
// With opencode.inherit_user_config on, a dispatch also reads the operator's
// own OpenCode config, and the probe reads none of the operator's OpenCode
// state, so the listing leaves that config out. Observed on 1.18.30, a lower
// config layer holding a provider's options.apiKey loads the provider with
// none of its variables set, and one holding a model entry for it adds the
// model to the listing. For a model the per-run config does not declare, a
// listing that lacks it is then a warning that says so, never a block.
func checkOpenCodeCatalog(h *AdapterHealth, p openCodeProbe, bin adapters.OpenCodeBinary, settings config.OpenCodeConfig, built adapters.OpenCodeRunConfig, block, warn func(string)) {
	model := settings.Model
	out, err := p.models(bin.Path, settings, model)
	if err != nil {
		warn("the catalog probe `opencode models` could not run: " + err.Error())
		return
	}
	key, _, _ := strings.Cut(strings.TrimSpace(model), "/")
	declared := openCodeDeclaresModel(built.Content, model)
	inherited := settings.InheritUserConfig && !declared
	if strings.TrimSpace(out) == "" {
		// `opencode models` exited 0 and listed nothing: the one provider
		// the per-run config enables did not load.
		if set, unset := adapters.OpenCodeProviderVars(model, p.lookupEnv); !declared && len(set) == 0 && len(unset) > 0 {
			if inherited {
				warn(fmt.Sprintf("`opencode models` lists no %s model under the per-run config, and none of the provider's variables (%s) is set in this environment; %s, and which can hold provider %s's API key. The model check did not decide: unless that config holds a key for %s, set %s where the pipeline runs",
					key, strings.Join(unset, ", "), openCodeInheritedConfigCaveat, key, key, strings.Join(unset, " or ")))
				return
			}
			h.ModelOK = boolPtr(false)
			block(fmt.Sprintf("`opencode models` lists no %s model under the per-run config: OpenCode loads provider %s only when one of its variables (%s) is set, and none is set in this environment, so a stage on opencode.model %s would find no model. Set %s where the pipeline runs",
				key, key, strings.Join(unset, ", "), model, strings.Join(unset, " or ")))
			return
		}
		warn(fmt.Sprintf("`opencode models` listed no %s model under the per-run config, so provider %s did not load and the model check did not run", key, key))
		return
	}
	ids, _, ok := parseOpenCodeCatalog(out)
	if !ok {
		warn("the catalog probe could not parse `opencode models`; the model check did not run")
		return
	}
	present := catalogContains(ids, model)
	if !present && inherited {
		warn(fmt.Sprintf("`opencode models` does not list opencode.model %s under the per-run config; %s, and which can declare the model. The model check did not decide: unless that config declares %s, a stage on it fails, so name a model `opencode models %s` lists",
			model, openCodeInheritedConfigCaveat, model, key))
		return
	}
	h.ModelOK = boolPtr(present)
	if present {
		if declared {
			h.Notes = append(h.Notes, fmt.Sprintf("`opencode models` lists opencode.model %s because the per-run config declares it: that shows this opencode loads the per-run config, not that provider %s serves the model",
				model, key))
		}
		return
	}
	block(fmt.Sprintf("`opencode models` does not list opencode.model %s under the per-run config, so a stage on it would fail: name a model `opencode models %s` lists, or set opencode.model to one",
		model, key))
}

// openCodeInheritedConfigCaveat says why, with opencode.inherit_user_config
// on, what the catalog probe lists is not what a dispatch finds.
const openCodeInheritedConfigCaveat = "but opencode.inherit_user_config is on, so a dispatch also reads your own OpenCode config (your OpenCode config directory and ~/.opencode), which the probe leaves out, as it reads none of your OpenCode state"

// openCodePerRunConfig builds the per-run config a dispatch of model would
// get, into a root that is never created, and returns the adapter's refusal
// when it would not build.
func openCodePerRunConfig(p openCodeProbe, settings config.OpenCodeConfig, model string) (adapters.OpenCodeRunConfig, error) {
	root := filepath.Join(os.TempDir(), "nightgauge-doctor-opencode")
	input, err := adapters.OpenCodeConfigInputFor(settings, adapters.RunOptions{Model: model, Stage: "doctor"}, root, p.lookupEnv)
	if err != nil {
		return adapters.OpenCodeRunConfig{}, err
	}
	return adapters.BuildOpenCodeConfig(input)
}

// openCodeDeclaresModel reports whether content, a per-run config, declares
// model: its provider block holds the model's own entry.
func openCodeDeclaresModel(content, model string) bool {
	key, id, _ := strings.Cut(strings.TrimSpace(model), "/")
	var cfg struct {
		Provider map[string]struct {
			Models map[string]json.RawMessage `json:"models"`
		} `json:"provider"`
	}
	if json.Unmarshal([]byte(content), &cfg) != nil {
		return false
	}
	_, ok := cfg.Provider[key].Models[id]
	return ok
}

// openCodeInjectedContext is the limit.context content, a per-run config,
// gives model id on the provider block key, or 0 when it gives none.
func openCodeInjectedContext(content, key, id string) int {
	var cfg struct {
		Provider map[string]struct {
			Models map[string]struct {
				Limit struct {
					Context int `json:"context"`
				} `json:"limit"`
			} `json:"models"`
		} `json:"provider"`
	}
	if json.Unmarshal([]byte(content), &cfg) != nil {
		return 0
	}
	return cfg.Provider[key].Models[id].Limit.Context
}

// checkOpenCodeEndpoints probes every model server the block declares. A
// server that is not ready blocks when opencode.model runs on it; otherwise
// it is a warning. A context warning is always a warning.
//
// The context the probe compares is the one a dispatch gives OpenCode: for
// opencode.model's endpoint, the limit.context run, the per-run config, holds
// for the model, which the machine-tier override sets or discovery fills and
// which is clamped to the loaded window; 0 when the config did not build,
// whose refusal is checkOpenCode's finding. Another endpoint's is the
// override, when the block sets one.
func checkOpenCodeEndpoints(p openCodeProbe, settings config.OpenCodeConfig, endpoints []adapters.OpenCodeEndpoint, run *adapters.OpenCodeRunConfig, oc *OpenCodeHealth, block, warn func(string)) {
	key, modelID, _ := strings.Cut(settings.Model, "/")
	for _, ep := range endpoints {
		model := ""
		injected := ep.Limit.Context
		if key == ep.ID {
			model = modelID
			injected = 0
			if run != nil {
				injected = openCodeInjectedContext(run.Content, ep.ID, modelID)
			}
		}
		r := p.endpoint(adapters.OpenCodeEndpointTarget{ID: ep.ID, Kind: ep.Provider, BaseURL: ep.BaseURL}, model, injected)
		oc.Endpoints = append(oc.Endpoints, r)
		switch {
		case !r.Ready && model != "":
			block(r.Problem)
		case !r.Ready:
			warn(r.Problem)
		}
		if r.Warning != "" {
			warn(r.Warning)
		}
		if ep.NonLoopback && strings.HasPrefix(ep.BaseURL, "http://") {
			warn(fmt.Sprintf("endpoint %s is on another machine and is reached over plain http, so a stage's prompts and repository content cross the network unencrypted", ep.ID))
		}
	}
}

// openCodeOfflinePosture describes what the per-run config sets that keeps a
// run off the network, and where the configured model runs. It is a
// statement about configuration: #1644 measures what a run actually
// connects to, and until it passes, egress is unverified.
func openCodeOfflinePosture(model string, endpoints []adapters.OpenCodeEndpoint) string {
	const posture = "offline posture (configuration only, egress unverified until #1644 passes): a run fetches no model catalog, never autoupdates or shares a session, downloads no LSP server, loads no default plugin, and enables only the provider it dispatches to"
	if model == "" {
		return posture + "; opencode.model is not set, so where a stage's model runs depends on the model it names"
	}
	key, _, _ := strings.Cut(model, "/")
	for _, ep := range endpoints {
		if ep.ID != key {
			continue
		}
		if ep.NonLoopback {
			return posture + fmt.Sprintf("; opencode.model %s runs on endpoint %s, on another machine, so a run on it is not offline", model, ep.ID)
		}
		return posture + fmt.Sprintf("; opencode.model %s runs on endpoint %s on this machine", model, ep.ID)
	}
	return posture + fmt.Sprintf("; opencode.model %s runs on hosted provider %s, so a run on it is not offline", model, key)
}

// openCodeRunDirs is the OpenCode directories of a pipeline run, from the
// isolation environment the adapter gives every spawn, for a run whose id is
// a placeholder.
func openCodeRunDirs(p openCodeProbe, home string, inherit bool) *OpenCodeDirs {
	machineDir := filepath.Join(home, ".nightgauge")
	if p.machineConfigDir != nil {
		if d, err := p.machineConfigDir(); err == nil && filepath.IsAbs(d) {
			machineDir = d
		}
	}
	env, err := adapters.OpenCodeIsolationEnv(adapters.OpenCodeIsolation{
		Root:              filepath.Join(adapters.OpenCodeRunsDir(home), openCodeRunIDPlaceholder),
		Home:              home,
		Lookup:            p.lookupEnv,
		GOOS:              p.goos,
		MachineConfigDir:  machineDir,
		InheritUserConfig: inherit,
	})
	if err != nil {
		return nil
	}
	return &OpenCodeDirs{
		Config:         filepath.Join(env["XDG_CONFIG_HOME"], "opencode"),
		Data:           filepath.Join(env["XDG_DATA_HOME"], "opencode"),
		Cache:          filepath.Join(env["XDG_CACHE_HOME"], "opencode"),
		State:          filepath.Join(env["XDG_STATE_HOME"], "opencode"),
		OperatorConfig: env["OPENCODE_CONFIG_DIR"],
	}
}

// openCodeAuthContentEnvVar holds OpenCode's stored logins in place of
// auth.json when it is set (ADR-022 § 17).
const openCodeAuthContentEnvVar = "OPENCODE_AUTH_CONTENT"

// openCodeStoredLogins returns the subscription or OAuth logins for anthropic
// that OpenCode holds in the sources ADR-022 § 17 names: auth.json in the
// operator's OpenCode data directory, OPENCODE_AUTH_CONTENT in the
// environment the doctor runs in, and auth.json in any pipeline run's root.
// Of each entry only its type is decoded; no credential value is kept,
// printed or logged, and neither source's content appears in any message.
func openCodeStoredLogins(p openCodeProbe, home string) []OpenCodeStoredLogin {
	var found []OpenCodeStoredLogin
	flag := func(source string, data []byte) {
		for _, provider := range openCodeOAuthProviders(data) {
			found = append(found, OpenCodeStoredLogin{Source: source, Provider: provider, Type: "oauth"})
		}
	}
	dataHome := filepath.Join(home, ".local", "share")
	if v, ok := p.lookupEnv("XDG_DATA_HOME"); ok && filepath.IsAbs(v) {
		dataHome = v
	}
	operatorAuth := filepath.Join(dataHome, "opencode", "auth.json")
	if data, err := p.readFile(operatorAuth); err == nil {
		flag(operatorAuth, data)
	}
	if v, ok := p.lookupEnv(openCodeAuthContentEnvVar); ok && v != "" {
		flag(openCodeAuthContentEnvVar, []byte(v))
	}
	if p.glob != nil {
		roots, _ := p.glob(filepath.Join(adapters.OpenCodeRunsDir(home), "*", "data", "opencode", "auth.json"))
		for _, path := range roots {
			if data, err := p.readFile(path); err == nil {
				flag(path, data)
			}
		}
	}
	return found
}

// openCodeOAuthProviders returns the providers, of those whose one pipeline
// credential is an API key (anthropic), that data, OpenCode's stored logins,
// holds an OAuth entry for. It decodes each entry's type and nothing else.
func openCodeOAuthProviders(data []byte) []string {
	var entries map[string]struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	if entry, ok := entries["anthropic"]; ok && entry.Type == "oauth" {
		return []string{"anthropic"}
	}
	return nil
}

// openCodeStoredLoginFinding is the warning for one flagged login. It names
// where the login is, never what it holds.
func openCodeStoredLoginFinding(login OpenCodeStoredLogin) string {
	return fmt.Sprintf("OpenCode's stored logins in %s hold a subscription or OAuth login for %s (type %s). A pipeline run never uses it: an %s/ stage through OpenCode authenticates only with ANTHROPIC_API_KEY. To run Claude on that login, use the claude-headless adapter, which runs Claude Code under the login Claude Code itself holds",
		login.Source, login.Provider, login.Type, login.Provider)
}
