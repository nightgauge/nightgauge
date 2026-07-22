package orchestrator

import (
	"os"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
)

// model_floor.go gives the Go autonomous scheduler parity with the TS
// SkillRunner's model_routing.minimum_model floor (#366). Before this, the
// dispatch loop resolved a stage's model purely from the cached prediction and
// a few haiku escalations — a per-stage minimum-model floor configured under
// model_routing.minimum_model was invisible on the autonomous path, so a stage
// floored to a premium tier silently ran (and attributed) a cheaper tier.
//
// The tier ordering is not re-declared here: it is derived from
// downgradeLadder (retry_engine.go), the single Go source of truth for tier
// strength, via normalizeTier. That keeps this floor and the model-unavailable
// downgrade ladder from drifting apart.

// tierRank maps a model reference — a registry tier alias like "opus" or a
// concrete ID like "claude-opus-4-8" — onto an ordinal where higher is more
// capable: haiku=0, sonnet=1, opus=2, fable=3. Returns -1 for models unknown
// to the registry (user-defined local models are never floored). This mirrors
// the inline tier map in skillRunner.ts's enforceMinimumModel and is the
// reverse index of downgradeLadder ([fable, opus, sonnet, haiku]).
func tierRank(model string) int {
	tier := normalizeTier(model) // retry_engine.go — alias or concrete ID → registry band
	if tier == "" {
		return -1
	}
	for i, t := range downgradeLadder {
		if t == tier {
			return len(downgradeLadder) - 1 - i
		}
	}
	return -1
}

// enforceMinimumModel raises selected to minimum when selected is strictly
// below the floor, mirroring skillRunner.ts's enforceMinimumModel (#366).
// Returns the (possibly raised) model as a bare tier alias, which the dispatch
// loop resolves through models.Get exactly like the predicted-model default.
//
// Two guard cases have no TS counterpart because the TS selector only ever
// passes one of the four tier aliases: a floor the registry does not recognize
// (rank -1) is ignored, and a selected model the registry does not recognize
// (a local/user-defined model) is left untouched — the floor never overrides
// an explicit local-model choice, consistent with how downgradeLadder skips
// local models.
func enforceMinimumModel(selected, minimum string) string {
	minRank := tierRank(minimum)
	if minRank < 0 {
		return selected
	}
	selRank := tierRank(selected)
	if selRank < 0 || selRank >= minRank {
		return selected
	}
	return minimum
}

// configModelFloors returns the model_routing.minimum_model map (stage → tier)
// from the workspace config, or nil when unset or unreadable. The caller loads
// it once per run and passes it to stageModelFloor per stage.
func configModelFloors(workspaceRoot string) map[string]string {
	cfg, err := config.Load(workspaceRoot)
	if err != nil || cfg == nil || cfg.ModelRouting == nil {
		return nil
	}
	return cfg.ModelRouting.MinimumModel
}

// stageModelFloor resolves the per-stage minimum-model floor with the same
// precedence as the TS modelResolver.getMinimumModel: the
// NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_{STAGE} env var (stage upper-cased,
// dashes → underscores) wins over the config map. Returns "" when no floor is
// configured for the stage.
func stageModelFloor(configFloors map[string]string, stage string) string {
	envKey := "NIGHTGAUGE_MODEL_ROUTING_MIN_MODEL_" + strings.ToUpper(strings.ReplaceAll(stage, "-", "_"))
	if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
		return v
	}
	return configFloors[stage]
}
