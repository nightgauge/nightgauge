package config

import "testing"

func TestResolveDisciplineGate(t *testing.T) {
	tru, fls := true, false

	// nil → conservative defaults (enabled, 30, block).
	en, min, mode := (*AutonomousConfig)(nil).ResolveDisciplineGate()
	if !en || min != DefaultDisciplineMinScore || mode != DefaultDisciplineGateMode {
		t.Errorf("nil defaults = (%v,%d,%q), want (true,%d,%q)", en, min, mode, DefaultDisciplineMinScore, DefaultDisciplineGateMode)
	}

	// empty block → defaults.
	en, min, mode = (&AutonomousConfig{DisciplineGate: &DisciplineGateConfig{}}).ResolveDisciplineGate()
	if !en || min != DefaultDisciplineMinScore || mode != DefaultDisciplineGateMode {
		t.Errorf("empty block = (%v,%d,%q), want defaults", en, min, mode)
	}

	// explicit overrides honored.
	en, min, mode = (&AutonomousConfig{DisciplineGate: &DisciplineGateConfig{Enabled: &fls, MinScore: 55, Mode: "warn"}}).ResolveDisciplineGate()
	if en || min != 55 || mode != "warn" {
		t.Errorf("overrides = (%v,%d,%q), want (false,55,warn)", en, min, mode)
	}

	// explicit true + invalid mode → mode falls back to default.
	en, _, mode = (&AutonomousConfig{DisciplineGate: &DisciplineGateConfig{Enabled: &tru, Mode: "bogus"}}).ResolveDisciplineGate()
	if !en || mode != DefaultDisciplineGateMode {
		t.Errorf("invalid mode should fall back: got (%v,%q)", en, mode)
	}
}
