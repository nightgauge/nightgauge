// Package health implements 7-dimension pipeline health analysis.
package health

import (
	"time"
)

// Dimension identifies a health analysis dimension.
type Dimension string

const (
	DimTokenEconomics        Dimension = "token_economics"
	DimCostHealth            Dimension = "cost_health"
	DimStageEffectiveness    Dimension = "stage_effectiveness"
	DimModelRouting          Dimension = "model_routing"
	DimReliability           Dimension = "reliability"
	DimLearningEffectiveness Dimension = "learning_effectiveness"
	DimPipelineVelocity      Dimension = "pipeline_velocity"
)

// DimensionScore holds the score for a single health dimension.
type DimensionScore struct {
	Dimension Dimension `json:"dimension"`
	Score     float64   `json:"score"` // 0.0 - 1.0
	Grade     string    `json:"grade"` // A, B, C, D, F
	Findings  []string  `json:"findings"`
}

// Report is the complete health analysis result.
type Report struct {
	OverallScore float64          `json:"overallScore"` // 0.0 - 1.0
	OverallGrade string           `json:"overallGrade"`
	Dimensions   []DimensionScore `json:"dimensions"`
	GeneratedAt  time.Time        `json:"generatedAt"`
}

// Analyzer performs 7-dimension health analysis.
type Analyzer struct{}

// NewAnalyzer creates a health analyzer.
func NewAnalyzer() *Analyzer {
	return &Analyzer{}
}

// Analyze produces a health report from execution history.
func (a *Analyzer) Analyze(runs []RunData) Report {
	dims := []DimensionScore{
		a.analyzeTokenEconomics(runs),
		a.analyzeCostHealth(runs),
		a.analyzeStageEffectiveness(runs),
		a.analyzeModelRouting(runs),
		a.analyzeReliability(runs),
		a.analyzeSelfImprovement(runs),
		a.analyzePipelineVelocity(runs),
	}

	total := 0.0
	for _, d := range dims {
		total += d.Score
	}
	overall := total / float64(len(dims))

	return Report{
		OverallScore: overall,
		OverallGrade: scoreToGrade(overall),
		Dimensions:   dims,
		GeneratedAt:  time.Now(),
	}
}

// RunData holds the data needed for health analysis from a single pipeline run.
type RunData struct {
	IssueNumber     int
	Success         bool
	DurationMs      int64
	InputTokens     int
	OutputTokens    int
	CostUSD         float64
	Model           string
	StageResults    []StageData
	CompletedAt     time.Time
	ComplexityScore int
}

// StageData holds per-stage metrics.
type StageData struct {
	Stage      string
	Success    bool
	DurationMs int64
	Tokens     int
	CostUSD    float64
	RetryCount int
}

func (a *Analyzer) analyzeTokenEconomics(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimTokenEconomics, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	var totalTokens, totalRuns int
	for _, r := range runs {
		totalTokens += r.InputTokens + r.OutputTokens
		totalRuns++
	}
	avgTokens := totalTokens / totalRuns

	var findings []string
	score := 1.0

	// Penalize high average token usage
	if avgTokens > 50000 {
		score -= 0.3
		findings = append(findings, "high average token usage per run")
	} else if avgTokens > 30000 {
		score -= 0.15
		findings = append(findings, "moderate token usage")
	}

	if score < 0 {
		score = 0
	}
	return DimensionScore{Dimension: DimTokenEconomics, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func (a *Analyzer) analyzeCostHealth(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimCostHealth, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	var totalCost float64
	for _, r := range runs {
		totalCost += r.CostUSD
	}
	avgCost := totalCost / float64(len(runs))

	var findings []string
	score := 1.0

	if avgCost > 2.0 {
		score -= 0.4
		findings = append(findings, "average cost per run exceeds $2.00")
	} else if avgCost > 1.0 {
		score -= 0.2
		findings = append(findings, "average cost per run exceeds $1.00")
	}

	return DimensionScore{Dimension: DimCostHealth, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func (a *Analyzer) analyzeStageEffectiveness(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimStageEffectiveness, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	stageFailures := make(map[string]int)
	stageTotal := make(map[string]int)

	for _, r := range runs {
		for _, s := range r.StageResults {
			stageTotal[s.Stage]++
			if !s.Success {
				stageFailures[s.Stage]++
			}
		}
	}

	var findings []string
	score := 1.0
	for stage, total := range stageTotal {
		failures := stageFailures[stage]
		if total > 0 {
			failRate := float64(failures) / float64(total)
			if failRate > 0.3 {
				score -= 0.2
				findings = append(findings, stage+" failure rate > 30%")
			} else if failRate > 0.1 {
				score -= 0.1
				findings = append(findings, stage+" failure rate > 10%")
			}
		}
	}

	if score < 0 {
		score = 0
	}
	return DimensionScore{Dimension: DimStageEffectiveness, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func (a *Analyzer) analyzeModelRouting(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimModelRouting, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	modelUsage := make(map[string]int)
	for _, r := range runs {
		modelUsage[r.Model]++
	}

	var findings []string
	score := 0.8 // Default good

	if len(modelUsage) == 1 {
		score -= 0.2
		findings = append(findings, "only one model used across all runs")
	}

	return DimensionScore{Dimension: DimModelRouting, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func (a *Analyzer) analyzeReliability(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimReliability, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	var successes int
	for _, r := range runs {
		if r.Success {
			successes++
		}
	}

	successRate := float64(successes) / float64(len(runs))
	var findings []string

	if successRate < 0.7 {
		findings = append(findings, "success rate below 70%")
	} else if successRate < 0.9 {
		findings = append(findings, "success rate below 90%")
	}

	return DimensionScore{Dimension: DimReliability, Score: successRate, Grade: scoreToGrade(successRate), Findings: findings}
}

func (a *Analyzer) analyzeSelfImprovement(runs []RunData) DimensionScore {
	if len(runs) < 5 {
		return DimensionScore{Dimension: DimLearningEffectiveness, Score: 0.5, Grade: "C", Findings: []string{"insufficient data for trend analysis"}}
	}

	// Compare first half vs second half success rates
	mid := len(runs) / 2
	firstHalf := runs[:mid]
	secondHalf := runs[mid:]

	firstSuccess := successRate(firstHalf)
	secondSuccess := successRate(secondHalf)

	score := 0.5
	var findings []string

	if secondSuccess > firstSuccess+0.1 {
		score = 0.9
		findings = append(findings, "improving success rate trend")
	} else if secondSuccess >= firstSuccess-0.05 {
		score = 0.7
		findings = append(findings, "stable performance")
	} else {
		score = 0.3
		findings = append(findings, "declining performance trend")
	}

	return DimensionScore{Dimension: DimLearningEffectiveness, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func (a *Analyzer) analyzePipelineVelocity(runs []RunData) DimensionScore {
	if len(runs) == 0 {
		return DimensionScore{Dimension: DimPipelineVelocity, Score: 0.5, Grade: "C", Findings: []string{"no data"}}
	}

	var totalDuration int64
	for _, r := range runs {
		totalDuration += r.DurationMs
	}
	avgDuration := time.Duration(totalDuration/int64(len(runs))) * time.Millisecond

	var findings []string
	score := 1.0

	if avgDuration > 60*time.Minute {
		score = 0.3
		findings = append(findings, "average pipeline > 60 minutes")
	} else if avgDuration > 30*time.Minute {
		score = 0.6
		findings = append(findings, "average pipeline > 30 minutes")
	} else if avgDuration > 15*time.Minute {
		score = 0.8
	}

	return DimensionScore{Dimension: DimPipelineVelocity, Score: score, Grade: scoreToGrade(score), Findings: findings}
}

func scoreToGrade(score float64) string {
	switch {
	case score >= 0.9:
		return "A"
	case score >= 0.8:
		return "B"
	case score >= 0.7:
		return "C"
	case score >= 0.5:
		return "D"
	default:
		return "F"
	}
}

func successRate(runs []RunData) float64 {
	if len(runs) == 0 {
		return 0
	}
	var s int
	for _, r := range runs {
		if r.Success {
			s++
		}
	}
	return float64(s) / float64(len(runs))
}
