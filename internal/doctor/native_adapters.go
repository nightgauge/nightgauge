package doctor

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// Adapter health (#2092), a native ADR-025 check group. `--adapters` selects
// the adapters the `adapters` check probes. Each adapter that is not usable is
// one finding, coded by the condition that makes it so; its cause is the
// probe's own and its remedy names the install command, the login command or
// the variable to set. A usable adapter can still carry an advisory finding.
//
// Every adapter finding is a warning or info, never a blocker:
// skills/_shared/PREFLIGHT.md halts on exit 2 and runs inside an agent
// session, so an adapter is already running when it does.

// Adapter codes (ADR-025 § 2: adapter health takes NGD100 upward).
const (
	codeAdapterNotInstalled  = "NGD100" // CLI not on PATH
	codeAdapterBelowFloor    = "NGD101" // version below its floor, or unreadable
	codeAdapterKeyUnset      = "NGD102" // SDK adapter: no API key variable set
	codeAdapterUnknown       = "NGD103" // unknown or retired adapter name
	codeAdapterCompat        = "NGD104" // compat manifests failed to load
	codeAdapterCatalogDrift  = "NGD105" // registry-served model missing from the CLI catalog
	codeAdapterModelRejected = "NGD106" // the provider rejected the probed model
	codeOpenCodeGate         = "NGD107" // opencode: experimental gate closed
	codeOpenCodeConfig       = "NGD108" // opencode: machine-tier `opencode:` block refused
	codeOpenCodePin          = "NGD109" // opencode: opencode.binary pin refused
	codeAdapterRefused       = "NGD110" // a dispatch would be refused for another reason
	codeAdapterWarnings      = "NGD111" // usable, with warnings
)

// adaptersCheck is the registry ID of the adapter check.
const adaptersCheck = "adapters"

// adaptersTimeout bounds the check. Adapters are probed concurrently, so it
// covers the slowest single probe: the model probe (modelProbeTimeout) after a
// version spawn, or an OpenCode row's version, catalog and endpoint probes.
const adaptersTimeout = 90 * time.Second

func init() {
	builtinChecks = append(builtinChecks, Check{
		ID: adaptersCheck, Title: "Adapter health", Group: "adapters",
		Code: codeAdapterNotInstalled, Timeout: adaptersTimeout,
		Run: func(ctx context.Context, env *Env) []Finding {
			if len(env.Adapters) == 0 {
				env.SetDetail(adaptersCheck, "not requested (pass --adapters)")
				return nil
			}
			health := CheckAdapters(env.Adapters)
			env.setAdapters(health)
			usable := 0
			for _, h := range health {
				if h.OK {
					usable++
				}
			}
			env.SetDetail(adaptersCheck, fmt.Sprintf("%d adapter(s) checked, %d usable", len(health), usable))
			return adapterHealthFindings(health)
		},
	})
}

// AdapterFindings probes the named adapters (CheckAdapters) and returns their
// findings. Cap recovery reads it (orchestrator.AdapterUsableForCapHop).
func AdapterFindings(names []string) []Finding {
	return adapterHealthFindings(CheckAdapters(names))
}

// AdapterUnusable reports whether findings hold one that makes adapter not
// usable, and that finding's cause.
func AdapterUnusable(findings []Finding, adapter string) (cause string, unusable bool) {
	want := strings.TrimSpace(adapter)
	for _, f := range findings {
		if f.Check != adaptersCheck || f.Evidence["adapter"] != want || f.Evidence["usable"] != "false" {
			continue
		}
		if f.Cause == "" {
			return "not usable", true
		}
		return f.Cause, true
	}
	return "", false
}

// adapterHealthFindings turns adapter rows into findings: one warning per
// adapter that is not usable, and for a usable one an info finding when it
// runs below its version floor, a warning when the provider rejected the
// probed model, and a warning carrying its warnings.
func adapterHealthFindings(health []AdapterHealth) []Finding {
	var fs []Finding
	for _, h := range health {
		if !h.OK {
			fs = append(fs, unusableAdapterFinding(h))
			continue
		}
		if h.Kind == string(kindCLI) && h.Installed && h.MinVersion != "" && !h.VersionOK {
			fs = append(fs, adapterFinding(h, codeAdapterBelowFloor, SeverityInfo,
				fmt.Sprintf("adapter %s runs below its version floor: %s", h.Adapter, h.Problem()),
				h.Remediation, updateRemedy(h)))
		}
		if h.ModelOK != nil && !*h.ModelOK {
			fs = append(fs, adapterFinding(h, codeAdapterModelRejected, SeverityWarning,
				fmt.Sprintf("adapter %s: the provider rejected model %s", h.Adapter, h.Model),
				h.Remediation, modelRemedy(h)))
		}
		if len(h.Warnings) > 0 {
			fs = append(fs, adapterFinding(h, codeAdapterWarnings, SeverityWarning,
				fmt.Sprintf("adapter %s is usable with %d warning(s): %s", h.Adapter, len(h.Warnings), firstLine(h.Warnings[0])),
				strings.Join(h.Warnings, "; "),
				manualRemedy("review", "Act on the adapter's warnings", adaptersCheck,
					"Act on each warning in the evidence (warning.N)",
					"Re-run `nightgauge doctor --adapters "+h.Adapter+"`")))
		}
	}
	return fs
}

// unusableAdapterFinding is the one finding of an adapter that is not usable.
// Its cause is the probe's own remediation, which cap recovery reports as
// the reason it did not hop.
func unusableAdapterFinding(h AdapterHealth) Finding {
	code := h.Code
	if code == "" {
		code = codeAdapterRefused
	}
	var remedy Remedy
	switch code {
	case codeAdapterNotInstalled:
		remedy = installRemedy(h)
	case codeAdapterBelowFloor:
		remedy = updateRemedy(h)
	case codeAdapterKeyUnset:
		vars := strings.Join(adapterSpecFor(h).apiKeyEnvs, " or ")
		remedy = manualRemedy("set-key", "Set "+vars+" where the pipeline runs", adaptersCheck,
			"Export "+vars+" in the environment the pipeline runs in (the doctor checks only that it is set, never its value)",
			"Re-run `nightgauge doctor --adapters "+h.Adapter+"`")
	case codeAdapterUnknown:
		remedy = manualRemedy("rename", "Name an adapter the doctor can check", adaptersCheck,
			"Pass one of: "+strings.Join(AllAdapterNames(), ", "),
			"A retired adapter's cause names its replacement")
	case codeAdapterCompat:
		remedy = manualRemedy("reinstall", "Reinstall nightgauge so its compat manifests load", adaptersCheck,
			"Reinstall or rebuild the nightgauge binary; the cause names the manifest file and field at fault",
			"Run `"+installCommand+"`")
	case codeAdapterCatalogDrift:
		remedy = manualRemedy("reconcile", "Reconcile the model registry with the CLI's catalog", adaptersCheck,
			"Confirm the listing with the catalog command the cause names",
			"Correct the registry's transports.cli.served fact if the CLI catalog changed")
	case codeOpenCodeGate:
		remedy = manualRemedy("enable", "Set "+adapters.ExperimentalOpenCodeEnvVar+"=1 to check an OpenCode dispatch", adaptersCheck,
			"Run `"+adapters.ExperimentalOpenCodeEnvVar+"=1 nightgauge doctor --adapters opencode`",
			"The adapter is experimental: see docs/decisions/022-opencode-multi-provider-adapter.md")
	case codeOpenCodeConfig:
		remedy = manualRemedy("fix-config", "Correct the opencode: block in the machine config", adaptersCheck,
			"Edit the opencode: block in ~/.nightgauge/config.yaml as the cause says",
			"Re-run `nightgauge doctor --adapters opencode`")
	case codeOpenCodePin:
		remedy = manualRemedy("fix-pin", "Point opencode.binary at an executable file, or remove it", adaptersCheck,
			"Set opencode.binary in ~/.nightgauge/config.yaml to the absolute path of an executable opencode",
			"Or remove opencode.binary to run the opencode on PATH")
	default:
		remedy = manualRemedy("resolve", "Resolve the refusal the cause names", adaptersCheck,
			"Act on each refusal in the cause, which is what a dispatch on this adapter would meet",
			"Re-run `nightgauge doctor --adapters "+h.Adapter+"`")
	}
	return adapterFinding(h, code, SeverityWarning,
		fmt.Sprintf("adapter %s is not usable: %s", h.Adapter, h.Problem()), h.Remediation, remedy)
}

// adapterFinding builds an adapter finding. The evidence names the adapter
// and what the probe established; for an SDK adapter it names the API-key
// variables and never reads their values.
func adapterFinding(h AdapterHealth, code string, sev Severity, title, cause string, remedy Remedy) Finding {
	ev := map[string]string{"adapter": h.Adapter, "usable": strconv.FormatBool(h.OK)}
	put := func(k, v string) {
		if v != "" {
			ev[k] = v
		}
	}
	put("kind", h.Kind)
	put("binary", h.Binary)
	put("path", h.Path)
	put("version", h.Version)
	put("min_version", h.MinVersion)
	put("model", h.Model)
	if h.Kind == string(kindSDK) {
		put("api_key_vars", strings.Join(adapterSpecFor(h).apiKeyEnvs, ", "))
	}
	if h.Catalog != nil {
		put("catalog_missing", strings.Join(h.Catalog.Missing, ", "))
	}
	for i, w := range h.Warnings {
		ev["warning."+strconv.Itoa(i)] = w
	}
	if cause == "" {
		cause = h.Problem()
	}
	return newFinding(adaptersCheck, code, sev, title, cause, ev,
		[]string{normalizeAdapterName(h.Adapter)}, remedy)
}

// Problem is the one-line reason a row is not usable, or for a usable row
// the condition its advisory finding reports. It names the cause the row
// establishes: a refused opencode.binary pin or machine config is never
// reported as a binary missing from PATH (#1741).
func (h AdapterHealth) Problem() string {
	binary := h.Binary
	if binary == "" {
		binary = h.Adapter
	}
	code := h.Code
	if code == "" && h.Kind == string(kindCLI) && !h.Installed {
		code = codeAdapterNotInstalled // a row built without a code
	}
	switch code {
	case codeAdapterNotInstalled:
		return binary + " CLI not found on PATH"
	case codeAdapterKeyUnset:
		return "API key not set (" + strings.Join(adapterSpecFor(h).apiKeyEnvs, " or ") + ")"
	case codeAdapterUnknown:
		return "not an adapter the doctor can check"
	case codeAdapterCompat:
		return "compat manifests failed to load, so version floors are not enforced"
	case codeAdapterCatalogDrift:
		missing := ""
		if h.Catalog != nil {
			missing = strings.Join(h.Catalog.Missing, ", ")
		}
		return "the CLI's catalog lacks model(s) the registry declares served: " + missing
	case codeOpenCodeGate:
		return "experimental adapter, not enabled"
	case codeOpenCodeConfig:
		return "the machine config's opencode: block is refused"
	case codeOpenCodePin:
		return "the opencode.binary pin is refused"
	case codeAdapterRefused:
		return "a dispatch would be refused"
	}
	if code == codeAdapterBelowFloor || (h.Installed && h.MinVersion != "" && !h.VersionOK) {
		version := h.Version
		if version == "" {
			version = "of unknown version"
		}
		if h.MinVersion == "" {
			return binary + " " + version + " is refused by its version policy"
		}
		return binary + " " + version + " is below the minimum version " + h.MinVersion
	}
	if h.OK {
		return "usable"
	}
	return "not usable"
}

// adapterSpecFor is the spec of a row's adapter, the zero spec for a name the
// doctor does not know.
func adapterSpecFor(h AdapterHealth) adapterSpec {
	return adapterSpecs[normalizeAdapterName(h.Adapter)]
}

// installRemedy names the install command from the adapter's compat
// manifest, then its login command when the CLI has one.
func installRemedy(h AdapterHealth) Remedy {
	spec := adapterSpecFor(h)
	steps := installSteps(h, spec)
	if spec.login != "" {
		steps = append(steps, "Log in with `"+spec.login+"`")
	}
	steps = append(steps, "Re-run `nightgauge doctor --adapters "+h.Adapter+"`")
	return manualRemedy("install", "Install the "+spec.binary+" CLI", adaptersCheck, steps...)
}

// updateRemedy names the install command, which installs the current
// release, against the version floor the row reports.
func updateRemedy(h AdapterHealth) Remedy {
	spec := adapterSpecFor(h)
	floor := h.MinVersion
	if floor == "" {
		floor = "the tested version"
	}
	steps := append(installSteps(h, spec), "Re-run `nightgauge doctor --adapters "+h.Adapter+"`")
	return manualRemedy("update", "Update "+spec.binary+" to "+floor+" or later", adaptersCheck, steps...)
}

// installSteps is how to install the adapter's CLI: OpenCode's managed,
// pinned install, or the package or installer its compat manifest names.
func installSteps(h AdapterHealth, spec adapterSpec) []string {
	canonical := normalizeAdapterName(h.Adapter)
	m, _ := adaptercompat.Get(canonical)
	if canonical == "opencode" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		command, pin := adapters.OpenCodeManagedInstall(m, home)
		return []string{
			"Install the tested build with `" + command + "`",
			"Pin it with opencode.binary: " + pin + " in ~/.nightgauge/config.yaml",
		}
	}
	switch {
	case m.Install.NPM != "":
		return []string{"Install it with `npm install -g " + m.Install.NPM + "` and make sure " + spec.binary + " is on PATH"}
	case m.Install.Installer != "":
		return []string{"Install it with the vendor installer at " + m.Install.Installer + " and make sure " + spec.binary + " is on PATH"}
	}
	return []string{"Install the " + spec.binary + " CLI and make sure it is on PATH"}
}

// modelRemedy is the route to a model the provider serves this caller.
func modelRemedy(h AdapterHealth) Remedy {
	spec := adapterSpecFor(h)
	if retentionRejection(h.Remediation) {
		return manualRemedy("retention", "Enable data retention, or pin a non-Covered model", adaptersCheck,
			"Enable 30-day data retention for the organization or workspace, or ask Anthropic to authorize zero-data-retention access",
			"Or pin "+nonCoveredFallbackID()+" for the stages that route to the "+spec.modelProbeBand+" band")
	}
	steps := []string{"Confirm with `" + spec.binary + " --model " + h.Model + " -p ok`"}
	if spec.login != "" {
		steps = append(steps, "If the CLI is not logged in, run `"+spec.login+"`")
	}
	return manualRemedy("confirm-model", "Confirm the model is served to this account", adaptersCheck, steps...)
}
