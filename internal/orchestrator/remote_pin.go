package orchestrator

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"github.com/nightgauge/nightgauge/internal/doctor"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/state"
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
// executed there. `claude` is the id, not the Go registry's `claude-headless`.
// Each one also resolves in adapters.NewRegistry(), which supplies Agentic and
// ValidateModel. TestRemotePinAllowListMatchesTheExtension fails when the two
// lists differ.
var remotePinAdapters = []string{
	"claude", "codex", "copilot", "gemini", "gemini-sdk", "grok", "opencode",
}

// remotePinJudgeOnlyAdapters are extension adapter ids that are never
// dispatched to run a pipeline stage: openai-compatible is the chat-only eval
// judge backend (#2128), with no Go runner. They are the only extension ids
// the allow-list leaves out.
var remotePinJudgeOnlyAdapters = []string{"openai-compatible"}

var (
	// remotePinOpenCodeModelRE is an opencode `-m` value: a provider key, a
	// slash, a model id.
	remotePinOpenCodeModelRE = regexp.MustCompile(`^[a-z0-9-]+/[A-Za-z0-9._:/-]+$`)
	// remotePinModelRE is any other adapter's model id.
	remotePinModelRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

// RemotePinStages are the stages a pinned run dispatches on the requested
// model, so the requested model must fit every one's performance ceiling.
var RemotePinStages = []state.PipelineStage{
	state.StageIssuePickup, state.StageFeaturePlanning, state.StageFeatureDev,
	state.StageFeatureValidate, state.StagePRCreate, state.StagePRMerge,
}

// RemotePinRefusal is ValidateRemotePin's refusal. Public is what the
// hosted service and the requester see, as the rejected ack's detail: a fixed
// category and at most an adapter id or variable names, never a path, a
// probe's output or a remediation. Error is the full reason, for the local log
// only.
type RemotePinRefusal struct {
	Public string
	Reason string
}

func (r *RemotePinRefusal) Error() string { return r.Reason }

func refuse(public, format string, args ...any) *RemotePinRefusal {
	return &RemotePinRefusal{Public: public, Reason: fmt.Sprintf(format, args...)}
}

// RemotePinPublic returns the requester-facing detail of a refusal, and a
// fixed category for any other error, so nothing unvetted reaches the hosted
// service.
func RemotePinPublic(err error) string {
	if r, ok := err.(*RemotePinRefusal); ok {
		return r.Public
	}
	return "validation-failed"
}

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
	// OperatorAdapter is the adapter the operator pinned for this machine
	// (`--adapter`, NIGHTGAUGE_ADAPTER), or "". A request for any other
	// adapter is refused: machine policy refuses, it never substitutes.
	OperatorAdapter func() string
	// StageCeiling is the performance mode's routed-tier ceiling for a stage
	// in the workspace the run belongs to (a band, or "" for none).
	StageCeiling func(stage state.PipelineStage) string
	// WorkspaceRoot is the root StageCeiling reads when it is nil.
	WorkspaceRoot string
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
	if d.OperatorAdapter == nil {
		d.OperatorAdapter = func() string { return os.Getenv("NIGHTGAUGE_ADAPTER") }
	}
	if d.StageCeiling == nil {
		root := d.WorkspaceRoot
		d.StageCeiling = func(stage state.PipelineStage) string {
			mode := routing.ResolvePerformanceMode(root)
			return routing.RoutedTierEnvelopeForWorkspace(root, mode, string(stage)).Ceiling
		}
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
	shape := func(msg string) error { return refuse("invalid-request: "+msg, "%s", msg) }
	if adapter == "" {
		return shape("a model was requested without an adapter")
	}
	if !slices.Contains(remotePinAdapters, adapter) {
		return refuse("adapter-not-allowed",
			"the requested adapter is not one this machine can run (allowed: %s)", strings.Join(remotePinAdapters, ", "))
	}
	if model == "" {
		return nil
	}
	if len(model) > RemotePinMaxModelBytes {
		return shape(fmt.Sprintf("the requested model is longer than %d bytes", RemotePinMaxModelBytes))
	}
	if strings.HasPrefix(model, "-") {
		return shape("the requested model must not start with '-'")
	}
	if strings.Contains(model, "..") {
		return shape("the requested model must not contain '..'")
	}
	if adapter == "opencode" {
		if !remotePinOpenCodeModelRE.MatchString(model) {
			return shape("the requested model is not an opencode <provider>/<model> id")
		}
		return nil
	}
	if !remotePinModelRE.MatchString(model) {
		return shape("the requested model is not a model id: letters, digits and . _ : / - only")
	}
	return nil
}

// remotePinTierRank is the model's tier rank for the ceiling check. An
// opencode id is ranked by the id after its provider key, so
// anthropic/claude-opus-… ranks as opus; a local model ranks -1 and is never
// refused by a ceiling.
func remotePinTierRank(model string) int {
	if r := routing.TierRank(model); r >= 0 {
		return r
	}
	if _, id, ok := strings.Cut(model, "/"); ok {
		return routing.TierRank(id)
	}
	return -1
}

// ValidateRemotePin decides whether this machine can serve a requested
// adapter and model, and returns a *RemotePinRefusal when it cannot. The
// order is ADR-022 § 2's: shape, capability (agentic, the operator's adapter
// pin, adapter health), the performance ceiling, the adapter's own model
// check, credentials, the catalog. The first refusal wins, and nothing after
// the shape check sees a value the shape check refused.
func ValidateRemotePin(adapter, model string, deps RemotePinDeps) error {
	if err := ValidateRemotePinShape(adapter, model); err != nil {
		return err
	}
	if adapter == "" {
		return nil
	}
	d := deps.withDefaults()

	reg := adapters.NewRegistry()
	runner, err := reg.Get(adapter)
	if err != nil {
		return refuse("adapter-not-allowed", "adapter %s is not registered on this machine", adapter)
	}
	if !runner.Agentic() {
		return refuse("adapter-not-agentic: "+adapter, "adapter %s cannot run pipeline stages: it has no agentic tool loop", adapter)
	}
	if op := strings.TrimSpace(d.OperatorAdapter()); op != "" {
		opRunner, err := reg.Get(op)
		if err != nil || opRunner.Name() != runner.Name() {
			public := "operator-pinned-adapter"
			if err == nil {
				public += ": " + opRunner.Name()
			}
			return refuse(public, "this machine's operator pinned adapter %q (--adapter or NIGHTGAUGE_ADAPTER), so a request for %s is refused rather than substituted", op, adapter)
		}
	}
	if ok, reason := d.AdapterUsable(adapter); !ok {
		return refuse("adapter-unavailable: "+adapter, "adapter %s is not usable on this machine, and a remote request cannot enable it: %s", adapter, reason)
	}
	if model == "" {
		return nil
	}
	if rank := remotePinTierRank(model); rank >= 0 {
		for _, stage := range RemotePinStages {
			ceiling := d.StageCeiling(stage)
			if c := routing.TierRank(ceiling); c >= 0 && rank > c {
				return refuse("above-performance-ceiling",
					"model %s is above the performance mode's %s ceiling for stage %s, and a pinned model runs every stage", model, ceiling, stage)
			}
		}
	}
	if v, ok := runner.(interface{ ValidateModel(string) error }); ok {
		if err := v.ValidateModel(model); err != nil {
			return refuse("model-refused: "+adapter, "adapter %s refuses model %s: %v", adapter, model, err)
		}
	}
	if adapter != "opencode" {
		return nil
	}
	provider, _, _ := strings.Cut(model, "/")
	if err := adapters.OpenCodeCredentialRefusal(model, d.LookupEnv); err != nil {
		if provider == "anthropic" {
			return refuse("credentials-missing: ANTHROPIC_API_KEY", "%v", err)
		}
		return refuse("provider-not-allowed: "+adapter, "%v", err)
	}
	ids, err := d.Catalog(model)
	if err != nil {
		return refuse("catalog-unavailable", "the opencode catalog could not be read, so model %s cannot be confirmed: %v", model, err)
	}
	if slices.Contains(ids, model) {
		return nil
	}
	// OpenCode lists a hosted provider's models only when one of the
	// provider's variables is set, and a provider the per-run config declares
	// (a local endpoint) without any. So when the catalog lists nothing at all
	// for the provider and none of its variables is set, the provider did not
	// load for want of credentials — the doctor's reading too
	// (checkOpenCodeCatalog). A provider that did load lacks only the model.
	providerLoaded := slices.ContainsFunc(ids, func(id string) bool { return strings.HasPrefix(id, provider+"/") })
	if set, unset := adapters.OpenCodeProviderVars(model, d.LookupEnv); !providerLoaded && len(set) == 0 && len(unset) > 0 {
		return refuse("credentials-missing: "+strings.Join(unset, ", "),
			"model %s is not in this machine's opencode catalog, and provider %s has no credentials configured on this machine: none of %s is set",
			model, provider, strings.Join(unset, ", "))
	}
	return refuse("model-not-in-catalog", "model %s is not in this machine's opencode catalog (`opencode models`)", model)
}
