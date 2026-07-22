package learning

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

// TuningParam represents a tunable parameter with its current and target values.
type TuningParam struct {
	Name     string  `json:"name"`
	Current  float64 `json:"current"`
	Target   float64 `json:"target"`
	MinValue float64 `json:"minValue"`
	MaxValue float64 `json:"maxValue"`
}

// TuningResult is the output of a single tuning step.
type TuningResult struct {
	Param     string  `json:"param"`
	OldValue  float64 `json:"oldValue"`
	NewValue  float64 `json:"newValue"`
	Delta     float64 `json:"delta"`
	Converged bool    `json:"converged"`
}

// AuditEntry records a tuning action for audit trail.
type AuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Param     string    `json:"param"`
	OldValue  float64   `json:"oldValue"`
	NewValue  float64   `json:"newValue"`
	Reason    string    `json:"reason"`
	Outcome   *Outcome  `json:"outcome,omitempty"`
}

// TunerConfig configures the tuning optimizer.
type TunerConfig struct {
	InitialLearningRate float64 `json:"initialLearningRate"`
	Decay               float64 `json:"decay"`
	Epsilon             float64 `json:"epsilon"`      // Convergence threshold
	ConvergenceWindow   int     `json:"convergenceN"` // Consecutive iterations within epsilon
}

// DefaultTunerConfig returns reasonable defaults.
func DefaultTunerConfig() TunerConfig {
	return TunerConfig{
		InitialLearningRate: 0.05,
		Decay:               0.95,
		Epsilon:             0.01,
		ConvergenceWindow:   5,
	}
}

// Tuner implements the pipeline learning and calibration optimizer.
type Tuner struct {
	config      TunerConfig
	iteration   int
	auditPath   string
	convergence map[string]int // consecutive within-epsilon count per param
}

// NewTuner creates a tuning optimizer.
func NewTuner(workspaceRoot string, config TunerConfig) *Tuner {
	auditDir := filepath.Join(workspaceRoot, ".nightgauge", "pipeline", "history")
	return &Tuner{
		config:      config,
		auditPath:   filepath.Join(auditDir, "tuning-audit.jsonl"),
		convergence: make(map[string]int),
	}
}

// Tune adjusts a parameter toward its target using decaying learning rate.
// Formula: new_param = old_param + learning_rate × (target - observed)
// learning_rate = initial_rate × decay^iteration
func (t *Tuner) Tune(param TuningParam, observed float64, outcome *Outcome) TuningResult {
	lr := t.config.InitialLearningRate * math.Pow(t.config.Decay, float64(t.iteration))
	delta := lr * (param.Target - observed)
	newValue := param.Current + delta

	// Clamp to bounds
	if newValue < param.MinValue {
		newValue = param.MinValue
	}
	if newValue > param.MaxValue {
		newValue = param.MaxValue
	}

	// Check convergence
	gap := math.Abs(param.Target - observed)
	converged := false
	if gap < t.config.Epsilon {
		t.convergence[param.Name]++
		if t.convergence[param.Name] >= t.config.ConvergenceWindow {
			converged = true
		}
	} else {
		t.convergence[param.Name] = 0
	}

	result := TuningResult{
		Param:     param.Name,
		OldValue:  param.Current,
		NewValue:  newValue,
		Delta:     delta,
		Converged: converged,
	}

	// Log audit entry
	entry := AuditEntry{
		Timestamp: time.Now(),
		Param:     param.Name,
		OldValue:  param.Current,
		NewValue:  newValue,
		Reason:    fmt.Sprintf("lr=%.4f gap=%.4f delta=%.4f", lr, gap, delta),
		Outcome:   outcome,
	}
	_ = t.appendAudit(entry)

	t.iteration++
	return result
}

// LearningRate returns the current learning rate.
func (t *Tuner) LearningRate() float64 {
	return t.config.InitialLearningRate * math.Pow(t.config.Decay, float64(t.iteration))
}

// IsConverged returns true if a parameter has converged.
func (t *Tuner) IsConverged(paramName string) bool {
	return t.convergence[paramName] >= t.config.ConvergenceWindow
}

// LoadAudit reads the audit trail.
func (t *Tuner) LoadAudit() ([]AuditEntry, error) {
	data, err := os.ReadFile(t.auditPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var entries []AuditEntry
	for _, line := range splitLines(data) {
		if len(line) == 0 {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// appendAudit writes an audit entry to the JSONL file.
func (t *Tuner) appendAudit(entry AuditEntry) error {
	if err := os.MkdirAll(filepath.Dir(t.auditPath), 0755); err != nil {
		return err
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(t.auditPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.Write(append(data, '\n'))
	return err
}
