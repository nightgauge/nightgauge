package orchestrator

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
)

// A remote run request (#1656, ADR-022 § 2 "Remote run requests") names the
// adapter and model a dashboard- or mobile-triggered run executes on. Both
// values arrive from the hosted service and are untrusted: the model becomes
// the argv after `-m`. ValidateRemotePin is the one authority that decides
// whether this machine can serve the pair, and it runs before the command is
// acknowledged, so a pair it refuses is never queued and never replaced.

// RemotePinMaxModelBytes bounds a requested model.
const RemotePinMaxModelBytes = 200

// remotePinAdapters is the allow-list: the adapter ids the extension
// dispatches (its ExecutionAdapter enum, AdapterEnumSchema in
// packages/nightgauge-vscode/src/config/schema.ts), because a requested pin is
// executed there. Each one also resolves in adapters.NewRegistry(), which
// supplies Agentic and ValidateModel. TestRemotePinAllowListMatchesTheExtension
// fails when the two lists differ.
var remotePinAdapters = []string{
	"claude", "codex", "copilot", "gemini", "gemini-sdk", "grok", "lm-studio", "ollama", "opencode",
}

var (
	// remotePinOpenCodeModelRE is an opencode `-m` value: a provider key, a
	// slash, a model id.
	remotePinOpenCodeModelRE = regexp.MustCompile(`^[a-z0-9-]+/[A-Za-z0-9._:/-]+$`)
	// remotePinModelRE is any other adapter's model id.
	remotePinModelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

// RemotePinDeps are ValidateRemotePin's side effects. A nil field uses this
// machine's.
type RemotePinDeps struct {
	// AdapterUsable is the adapter health reading the cap-hop walk uses. It
	// refuses a gated adapter and every other prerequisite.
	AdapterUsable func(adapter string) (bool, string)
	// LookupEnv reads this machine's environment for provider credentials.
	LookupEnv func(string) (string, bool)
	// Catalog lists the models `opencode models` shows for model's dispatch.
	Catalog func(model string) ([]string, error)
}

func (d RemotePinDeps) withDefaults() RemotePinDeps {
	if d.AdapterUsable == nil {
		d.AdapterUsable = AdapterUsableForCapHop
	}
	if d.LookupEnv == nil {
		d.LookupEnv = os.LookupEnv
	}
	if d.Catalog == nil {
		d.Catalog = doctor.OpenCodeCatalog
	}
	return d
}

// ValidateRemotePinShape is the pure half of ValidateRemotePin: the pattern,
// length and allow-list checks, which read nothing but the two values. Both
// empty is no request and is accepted. The refusal never echoes a value that
// failed its shape.
func ValidateRemotePinShape(adapter, model string) error {
	if adapter == "" && model == "" {
		return nil
	}
	if adapter == "" {
		return fmt.Errorf("a model was requested without an adapter; name the adapter that should run it")
	}
	if !slices.Contains(remotePinAdapters, adapter) {
		return fmt.Errorf("the requested adapter is not one this machine can run (allowed: %s)",
			strings.Join(remotePinAdapters, ", "))
	}
	if model == "" {
		return nil
	}
	if len(model) > RemotePinMaxModelBytes {
		return fmt.Errorf("the requested model is longer than %d bytes", RemotePinMaxModelBytes)
	}
	if strings.HasPrefix(model, "-") {
		return fmt.Errorf("the requested model must not start with '-'")
	}
	if strings.Contains(model, "..") {
		return fmt.Errorf("the requested model must not contain '..'")
	}
	if adapter == "opencode" {
		if !remotePinOpenCodeModelRE.MatchString(model) {
			return fmt.Errorf("the requested model is not an opencode <provider>/<model> id, such as lmstudio/<model-id>")
		}
		return nil
	}
	if !remotePinModelRE.MatchString(model) {
		return fmt.Errorf("the requested model is not a model id: letters, digits and . _ : / - only")
	}
	return nil
}

// ValidateRemotePin decides whether this machine can serve a requested
// adapter and model, and returns the reason when it cannot. The order is
// ADR-022 § 2's: shape, capability, the adapter's own model check,
// credentials, the catalog. The first refusal wins, and nothing after the
// shape check sees a value the shape check refused.
func ValidateRemotePin(adapter, model string, deps RemotePinDeps) error {
	if err := ValidateRemotePinShape(adapter, model); err != nil {
		return err
	}
	if adapter == "" {
		return nil
	}
	d := deps.withDefaults()

	runner, err := adapters.NewRegistry().Get(adapter)
	if err != nil {
		return fmt.Errorf("adapter %s is not registered on this machine", adapter)
	}
	if !runner.Agentic() {
		return fmt.Errorf("adapter %s cannot run pipeline stages: it has no agentic tool loop", adapter)
	}
	if ok, reason := d.AdapterUsable(adapter); !ok {
		return fmt.Errorf("adapter %s is not usable on this machine, and a remote request cannot enable it: %s", adapter, reason)
	}
	if model == "" {
		return nil
	}
	if v, ok := runner.(interface{ ValidateModel(string) error }); ok {
		if err := v.ValidateModel(model); err != nil {
			return fmt.Errorf("adapter %s refuses model %s: %w", adapter, model, err)
		}
	}
	if adapter != "opencode" {
		return nil
	}
	if err := adapters.OpenCodeCredentialRefusal(model, d.LookupEnv); err != nil {
		return err
	}
	ids, err := d.Catalog(model)
	if err != nil {
		return fmt.Errorf("the opencode catalog could not be read, so model %s cannot be confirmed: %w", model, err)
	}
	if slices.Contains(ids, model) {
		return nil
	}
	// OpenCode lists a hosted provider's models only when one of the
	// provider's variables is set, and a provider the per-run config declares
	// (a local endpoint) without any. So a missing model whose provider has
	// none of its variables set is, in the doctor's reading too
	// (checkOpenCodeCatalog), a provider with no credentials here.
	if set, unset := adapters.OpenCodeProviderVars(model, d.LookupEnv); len(set) == 0 && len(unset) > 0 {
		provider, _, _ := strings.Cut(model, "/")
		return fmt.Errorf("model %s is not in this machine's opencode catalog, and provider %s has no credentials configured on this machine: none of %s is set",
			model, provider, strings.Join(unset, ", "))
	}
	return fmt.Errorf("model %s is not in this machine's opencode catalog (`opencode models`)", model)
}
