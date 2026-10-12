package conversation

import (
	"fmt"
	"os"
	"slices"

	"github.com/nightgauge/nightgauge/internal/config"
	"github.com/nightgauge/nightgauge/internal/conversation/tools"
	"github.com/nightgauge/nightgauge/internal/execution/adapters"
	"github.com/nightgauge/nightgauge/internal/executionprofile"
	"github.com/nightgauge/nightgauge/internal/intelligence/routing"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/platform"
)

// defaultTurnModel is the band a turn starts from when the workspace names no
// ui.core.default_model: the stage dispatch path's own default
// (orchestrator.defaultDispatchModel).
const defaultTurnModel = routing.TierSonnet

// plan is how one turn will run: the profile resolved on the workspace, the
// conversational builder it dispatches through, and the argv that builder
// produced. It is built before the turn is claimed, so a turn this workspace
// cannot serve is refused rather than claimed.
type plan struct {
	profile platform.ExecutionProfile
	// model is the model the turn runs on.
	model string
	// effort is the rung passed to the adapter, "" when none is.
	effort string
	// effortNote says why no effort was applied, when none was.
	effortNote string
	builder    adapters.ConversationBuilder
	// opts are the options the argv was built from. The host rebuilds the
	// argv from them once the turn's tool server is up, adding only its
	// config file (startTools).
	opts adapters.ConversationTurnOptions
	cmd  string
	args []string
	env  map[string]string
}

// refusal is a turn this workspace cannot serve: the detail the turn's
// rejected acknowledgement carries, a fixed category and a short gloss.
type refusal struct{ detail string }

func (r *refusal) Error() string { return r.detail }

func refuse(category, format string, args ...any) *refusal {
	return &refusal{detail: category + ": " + fmt.Sprintf(format, args...)}
}

// planner resolves turn plans. Its fields are the seams tests replace.
type planner struct {
	resolveProfile func(workspaceRoot string) (platform.ExecutionProfile, error)
	builderFor     func(adapter string) (adapters.ConversationBuilder, bool)
	lookPath       func(file string) (string, error)
	getenv         func(string) string
}

func defaultPlanner(lookPath func(string) (string, error)) planner {
	reg := adapters.NewRegistry()
	return planner{
		resolveProfile: executionprofile.Resolve,
		builderFor:     reg.ConversationBuilder,
		lookPath:       lookPath,
		getenv:         os.Getenv,
	}
}

// plan resolves the turn's execution profile on the workspace, with the
// resolvers the pipeline dispatches with and nothing the command carries, and
// builds its argv through the adapter's conversational builder. A workspace
// whose adapter has no conversational builder is refused: it does not host
// conversations, and it does not advertise the capability either.
func (p planner) plan(workspaceRoot string) (plan, error) {
	profile, err := p.resolveProfile(workspaceRoot)
	if err != nil {
		return plan{}, refuse("profile-unresolved", "the workspace's execution profile did not resolve")
	}
	builder, ok := p.builderFor(profile.Adapter)
	if !ok {
		return plan{}, refuse("adapter-not-conversational",
			"the workspace's adapter %s does not host conversation turns", profile.Adapter)
	}
	mode := routing.PerformanceMode(profile.PerformanceMode)
	envelope := routing.Envelope(mode)
	cfg, err := config.Load(workspaceRoot)
	if err != nil {
		cfg = nil
	}

	// The model: the workspace's default band (or the dispatch default),
	// clamped into the mode's routed-tier envelope with the workspace's
	// max_model cap, as an unrouted stage's model is. A turn is not a stage,
	// so no stage pin applies (ADR-017 Decision 3).
	base := config.ResolveDefaultModel(cfg, p.getenv)
	if base == "" {
		base = defaultTurnModel
	}
	band := routing.ClampToEnvelope(base, routing.RoutedTierEnvelopeForWorkspace(workspaceRoot, mode, ""))
	provider := models.ProviderForAdapter(profile.Adapter)
	desc, known := models.Resolve(provider, band)
	model := band
	if known {
		model = desc.ID
	}

	// The effort: the advertised one, already clamped into the envelope by
	// executionprofile.Resolve. Spike #1568 § 11's rule, enforced here and
	// never left to the CLI, which drops an unsupported rung silently:
	//  1. no effort axis (the adapter has no control, or the model declares
	//     no rung): no flag, reported as not applied;
	//  2. a rung the model does not declare: refused before anything runs;
	//  3. a declared rung: passed through the adapter's native control.
	effort, note := profile.Effort, ""
	switch {
	case effort == "":
		note = "no effort named; the model's default applies"
	case !builder.HasEffortControl():
		effort, note = "", "the adapter has no per-turn effort control"
	case !known:
		effort, note = "", "the model declares no effort ladder"
	case len(desc.SupportedEfforts) == 0:
		effort, note = "", fmt.Sprintf("%s has no effort axis", desc.ID)
	case !slices.Contains(desc.SupportedEfforts, effort):
		return plan{}, refuse("effort-unsupported", "%s does not declare effort %s", desc.ID, effort)
	}

	// The tools are the conversation's own and nothing else (#1570): no
	// built-in tool, the conversation tools allowed by name, and the
	// boundary stated in the system prompt. No workspace setting reaches
	// any of the three.
	opts := adapters.ConversationTurnOptions{
		Model:              model,
		Effort:             effort,
		ThinkingOff:        envelope.ThinkingPolicy == "off",
		AllowedTools:       tools.AllowedToolRules(),
		AppendSystemPrompt: boundaryPrompt,
	}
	cmd, args, env := builder.BuildConversationTurn(opts)
	if _, err := p.lookPath(cmd); err != nil {
		return plan{}, refuse("adapter-unavailable", "%s is not installed on this host", cmd)
	}
	return plan{
		profile: profile, model: model, effort: effort, effortNote: note,
		builder: builder, opts: opts, cmd: cmd, args: args, env: env,
	}, nil
}
