package learning

import (
	"math"
	"testing"
)

func TestTuneBasic(t *testing.T) {
	dir := t.TempDir()
	tuner := NewTuner(dir, DefaultTunerConfig())

	param := TuningParam{
		Name:     "size_accuracy",
		Current:  0.5,
		Target:   0.8,
		MinValue: 0.0,
		MaxValue: 1.0,
	}

	result := tuner.Tune(param, 0.5, nil)
	if result.Param != "size_accuracy" {
		t.Errorf("Param = %q, want size_accuracy", result.Param)
	}
	// Delta should be positive (moving toward target)
	if result.Delta <= 0 {
		t.Errorf("Delta = %f, want > 0", result.Delta)
	}
	if result.NewValue <= param.Current {
		t.Errorf("NewValue = %f should be > OldValue %f", result.NewValue, param.Current)
	}
}

func TestTuneConvergence(t *testing.T) {
	dir := t.TempDir()
	config := TunerConfig{
		InitialLearningRate: 0.1,
		Decay:               1.0, // no decay
		Epsilon:             0.1,
		ConvergenceWindow:   3,
	}
	tuner := NewTuner(dir, config)

	param := TuningParam{
		Name:     "test_param",
		Current:  0.8,
		Target:   0.8,
		MinValue: 0.0,
		MaxValue: 1.0,
	}

	// Observed is very close to target — should converge after 3 iterations
	for i := 0; i < 3; i++ {
		result := tuner.Tune(param, 0.79, nil)
		if i < 2 && result.Converged {
			t.Errorf("converged too early at iteration %d", i)
		}
		if i == 2 && !result.Converged {
			t.Error("expected convergence at iteration 3")
		}
	}
}

func TestTuneClamping(t *testing.T) {
	dir := t.TempDir()
	config := DefaultTunerConfig()
	config.InitialLearningRate = 10.0 // Very aggressive
	tuner := NewTuner(dir, config)

	param := TuningParam{
		Name:     "test",
		Current:  0.5,
		Target:   100.0,
		MinValue: 0.0,
		MaxValue: 1.0,
	}

	result := tuner.Tune(param, 0.0, nil)
	if result.NewValue > 1.0 {
		t.Errorf("NewValue = %f, should be clamped to 1.0", result.NewValue)
	}
}

func TestLearningRateDecay(t *testing.T) {
	dir := t.TempDir()
	config := TunerConfig{
		InitialLearningRate: 1.0,
		Decay:               0.5,
		Epsilon:             0.001,
		ConvergenceWindow:   5,
	}
	tuner := NewTuner(dir, config)

	// Initial LR should be 1.0
	lr0 := tuner.LearningRate()
	if math.Abs(lr0-1.0) > 0.001 {
		t.Errorf("initial LR = %f, want 1.0", lr0)
	}

	// After one tune step, LR should be 0.5
	param := TuningParam{Name: "x", Current: 0.5, Target: 1.0, MinValue: 0, MaxValue: 2}
	tuner.Tune(param, 0.5, nil)

	lr1 := tuner.LearningRate()
	if math.Abs(lr1-0.5) > 0.001 {
		t.Errorf("LR after 1 step = %f, want 0.5", lr1)
	}
}

func TestAuditTrail(t *testing.T) {
	dir := t.TempDir()
	tuner := NewTuner(dir, DefaultTunerConfig())

	param := TuningParam{Name: "test", Current: 0.5, Target: 0.8, MinValue: 0, MaxValue: 1}
	tuner.Tune(param, 0.5, nil)
	tuner.Tune(param, 0.6, nil)

	entries, err := tuner.LoadAudit()
	if err != nil {
		t.Fatalf("LoadAudit: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 audit entries, got %d", len(entries))
	}
}
