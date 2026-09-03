package main

import (
	"testing"

	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

// TestAutonomousSchedulerConfig_CarriesStageAdapter pins #1336: the scheduler
// `autonomous run` builds must carry a CLI stage adapter, because its
// CLI-only dispatch branch (no IPC callback, no cloud dispatcher) executes
// stages through ExecutionManagerRunner, which refuses to run without one.
// Before the fix the command constructed SchedulerConfig with no Adapter and
// every dispatched issue failed at its first stage.
func TestAutonomousSchedulerConfig_CarriesStageAdapter(t *testing.T) {
	t.Setenv("NIGHTGAUGE_ADAPTER", "")
	adapter, explicit, err := resolveStageAdapter("", "")
	if err != nil {
		t.Fatalf("resolveStageAdapter: %v", err)
	}
	if explicit != "" {
		t.Fatalf("explicit = %q, want empty when neither flag nor env pinned an adapter", explicit)
	}
	cfg := autonomousSchedulerConfig(autonomousSchedulerInputs{
		Owner:           "nightgauge",
		ProjectNumber:   3,
		MaxPerRepo:      1,
		WorkspaceRoot:   t.TempDir(),
		Adapter:         adapter,
		AdapterExplicit: explicit,
	})
	if cfg.Adapter == nil {
		t.Fatal("SchedulerConfig.Adapter is nil — the CLI-only dispatch branch cannot execute a stage")
	}
	sched := orchestrator.NewScheduler(nil, cfg)
	if sched.ExecMgr() == nil || !sched.ExecMgr().HasAdapter() {
		t.Fatal("scheduler built by autonomous run reports HasAdapter() == false")
	}
}

// TestResolveStageAdapter_Precedence: --adapter beats ui.core.adapter, which
// beats the claude-headless default; the explicit value is echoed back only
// when the flag or NIGHTGAUGE_ADAPTER pinned it.
func TestResolveStageAdapter_Precedence(t *testing.T) {
	t.Setenv("NIGHTGAUGE_ADAPTER", "")

	adapter, explicit, err := resolveStageAdapter("", "")
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	// The registry's default key is claude-headless; that adapter reports
	// its name as "claude".
	if adapter.Name() != "claude" || explicit != "" {
		t.Fatalf("default = (%s, %q), want (claude, \"\")", adapter.Name(), explicit)
	}

	adapter, explicit, err = resolveStageAdapter("", "codex")
	if err != nil {
		t.Fatalf("config default: %v", err)
	}
	if adapter.Name() != "codex" || explicit != "" {
		t.Fatalf("config default = (%s, %q), want (codex, \"\")", adapter.Name(), explicit)
	}

	adapter, explicit, err = resolveStageAdapter("claude-headless", "codex")
	if err != nil {
		t.Fatalf("flag: %v", err)
	}
	if adapter.Name() != "claude" || explicit != "claude-headless" {
		t.Fatalf("flag = (%s, %q), want (claude, claude-headless)", adapter.Name(), explicit)
	}

	if _, _, err := resolveStageAdapter("no-such-adapter", ""); err == nil {
		t.Fatal("unknown adapter name must be an error, not a silent fallback")
	}
}

// TestAutonomousRunCmd_HasAdapterFlag: the flag exists so an operator can pin
// the adapter for stages this process runs itself, matching `run`.
func TestAutonomousRunCmd_HasAdapterFlag(t *testing.T) {
	cmd := autonomousRunCmd()
	if cmd.Flags().Lookup("adapter") == nil {
		t.Fatal("autonomous run has no --adapter flag")
	}
}
