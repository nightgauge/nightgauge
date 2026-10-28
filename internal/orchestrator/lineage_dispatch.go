package orchestrator

import (
	"log"
	"os"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/models"
	"github.com/nightgauge/nightgauge/internal/state"
)

// affinityPrior is what the previous dispatch left warm, for
// ApplyCacheAffinity. nil when there was no previous dispatch.
type affinityPrior struct {
	previousModel    string
	previousProvider string
	adapter          string
	warmPrefixTokens int
	idle             time.Duration
}

// dispatchAffinityPrior reads the previous dispatch's lineage and its stage's
// token totals off runtime. The warm prefix is the stage's combined input
// (fresh input plus cache reads), the prompt the next stage would re-read.
func dispatchAffinityPrior(runtime *state.RuntimeState, adapter string, now time.Time) *affinityPrior {
	if runtime == nil {
		return nil
	}
	last := runtime.LastLineage()
	if last == nil {
		return nil
	}
	p := &affinityPrior{
		previousModel:    last.Model,
		previousProvider: last.Provider,
		adapter:          adapter,
		idle:             now.Sub(last.DispatchAt),
	}
	if res, ok := runtime.LastCompletedStage(); ok && string(res.Stage) == last.Stage {
		p.warmPrefixTokens = res.InputTokens
		p.idle = now.Sub(res.StartedAt.Add(res.Duration))
	}
	return p
}

// input builds the affinity decision's input for candidate on this adapter.
func (p *affinityPrior) input(stage, candidate string) AffinityInput {
	caps := adaptercompat.CacheFor(p.adapter)
	provider := models.ProviderFor(p.adapter, candidate)
	return AffinityInput{
		Stage:            stage,
		Candidate:        candidate,
		Previous:         p.previousModel,
		SameProvider:     provider == p.previousProvider,
		PrevWithinExpiry: caps.ExpirySeconds <= 0 || p.idle <= time.Duration(caps.ExpirySeconds)*time.Second,
		ImplicitCache:    caps.ImplicitPrefixCache,
		WarmPrefixTokens: p.warmPrefixTokens,
		Rates: func(model string) (AffinityRates, bool) {
			return registryAffinityRates(provider, model)
		},
	}
}

// registryAffinityRates prices model from the model registry. A model with no
// cache rates is unpriceable here: affinity then leaves the candidate alone.
func registryAffinityRates(provider, model string) (AffinityRates, bool) {
	d, ok := models.Resolve(provider, model)
	if !ok || d.Rates.CacheRead == nil || d.Rates.CacheCreation5m == nil {
		return AffinityRates{}, false
	}
	return AffinityRates{Input: d.Rates.Input, CacheRead: *d.Rates.CacheRead, CacheCreate5m: *d.Rates.CacheCreation5m}, true
}

// dispatchLineage is this dispatch's lineage, computed before the session
// resume so ResumeAllowed can gate it.
type dispatchLineage struct {
	prev       *state.LineageState
	prevKey    LineageKey
	key        LineageKey
	transition LineageTransition
	idle       time.Duration
}

// computeDispatchLineage builds the lineage key for a dispatch and compares
// it with the previous one. The account hash is salted per clone; when it
// cannot be computed it stays empty, which makes ResumeAllowed fail closed.
func computeDispatchLineage(runtime *state.RuntimeState, workspaceRoot, repo string, issue int,
	adapter, model, effort string, now time.Time) dispatchLineage {
	accountHash := ""
	if dir, err := layout.CloneDir(workspaceRoot); err == nil {
		if h, err := workspaceIdentityHash(dir, identityDescriptor(adapter, os.Getenv)); err == nil {
			accountHash = h
		} else {
			log.Printf("#%d: lineage identity hash unavailable: %v", issue, err)
		}
	}
	provider := models.ProviderFor(adapter, model)
	dl := dispatchLineage{key: LineageKey{
		Provider:    provider,
		AccountHash: accountHash,
		ModelFamily: modelFamily(provider, model),
		EffortClass: EffortClass(effort),
		Repo:        repo,
		Issue:       issue,
		RunID:       runtime.RunID,
	}}
	dl.prev = runtime.LastLineage()
	if dl.prev == nil {
		return dl
	}
	dl.prevKey = LineageKey{
		Provider:    dl.prev.Provider,
		AccountHash: dl.prev.AccountHash,
		ModelFamily: dl.prev.ModelFamily,
		EffortClass: dl.prev.EffortClass,
		Repo:        repo,
		Issue:       issue,
		RunID:       runtime.RunID,
	}
	if p := dispatchAffinityPrior(runtime, adapter, now); p != nil {
		dl.idle = p.idle
	}
	dl.transition = CompareLineage(dl.prevKey, dl.key, adaptercompat.CacheFor(adapter), dl.idle)
	return dl
}

// resumeAllowed reports whether a same-stage session resume may be used.
func (dl dispatchLineage) resumeAllowed() bool {
	return dl.prev != nil && ResumeAllowed(dl.prevKey, dl.key)
}

// record stores the lineage on runtime and, on a new lineage after a previous
// one, the handoff event with its estimated cold-start cost.
func (dl dispatchLineage) record(runtime *state.RuntimeState, stage state.PipelineStage, issue int,
	adapter, model string, aff AffinityDecision, resumeAttempted, resumeAccepted bool, now time.Time) {
	prevID := ""
	if dl.prev != nil {
		prevID = dl.prev.ID
	}
	id := NextLineageID(prevID, dl.key, dl.transition)
	var handoff *state.HandoffEvent
	if dl.prev != nil && !dl.transition.Same {
		cold := aff.PenaltyUSD
		if dl.transition.Reason == LineageExpired {
			cold = 0 // the cache was cold already; switching cost nothing extra
		}
		handoff = &state.HandoffEvent{
			FromStage:             dl.prev.Stage,
			ToStage:               string(stage),
			Reason:                dl.transition.Reason,
			FromLineage:           prevID,
			ToLineage:             id,
			EstimatedColdStartUSD: cold,
		}
		log.Printf("#%d: lineage handoff %s→%s reason=%s est_cold_start=$%.4f",
			issue, dl.prev.Stage, stage, dl.transition.Reason, cold)
	}
	runtime.RecordLineage(state.LineageState{
		ID:          id,
		Stage:       string(stage),
		Adapter:     adapter,
		Model:       model,
		Provider:    dl.key.Provider,
		AccountHash: dl.key.AccountHash,
		ModelFamily: dl.key.ModelFamily,
		EffortClass: dl.key.EffortClass,
		DispatchAt:  now,
	}, state.StageLineageRecord{LineageID: id, ResumeAttempted: resumeAttempted, ResumeAccepted: resumeAccepted}, handoff)
}

// currentAdapterName is the Go-side adapter the next dispatch runs on, or ""
// on the IPC path, where the extension owns the adapter.
func (s *Scheduler) currentAdapterName() string {
	if s.execMgr != nil && s.execMgr.HasAdapter() {
		return s.execMgr.AdapterName()
	}
	return ""
}
