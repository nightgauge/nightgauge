package config

import "testing"

// Telemetry is opt-out (#738): absent configuration means on. The nil-receiver
// case matters as much as the nil-field one — a config file with no `platform:`
// block at all reaches IsEnabled through a nil *TelemetryConfig.
func TestTelemetryDefaultsOn(t *testing.T) {
	var nilCfg *TelemetryConfig
	if !nilCfg.IsEnabled() {
		t.Error("nil TelemetryConfig must default to enabled")
	}
	if !(&TelemetryConfig{}).IsEnabled() {
		t.Error("unset Enabled must default to enabled")
	}
}

// The one guarantee the flip must not break: an operator who wrote `false`
// keeps it. This is the whole difference between changing a default and
// overriding an answer.
func TestExplicitDisableIsHonored(t *testing.T) {
	cfg := &TelemetryConfig{Enabled: boolPtr(false)}
	if cfg.IsEnabled() {
		t.Error("explicit enabled: false must disable telemetry")
	}
}

func TestExplicitEnableIsHonored(t *testing.T) {
	cfg := &TelemetryConfig{Enabled: boolPtr(true)}
	if !cfg.IsEnabled() {
		t.Error("explicit enabled: true must enable telemetry")
	}
}

// IsExplicitlySet distinguishes "on by default" from "on by choice" so the
// disclosure notice reaches only the population the default actually moved.
func TestIsExplicitlySet(t *testing.T) {
	var nilCfg *TelemetryConfig
	if nilCfg.IsExplicitlySet() {
		t.Error("nil config has no operator answer")
	}
	if (&TelemetryConfig{}).IsExplicitlySet() {
		t.Error("unset Enabled has no operator answer")
	}
	for _, v := range []bool{true, false} {
		if !(&TelemetryConfig{Enabled: boolPtr(v)}).IsExplicitlySet() {
			t.Errorf("explicit enabled: %v is an operator answer", v)
		}
	}
}
