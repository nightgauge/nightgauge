// Package orchestrator — wave_orchestrator.go implements epic-level parallel
// subagent orchestration. When the scheduler detects an epic board item, it
// delegates to RunEpicWaves which:
//  1. Fetches sub-issues and analyzes dependencies (teams.DetectDependencies)
//  2. Computes execution waves (teams.CalculateWaves)
//  3. Splits token budget across wave members (teams.SplitBudget)
//  4. Spawns parallel subagents per wave, each in its own git worktree
//  5. Waits for wave completion, then cascades to next wave
//  6. Failure isolation: one subagent failure does not cancel siblings
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/intelligence/batch"
	"github.com/nightgauge/nightgauge/internal/intelligence/teams"
	"github.com/nightgauge/nightgauge/internal/layout"
	"github.com/nightgauge/nightgauge/internal/state"
	"github.com/nightgauge/nightgauge/pkg/types"
)

// ScalingConfig controls dynamic subagent concurrency within a wave.
type ScalingConfig struct {
	MaxConcurrent     int   // Hard ceiling on parallel agents per wave (default: 6)
	MinBudgetPerAgent int64 // Minimum tokens per agent to be viable (default: 100K)
}

// DefaultScalingConfig returns sensible defaults for agent scaling.
func DefaultScalingConfig() ScalingConfig {
	return ScalingConfig{
		MaxConcurrent:     6,
		MinBudgetPerAgent: 100_000,
	}
}

// ScalingDecision describes why a particular concurrency was chosen.
type ScalingDecision struct {
	Wave            int    `json:"wave"`
	WaveSize        int    `json:"waveSize"`
	Concurrency     int    `json:"concurrency"`
	Reason          string `json:"reason"` // "ideal", "config_ceiling", "budget_constraint"
	RemainingBudget int64  `json:"remainingBudget"`
	BudgetPerAgent  int64  `json:"budgetPerAgent"`
}

// scaleAgents determines optimal concurrency for a wave.
// Factors: wave size, remaining budget, config ceiling.
// The returned ScalingDecision includes the reason for the choice.
func scaleAgents(waveSize int, remainingBudget int64, cfg ScalingConfig) ScalingDecision {
	decision := ScalingDecision{
		WaveSize:        waveSize,
		RemainingBudget: remainingBudget,
	}

	// Start with wave size (ideal: one goroutine per sub-issue)
	concurrency := waveSize
	reason := "ideal"

	// Apply config ceiling
	if cfg.MaxConcurrent > 0 && concurrency > cfg.MaxConcurrent {
		concurrency = cfg.MaxConcurrent
		reason = "config_ceiling"
	}

	// Budget constraint: if budget per agent would be below minimum viable,
	// reduce concurrency to ensure each agent has enough tokens
	if remainingBudget > 0 && cfg.MinBudgetPerAgent > 0 {
		maxFromBudget := int(remainingBudget / cfg.MinBudgetPerAgent)
		if maxFromBudget < concurrency {
			concurrency = maxFromBudget
			reason = "budget_constraint"
		}
	}

	// Floor: always at least 1
	if concurrency < 1 {
		concurrency = 1
	}

	decision.Concurrency = concurrency
	decision.Reason = reason
	if concurrency > 0 {
		decision.BudgetPerAgent = remainingBudget / int64(concurrency)
	}

	return decision
}

// WaveOrchestrator coordinates parallel subagent execution across waves.
type WaveOrchestrator struct {
	scheduler *Scheduler

	// Configuration
	maxConcurrent int           // Max parallel agents per wave (2-6, default 4)
	totalBudget   int           // Total token budget for the epic run
	scalingConfig ScalingConfig // Dynamic scaling settings

	// State tracking
	mu          sync.Mutex
	epicNumber  int
	repo        string
	waves       []teams.WaveAssignment
	currentWave int
	// agentResults is keyed by the sub-issue's repository and number
	// (repoIssueKey): an epic's sub-issues can live in several repositories,
	// where one number names a different issue in each (#2377).
	agentResults map[string]*AgentResult
	strategy     batch.Strategy
}

// AgentResult tracks the outcome of a single subagent pipeline execution.
type AgentResult struct {
	IssueNumber int `json:"issueNumber"`
	// Repo is the sub-issue's own repository ("owner/name").
	Repo         string        `json:"repo,omitempty"`
	Success      bool          `json:"success"`
	Error        string        `json:"error,omitempty"`
	InputTokens  int           `json:"inputTokens"`
	OutputTokens int           `json:"outputTokens"`
	CostUSD      float64       `json:"costUsd"`
	Duration     time.Duration `json:"duration"`
	WaveIndex    int           `json:"waveIndex"`
}

// WaveStatus represents the current state of wave orchestration.
type WaveStatus struct {
	EpicNumber    int          `json:"epicNumber"`
	Repo          string       `json:"repo"`
	Strategy      string       `json:"strategy"`
	TotalWaves    int          `json:"totalWaves"`
	CurrentWave   int          `json:"currentWave"`
	MaxConcurrent int          `json:"maxConcurrent"`
	Waves         []WaveDetail `json:"waves"`
	// AgentResults is keyed by repoIssueKey ("owner/repo#N").
	AgentResults map[string]*AgentResult `json:"agentResults"`
	Summary      *WaveSummary            `json:"summary,omitempty"`
}

// WaveDetail describes a single wave.
type WaveDetail struct {
	WaveIndex int `json:"waveIndex"`
	// Issues names each sub-issue as "owner/repo#N".
	Issues []string `json:"issues"`
	Status string   `json:"status"` // pending, running, completed, failed
}

// WaveSummary provides aggregate metrics after epic completion.
type WaveSummary struct {
	TotalIssues    int           `json:"totalIssues"`
	Succeeded      int           `json:"succeeded"`
	Failed         int           `json:"failed"`
	TotalTokensIn  int           `json:"totalTokensIn"`
	TotalTokensOut int           `json:"totalTokensOut"`
	TotalCostUSD   float64       `json:"totalCostUsd"`
	TotalDuration  time.Duration `json:"totalDuration"`
	SpeedupFactor  float64       `json:"speedupFactor"` // estimated sequential time / actual time
}

// newWaveOrchestrator creates a wave orchestrator for an epic.
func newWaveOrchestrator(s *Scheduler, epicNumber int, repo string, maxConcurrent, totalBudget int) *WaveOrchestrator {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	if maxConcurrent > 12 {
		maxConcurrent = 12
	}
	if totalBudget <= 0 {
		totalBudget = 2_000_000 // Default 2M tokens for epic
	}

	// Build ScalingConfig from the config file if available, else use defaults
	sc := DefaultScalingConfig()
	if s != nil && s.scalingConfig != nil {
		sc = *s.scalingConfig
	}
	// Override maxConcurrent from the passed-in value if it was explicitly set
	if maxConcurrent > 0 {
		sc.MaxConcurrent = maxConcurrent
	}

	return &WaveOrchestrator{
		scheduler:     s,
		maxConcurrent: maxConcurrent,
		totalBudget:   totalBudget,
		scalingConfig: sc,
		epicNumber:    epicNumber,
		repo:          repo,
		agentResults:  make(map[string]*AgentResult),
	}
}

// RunEpicWaves is the entry point for epic-level parallel orchestration.
// It detects sub-issues, plans waves, and executes them with parallel subagents.
// Returns true if all sub-issues completed successfully.
func (s *Scheduler) RunEpicWaves(ctx context.Context, item types.BoardItem) bool {
	log.Printf("epic #%d: starting wave orchestration for %q", item.Number, item.Title)

	wo := newWaveOrchestrator(s, item.Number, item.Repo, s.maxPerRepo, 0)

	// Phase 1: Fetch sub-issues and their details
	ownerPart, repoPart := splitOwnerRepo(item.Repo)
	subIssues, issueDetails, blockedBy, err := wo.fetchSubIssueDetails(ctx, ownerPart, repoPart, item)
	if err != nil {
		log.Printf("epic #%d: failed to fetch sub-issues: %v", item.Number, err)
		return false
	}

	if len(subIssues) == 0 {
		log.Printf("epic #%d: no open sub-issues to process", item.Number)
		return true
	}

	// Phase 2: Run batch assessment
	assessment := wo.assessBatch(issueDetails)
	wo.strategy = assessment.Strategy
	log.Printf("epic #%d: batch assessment — strategy=%s, reasoning=%q, estimated_cost=$%.2f",
		item.Number, assessment.Strategy, assessment.Reasoning, assessment.EstimatedCostUSD)

	// If sequential is recommended, fall back to queue-based execution
	if assessment.Strategy == batch.StrategySequential {
		log.Printf("epic #%d: sequential strategy recommended — falling back to queue", item.Number)
		return false // Caller should fall back to EnqueueEpic
	}

	// Phase 3: Detect dependencies and calculate waves
	deps := wo.detectDependencies(subIssues, issueDetails, blockedBy)
	waves, err := teams.CalculateWaves(subIssues, deps)
	if err != nil {
		log.Printf("epic #%d: wave calculation failed: %v", item.Number, err)
		return false
	}

	// Merge if needed to respect concurrency limit
	waves = teams.MergeWaves(waves, wo.maxConcurrent)
	wo.waves = waves

	log.Printf("epic #%d: planned %d waves:", item.Number, len(waves))
	for _, w := range waves {
		log.Printf("  wave %d: %v", w.WaveIndex, wo.subIssueRefs(w.Issues))
	}

	// Phase 4: Persist wave plan
	wo.persistWavePlan()

	// Phase 5: Execute waves sequentially, issues within each wave in parallel
	epicStart := time.Now()
	allSuccess := true

	// Calculate remaining budget for scaling decisions
	remainingBudget := int64(wo.totalBudget)

	for waveIdx, wave := range waves {
		select {
		case <-ctx.Done():
			log.Printf("epic #%d: cancelled at wave %d", item.Number, waveIdx)
			return false
		default:
		}

		wo.mu.Lock()
		wo.currentWave = waveIdx
		wo.mu.Unlock()

		log.Printf("epic #%d: ═══ Wave %d/%d — issues %v ═══",
			item.Number, waveIdx+1, len(waves), wo.subIssueRefs(wave.Issues))

		// Dynamic scaling: determine concurrency for this wave
		decision := scaleAgents(len(wave.Issues), remainingBudget, wo.scalingConfig)
		decision.Wave = waveIdx
		log.Printf("epic #%d: wave %d scaling — waveSize=%d, concurrency=%d, reason=%s, budgetPerAgent=%d",
			item.Number, waveIdx, decision.WaveSize, decision.Concurrency, decision.Reason, decision.BudgetPerAgent)

		// Emit scaling decision event for UI observability
		if s.onScalingDecision != nil {
			s.onScalingDecision(item.Number, decision)
		}

		// Split budget for this wave's issues
		waveBudget := wo.totalBudget / len(waves)
		budgetResult := teams.SplitBudget(wave.Issues, waveBudget, teams.StrategyProportional)

		// Check for file conflicts within this wave
		conflicts := teams.DetectFileConflicts(wave.Issues)
		if len(conflicts) > 0 {
			for _, c := range conflicts {
				log.Printf("epic #%d: wave %d file conflict: %s (%s) — issues %v",
					item.Number, waveIdx, c.Path, c.Severity, c.Issues)
			}
			// Error-severity conflicts force sequential within this wave
			hasError := false
			for _, c := range conflicts {
				if c.Severity == "error" {
					hasError = true
					break
				}
			}
			if hasError {
				log.Printf("epic #%d: wave %d has file conflicts — running sequentially", item.Number, waveIdx)
				for _, si := range wave.Issues {
					result := wo.runSubagent(ctx, si, item, waveIdx, wo.budgetForIssue(budgetResult, si))
					wo.recordResult(result)
					if !result.Success {
						allSuccess = false
					}
				}
				// Deduct wave budget from remaining
				remainingBudget -= int64(waveBudget)
				continue
			}
		}

		// Run wave issues in parallel, respecting scaled concurrency
		waveResults := wo.runWaveScaled(ctx, wave, item, waveIdx, budgetResult, decision.Concurrency)
		for _, result := range waveResults {
			wo.recordResult(result)
			if !result.Success {
				allSuccess = false
				// Check if downstream waves depend on this failed issue
				// We don't abort the entire epic — just log the failure
				log.Printf("epic #%d: wave %d issue %s#%d failed — downstream dependencies may be affected",
					item.Number, waveIdx, result.Repo, result.IssueNumber)
			}
		}

		// Deduct wave budget from remaining
		remainingBudget -= int64(waveBudget)

		log.Printf("epic #%d: wave %d complete — %d/%d succeeded",
			item.Number, waveIdx, wo.waveSuccessCount(waveIdx), len(wave.Issues))
	}

	// Phase 6: Summary
	epicDuration := time.Since(epicStart)
	summary := wo.buildSummary(epicDuration)
	wo.mu.Lock()
	wo.mu.Unlock()

	log.Printf("epic #%d: ═══ Epic Complete ═══", item.Number)
	log.Printf("epic #%d:   issues: %d succeeded, %d failed of %d total",
		item.Number, summary.Succeeded, summary.Failed, summary.TotalIssues)
	log.Printf("epic #%d:   tokens: %d in / %d out",
		item.Number, summary.TotalTokensIn, summary.TotalTokensOut)
	log.Printf("epic #%d:   cost: $%.4f", item.Number, summary.TotalCostUSD)
	log.Printf("epic #%d:   duration: %s (%.1fx speedup)", item.Number, epicDuration, summary.SpeedupFactor)

	// Persist final status
	wo.persistWaveStatus(summary)

	return allSuccess
}

// fetchSubIssueDetails fetches all open sub-issues and their full details.
// subIssues and issueDetails are index-aligned. blockedBy maps each sub-issue
// (wo.subIssueKey) to the repoIssueKey of each of its open blockers: a
// blocker's number names an issue in the blocker's own repository, and the
// epic's sub-issues can share a number across repositories (#2377).
func (wo *WaveOrchestrator) fetchSubIssueDetails(ctx context.Context, owner, repo string, item types.BoardItem) ([]teams.SubIssue, []batch.IssueInput, map[string][]string, error) {
	epic, err := wo.scheduler.issueSvc.GetEpicProgressByNumber(ctx, owner, repo, item.Number)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("fetch epic progress: %w", err)
	}

	var subIssues []teams.SubIssue
	var issueDetails []batch.IssueInput
	blockedByKeys := make(map[string][]string)

	for _, si := range epic.SubIssues {
		if !strings.EqualFold(si.State, "OPEN") {
			continue
		}

		// Fetch full issue details for dependency/complexity analysis
		siOwner, siRepo := owner, repo
		if si.Repo != "" && si.Repo != item.Repo {
			parts := strings.SplitN(si.Repo, "/", 2)
			if len(parts) == 2 {
				siOwner, siRepo = parts[0], parts[1]
			}
		}

		// The plan uses the sub-issue's body, labels and blocker list, so only
		// the blocker list is read whole.
		issue, err := wo.scheduler.issueSvc.GetIssueWithRelations(ctx, siOwner, siRepo, si.Number, gh.RelationBlockedBy)
		if errors.Is(err, gh.ErrConnectionTruncated) {
			// A blocker list read only in part would plan the sub-issue into
			// an earlier wave than its unseen blockers allow, and leaving it
			// out would drop its siblings' edges to it as well.
			return nil, nil, nil, fmt.Errorf("fetch sub-issue %s/%s#%d: %w", siOwner, siRepo, si.Number, err)
		}
		if err != nil {
			log.Printf("epic #%d: warn — failed to fetch sub-issue #%d: %v", wo.epicNumber, si.Number, err)
			continue
		}

		// Extract file references from body for dependency detection
		files := teams.ExtractTargetFiles(issue.Body)

		// Determine complexity from labels
		complexity := "medium"
		for _, label := range issue.Labels {
			switch {
			case strings.Contains(label, "simple") || strings.Contains(label, "trivial"):
				complexity = "simple"
			case strings.Contains(label, "complex") || strings.Contains(label, "hard"):
				complexity = "complex"
			}
		}

		sub := teams.SubIssue{
			Number:     si.Number,
			Title:      si.Title,
			Repo:       siOwner + "/" + siRepo,
			Files:      files,
			Complexity: complexity,
		}
		subIssues = append(subIssues, sub)

		// A blocker that records no repository is in the sub-issue's own.
		blockedBy := make([]int, 0, len(issue.BlockedBy))
		for _, b := range issue.BlockedBy {
			if strings.EqualFold(b.State, "OPEN") {
				blockedBy = append(blockedBy, b.Number)
				blockerRepo := b.Repo
				if blockerRepo == "" {
					blockerRepo = sub.Repo
				}
				key := wo.subIssueKey(sub)
				blockedByKeys[key] = append(blockedByKeys[key], repoIssueKey(blockerRepo, b.Number))
			}
		}

		issueDetails = append(issueDetails, batch.IssueInput{
			Number:    si.Number,
			Title:     si.Title,
			Body:      issue.Body,
			Labels:    issue.Labels,
			BlockedBy: blockedBy,
		})
	}

	return subIssues, issueDetails, blockedByKeys, nil
}

// assessBatch runs the batch assessor on the sub-issues.
func (wo *WaveOrchestrator) assessBatch(issues []batch.IssueInput) batch.Assessment {
	assessor := batch.NewAssessor()
	return assessor.Assess(issues)
}

// detectDependencies uses the teams package to detect inter-issue dependencies.
//
// issueDetails is index-aligned with subIssues, as fetchSubIssueDetails builds
// them. blockedBy is fetchSubIssueDetails' map of each sub-issue's open
// blockers. Both are matched by repository and number: two sub-issues of one
// epic can share a number in different repositories, and matched by number
// alone the second read the first's body, and a blocker in one repository
// ordered the sub-issue with its number in another (#2377).
func (wo *WaveOrchestrator) detectDependencies(subIssues []teams.SubIssue, issueDetails []batch.IssueInput, blockedBy map[string][]string) map[int][]int {
	// Build source text array (issue bodies) for heuristic analysis
	sources := make([]string, len(subIssues))
	for i, si := range subIssues {
		if i < len(issueDetails) && issueDetails[i].Number == si.Number {
			sources[i] = issueDetails[i].Body
		}
	}

	config := teams.DefaultDependencyConfig()
	deps := teams.DetectDependencies(subIssues, sources, config)

	// Also incorporate explicit blockedBy relationships from GitHub. A blocker
	// outside the epic's open sub-issues is no edge here.
	index := make(map[string]int, len(subIssues))
	for j, si := range subIssues {
		index[wo.subIssueKey(si)] = j
	}
	for i, si := range subIssues {
		for _, blocker := range blockedBy[wo.subIssueKey(si)] {
			j, ok := index[blocker]
			if !ok || j == i {
				continue
			}
			// i depends on j
			if !containsInt(deps[i], j) {
				deps[i] = append(deps[i], j)
			}
		}
	}

	return deps
}

// runWaveParallel spawns parallel subagents for all issues in a wave.
func (wo *WaveOrchestrator) runWaveParallel(ctx context.Context, wave teams.WaveAssignment, epicItem types.BoardItem, waveIdx int, budgetResult teams.BudgetResult) []*AgentResult {
	var wg sync.WaitGroup
	results := make([]*AgentResult, len(wave.Issues))

	for i, si := range wave.Issues {
		wg.Add(1)
		go func(idx int, issue teams.SubIssue) { // lifecycle: joined via wg.Wait() below (#491 pin)
			defer wg.Done()
			budget := wo.budgetForIssue(budgetResult, issue)
			results[idx] = wo.runSubagent(ctx, issue, epicItem, waveIdx, budget)
		}(i, si)
	}

	wg.Wait()
	return results
}

// runWaveScaled runs wave issues respecting the scaled concurrency limit.
// When concurrency < wave size, issues are split into sequential batches.
// Each batch runs its issues in parallel up to the concurrency limit.
//
//	Wave has 8 items, concurrency = 4:
//	  Batch 1: items 1-4 (parallel)
//	  Batch 2: items 5-8 (parallel, after batch 1 completes)
func (wo *WaveOrchestrator) runWaveScaled(ctx context.Context, wave teams.WaveAssignment, epicItem types.BoardItem, waveIdx int, budgetResult teams.BudgetResult, concurrency int) []*AgentResult {
	issues := wave.Issues

	// Fast path: concurrency >= wave size, run all in parallel
	if concurrency >= len(issues) {
		return wo.runWaveParallel(ctx, wave, epicItem, waveIdx, budgetResult)
	}

	// Slow path: split into sequential batches
	results := make([]*AgentResult, len(issues))
	for batchStart := 0; batchStart < len(issues); batchStart += concurrency {
		select {
		case <-ctx.Done():
			// Fill remaining slots with cancellation results
			for i := batchStart; i < len(issues); i++ {
				results[i] = &AgentResult{
					IssueNumber: issues[i].Number,
					Repo:        wo.subIssueRepo(issues[i]),
					WaveIndex:   waveIdx,
					Error:       "cancelled",
				}
			}
			return results
		default:
		}

		batchEnd := batchStart + concurrency
		if batchEnd > len(issues) {
			batchEnd = len(issues)
		}
		batchIssues := issues[batchStart:batchEnd]

		log.Printf("epic #%d: wave %d batch %d/%d — issues %d-%d of %d (concurrency=%d)",
			wo.epicNumber, waveIdx, batchStart/concurrency+1,
			(len(issues)+concurrency-1)/concurrency,
			batchStart+1, batchEnd, len(issues), concurrency)

		var wg sync.WaitGroup
		for i, si := range batchIssues {
			wg.Add(1)
			go func(globalIdx int, issue teams.SubIssue) { // lifecycle: joined via wg.Wait() below (#491 pin)
				defer wg.Done()
				budget := wo.budgetForIssue(budgetResult, issue)
				results[globalIdx] = wo.runSubagent(ctx, issue, epicItem, waveIdx, budget)
			}(batchStart+i, si)
		}
		wg.Wait()
	}

	return results
}

// subIssueItem is the synthetic board item a wave runs a sub-issue as.
//
// The sub-issue runs in its own repository (si.Repo). A sub-issue of this epic
// in another repository used to run as the epic repository's issue with the
// same number (#2377). ParentNumber and ParentRepo link it to the epic, so the
// scheduler can inject accumulated sibling context into the planning/dev
// prompt (#4096); left 0, they kept the epic-context loop open.
func (wo *WaveOrchestrator) subIssueItem(si teams.SubIssue, epicItem types.BoardItem) types.BoardItem {
	repo := si.Repo
	if repo == "" {
		repo = epicItem.Repo
	}
	return types.BoardItem{
		Number:       si.Number,
		Title:        si.Title,
		Repo:         repo,
		Labels:       epicItem.Labels,
		ID:           fmt.Sprintf("epic-%d-sub-%d", wo.epicNumber, si.Number),
		ParentNumber: wo.epicNumber,
		ParentRepo:   epicItem.Repo,
	}
}

// runSubagent executes the full pipeline for a single sub-issue.
// Each subagent gets its own worktree for isolation.
func (wo *WaveOrchestrator) runSubagent(ctx context.Context, si teams.SubIssue, epicItem types.BoardItem, waveIdx int, tokenBudget int) *AgentResult {
	start := time.Now()
	subItem := wo.subIssueItem(si, epicItem)
	result := &AgentResult{
		IssueNumber: si.Number,
		Repo:        subItem.Repo,
		WaveIndex:   waveIdx,
	}

	log.Printf("epic #%d: wave %d — starting subagent for %s#%d %q",
		wo.epicNumber, waveIdx, subItem.Repo, si.Number, si.Title)

	// Create a per-subagent child context for cancellation isolation
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Track that this subagent is running — use a done channel for the caller
	done := make(chan struct{})
	var pipelineSuccess bool
	var pipelineRuntime *state.RuntimeState

	// Run the pipeline synchronously (runPipeline blocks until done).
	// lifecycle: joined below via the done-channel select (ctx.Done()/<-done)
	// before this function returns — #491 pin allowlist cites this line.
	go func() {
		defer close(done)
		wo.scheduler.runPipeline(subCtx, subItem)
	}()

	// Wait for pipeline completion or context cancellation
	select {
	case <-done:
		// Pipeline completed
	case <-ctx.Done():
		cancel() // Cancel the subagent
		<-done   // Wait for cleanup
		result.Error = "cancelled"
		result.Duration = time.Since(start)
		return result
	}

	// runPipeline fires onPipelineComplete in its defer, which persists the
	// runtime state — read it back from the state file, in the checkout the
	// run was rooted in.
	pipelineSuccess, pipelineRuntime = wo.readPipelineState(subItem.Repo, si.Number)

	result.Success = pipelineSuccess
	result.Duration = time.Since(start)

	if pipelineRuntime != nil {
		result.InputTokens = pipelineRuntime.InputTokens
		result.OutputTokens = pipelineRuntime.OutputTokens
		result.CostUSD = pipelineRuntime.TotalCostUSD
	}

	if result.Success {
		log.Printf("epic #%d: wave %d — subagent %s#%d completed successfully in %s",
			wo.epicNumber, waveIdx, subItem.Repo, si.Number, result.Duration)
		// Append sub-issue findings to epic context accumulator
		wo.appendSubIssueToEpicContext(si)
	} else {
		errMsg := "pipeline failed"
		if result.Error != "" {
			errMsg = result.Error
		}
		log.Printf("epic #%d: wave %d — subagent %s#%d failed: %s",
			wo.epicNumber, waveIdx, subItem.Repo, si.Number, errMsg)
		result.Error = errMsg
	}

	return result
}

// readPipelineState reads the persisted pipeline state of repo's issueNumber.
//
// runPipeline roots a run, and persists its runtime snapshot, in the checkout
// of the run's own repository (resolveRunRoot(item.Repo)), so the snapshot is
// read there. Since a wave runs a sub-issue in its own repository (#2377), the
// launch checkout holds no snapshot of a sub-issue in another repository: read
// there, a merged sub-issue counted as failed, or the launch repository's run
// of the same number answered for it. Only repo's snapshots count, because the
// file name carries the number alone.
func (wo *WaveOrchestrator) readPipelineState(repo string, issueNumber int) (bool, *state.RuntimeState) {
	root := wo.scheduler.runRoot(repo)
	if root == "" {
		return false, nil
	}
	stateDir, err := layout.PipelineStateDir(root)
	if err != nil {
		return false, nil
	}
	// Issue-addressed: the wave orchestrator asks "did sub-issue #N succeed?",
	// which is a question about the run it just drove. Standard pick — prefer
	// non-terminal, then newest StartedAt (ADR-017 Decision 8).
	runtime, err := state.PickPersistedStateForRepoIssue(stateDir, repo, issueNumber)
	if err != nil {
		return false, nil
	}
	// A pipeline is successful if it accounted for all stages AND the PR is
	// actually merged on GitHub (issue #2843 — stage-count alone was letting
	// false-complete runs mark the board as Done with an open PR). Routing
	// fast-track (#4126) may skip planning/validate, so a stage counts as
	// accounted-for when it either completed or was deliberately skipped —
	// completed + skipped must equal the 6-stage order (see the success-calc
	// rule in .claude/rules/vscode-extension.md). The PR-merged check below
	// still guards against a false-complete run with an open PR.
	if len(runtime.CompletedStages)+len(runtime.SkippedStages) != 6 || runtime.Stage != state.StagePRMerge {
		return false, runtime
	}
	if wo.scheduler != nil {
		if merged, reason := wo.scheduler.verifyPRMerged(context.Background(), runtime.PrUrl, issueNumber); !merged {
			log.Printf("epic #%d: sub-issue %s#%d pr-merge stage completed but PR not merged — %s",
				wo.epicNumber, repo, issueNumber, reason)
			return false, runtime
		}
	}
	return true, runtime
}

// recordResult stores an agent result under its sub-issue's repository and
// number. A result that records no repository is the epic repository's.
func (wo *WaveOrchestrator) recordResult(result *AgentResult) {
	wo.mu.Lock()
	defer wo.mu.Unlock()
	if result.Repo == "" {
		result.Repo = wo.repo
	}
	wo.agentResults[repoIssueKey(result.Repo, result.IssueNumber)] = result
}

// waveSuccessCount returns how many issues succeeded in a wave.
func (wo *WaveOrchestrator) waveSuccessCount(waveIdx int) int {
	wo.mu.Lock()
	defer wo.mu.Unlock()
	count := 0
	for _, r := range wo.agentResults {
		if r.WaveIndex == waveIdx && r.Success {
			count++
		}
	}
	return count
}

// budgetForIssue returns the token budget allocated to si, matched by
// repository and number: two sub-issues in one wave can share a number.
func (wo *WaveOrchestrator) budgetForIssue(budgetResult teams.BudgetResult, si teams.SubIssue) int {
	for _, alloc := range budgetResult.Allocations {
		if alloc.IssueNumber == si.Number && strings.EqualFold(alloc.Repo, si.Repo) {
			return alloc.TokenBudget
		}
	}
	return wo.totalBudget / 6 // Fallback: equal split
}

// subIssueRepo is si's own repository; a sub-issue that records none is the
// epic repository's.
func (wo *WaveOrchestrator) subIssueRepo(si teams.SubIssue) string {
	if si.Repo != "" {
		return si.Repo
	}
	return wo.repo
}

// subIssueKey keys si by repository and number (repoIssueKey), as
// agentResults and the blocker map are keyed.
func (wo *WaveOrchestrator) subIssueKey(si teams.SubIssue) string {
	return repoIssueKey(wo.subIssueRepo(si), si.Number)
}

// subIssueRefs names each sub-issue as "owner/repo#N", for logs and the wave
// status file.
func (wo *WaveOrchestrator) subIssueRefs(issues []teams.SubIssue) []string {
	refs := make([]string, len(issues))
	for i, si := range issues {
		refs[i] = fmt.Sprintf("%s#%d", wo.subIssueRepo(si), si.Number)
	}
	return refs
}

// buildSummary creates aggregate metrics for the epic run.
func (wo *WaveOrchestrator) buildSummary(totalDuration time.Duration) *WaveSummary {
	wo.mu.Lock()
	defer wo.mu.Unlock()

	summary := &WaveSummary{
		TotalDuration: totalDuration,
	}

	var sequentialEstimate time.Duration
	for _, r := range wo.agentResults {
		summary.TotalIssues++
		summary.TotalTokensIn += r.InputTokens
		summary.TotalTokensOut += r.OutputTokens
		summary.TotalCostUSD += r.CostUSD
		sequentialEstimate += r.Duration
		if r.Success {
			summary.Succeeded++
		} else {
			summary.Failed++
		}
	}

	if totalDuration > 0 {
		summary.SpeedupFactor = float64(sequentialEstimate) / float64(totalDuration)
	} else {
		summary.SpeedupFactor = 1.0
	}

	return summary
}

// epicStateDir is the pipeline state of the epic's own repository, where
// the wave plan, the wave status and the epic context are kept. The files are
// named by the epic's number, which names an issue only in that repository:
// kept in the launch checkout, two epics that share a number shared them
// (#2377).
func (wo *WaveOrchestrator) epicStateDir() (string, error) {
	root, err := wo.scheduler.resolveRunRoot(wo.repo)
	if err != nil {
		return "", err
	}
	return layout.PipelineStateDir(root)
}

// persistWavePlan writes the wave plan to disk for observability.
func (wo *WaveOrchestrator) persistWavePlan() {
	dir, err := wo.epicStateDir()
	if err != nil {
		log.Printf("epic #%d: pipeline dir not resolved: %v", wo.epicNumber, err)
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("epic #%d: failed to create pipeline dir: %v", wo.epicNumber, err)
		return
	}

	plan := struct {
		EpicNumber    int                    `json:"epicNumber"`
		Repo          string                 `json:"repo"`
		Strategy      string                 `json:"strategy"`
		MaxConcurrent int                    `json:"maxConcurrent"`
		TotalBudget   int                    `json:"totalBudget"`
		Waves         []teams.WaveAssignment `json:"waves"`
		CreatedAt     time.Time              `json:"createdAt"`
	}{
		EpicNumber:    wo.epicNumber,
		Repo:          wo.repo,
		Strategy:      string(wo.strategy),
		MaxConcurrent: wo.maxConcurrent,
		TotalBudget:   wo.totalBudget,
		Waves:         wo.waves,
		CreatedAt:     time.Now(),
	}

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		log.Printf("epic #%d: failed to marshal wave plan: %v", wo.epicNumber, err)
		return
	}

	path := filepath.Join(dir, fmt.Sprintf("wave-plan-%d.json", wo.epicNumber))
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("epic #%d: failed to write wave plan: %v", wo.epicNumber, err)
	}
}

// persistWaveStatus writes the final wave execution status to disk.
func (wo *WaveOrchestrator) persistWaveStatus(summary *WaveSummary) {
	dir, err := wo.epicStateDir()
	if err != nil {
		log.Printf("epic #%d: pipeline dir not resolved: %v", wo.epicNumber, err)
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		log.Printf("epic #%d: failed to create pipeline dir: %v", wo.epicNumber, err)
		return
	}

	wo.mu.Lock()
	status := WaveStatus{
		EpicNumber:    wo.epicNumber,
		Repo:          wo.repo,
		Strategy:      string(wo.strategy),
		TotalWaves:    len(wo.waves),
		CurrentWave:   wo.currentWave,
		MaxConcurrent: wo.maxConcurrent,
		AgentResults:  wo.agentResults,
		Summary:       summary,
	}
	for _, w := range wo.waves {
		detail := WaveDetail{
			WaveIndex: w.WaveIndex,
			Issues:    wo.subIssueRefs(w.Issues),
		}
		// Determine wave status from results
		allDone := true
		anyFailed := false
		for _, si := range w.Issues {
			if r, ok := wo.agentResults[wo.subIssueKey(si)]; ok {
				if !r.Success {
					anyFailed = true
				}
			} else {
				allDone = false
			}
		}
		if !allDone {
			detail.Status = "pending"
		} else if anyFailed {
			detail.Status = "failed"
		} else {
			detail.Status = "completed"
		}
		status.Waves = append(status.Waves, detail)
	}
	wo.mu.Unlock()

	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		log.Printf("epic #%d: failed to marshal wave status: %v", wo.epicNumber, err)
		return
	}

	path := filepath.Join(dir, fmt.Sprintf("wave-status-%d.json", wo.epicNumber))
	if err := os.WriteFile(path, data, 0644); err != nil {
		log.Printf("epic #%d: failed to write wave status: %v", wo.epicNumber, err)
	}
}

// ─── Epic Context Accumulator ────────────────────────────────────────────────

// epicContext is the on-disk format for the shared epic context file.
// Matches the TypeScript EpicContextSchema in epic-context.ts.
type epicContext struct {
	SchemaVersion    string                       `json:"schema_version"`
	EpicNumber       int                          `json:"epic_number"`
	LastUpdated      string                       `json:"last_updated"`
	SubIssueFindings map[string]*subIssueFindings `json:"sub_issue_findings"`
	SharedResearch   sharedResearch               `json:"shared_research"`
}

type subIssueFindings struct {
	FilesTouched []string `json:"files_touched"`
	Decisions    []string `json:"decisions"`
	Discoveries  []string `json:"discoveries"`
	Patterns     []string `json:"patterns"`
	RecordedAt   string   `json:"recorded_at"`
}

type sharedResearch struct {
	CodebaseNotes     []string `json:"codebase_notes"`
	ArchitectureNotes []string `json:"architecture_notes"`
	RelevantFiles     []string `json:"relevant_files"`
}

// epicContextPath returns the path to the epic context file. Delegates to the
// shared helper so the accumulator and the prompt-injection read side
// (epic_context_prompt.go) compute the same path.
//
// The file lives in the pipeline state of the epic's own repository
// (wo.repo), where its number names this epic. In the launch checkout's, an
// epic of another repository shared the file of every epic with its number,
// and a sub-issue could be handed another epic's findings (#2377).
func (wo *WaveOrchestrator) epicContextPath() (string, error) {
	root, err := wo.scheduler.resolveRunRoot(wo.repo)
	if err != nil {
		return "", err
	}
	return epicContextFilePath(root, wo.epicNumber)
}

// readEpicContext reads the epic context file, returning nil if it doesn't
// exist or the epic's repository has no checkout here.
func (wo *WaveOrchestrator) readEpicContext() *epicContext {
	root, err := wo.scheduler.resolveRunRoot(wo.repo)
	if err != nil {
		return nil
	}
	return readEpicContextFile(root, wo.epicNumber)
}

// writeEpicContext writes the epic context file atomically.
func (wo *WaveOrchestrator) writeEpicContext(ec *epicContext) error {
	path, err := wo.epicContextPath()
	if err != nil {
		return fmt.Errorf("resolve epic context path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create pipeline dir: %w", err)
	}
	data, err := json.MarshalIndent(ec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal epic context: %w", err)
	}
	// Atomic write via temp file + rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath) // best-effort cleanup
		return fmt.Errorf("rename temp file: %w", err)
	}
	return nil
}

// findingKey is si's key in the epic context's sub_issue_findings: the bare
// number for a sub-issue in the epic's own repository, which is how every
// writer has keyed it, and "owner/repo#N" for one in another repository, so
// it does not overwrite the findings of the epic repository's issue with the
// same number (#2377).
func (wo *WaveOrchestrator) findingKey(si teams.SubIssue) string {
	repo := wo.subIssueRepo(si)
	if strings.EqualFold(repo, wo.repo) {
		return strconv.Itoa(si.Number)
	}
	return fmt.Sprintf("%s#%d", repo, si.Number)
}

// appendSubIssueToEpicContext records a completed sub-issue's findings
// in the shared epic context file. This allows later sub-issues to
// benefit from earlier sub-issues' codebase discoveries.
func (wo *WaveOrchestrator) appendSubIssueToEpicContext(si teams.SubIssue) {
	wo.mu.Lock()
	defer wo.mu.Unlock()

	ec := wo.readEpicContext()
	if ec == nil {
		ec = &epicContext{
			SchemaVersion:    "1.0",
			EpicNumber:       wo.epicNumber,
			SubIssueFindings: make(map[string]*subIssueFindings),
			SharedResearch: sharedResearch{
				CodebaseNotes:     []string{},
				ArchitectureNotes: []string{},
				RelevantFiles:     []string{},
			},
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	ec.LastUpdated = now

	// Record this sub-issue's files as findings
	findings := &subIssueFindings{
		FilesTouched: si.Files,
		Decisions:    []string{},
		Discoveries:  []string{},
		Patterns:     []string{},
		RecordedAt:   now,
	}
	if findings.FilesTouched == nil {
		findings.FilesTouched = []string{}
	}
	ec.SubIssueFindings[wo.findingKey(si)] = findings

	// Merge files into shared relevant_files (deduplicate)
	if len(si.Files) > 0 {
		seen := make(map[string]bool)
		for _, f := range ec.SharedResearch.RelevantFiles {
			seen[f] = true
		}
		for _, f := range si.Files {
			if !seen[f] {
				seen[f] = true
				ec.SharedResearch.RelevantFiles = append(ec.SharedResearch.RelevantFiles, f)
			}
		}
	}

	if err := wo.writeEpicContext(ec); err != nil {
		log.Printf("epic #%d: failed to write epic context after sub-issue #%d: %v",
			wo.epicNumber, si.Number, err)
	} else {
		log.Printf("epic #%d: appended sub-issue #%d findings to epic context",
			wo.epicNumber, si.Number)
	}
}
