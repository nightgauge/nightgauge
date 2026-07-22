package versionDowngradeGate

import (
	"testing"
)

func TestDefaultGateConfig(t *testing.T) {
	cfg := DefaultGateConfig()
	if cfg.Enabled {
		t.Error("Enabled = true, want false (gate is opt-in)")
	}
	if cfg.EnforcementMode != EnforcementWarn {
		t.Errorf("EnforcementMode = %q, want %q", cfg.EnforcementMode, EnforcementWarn)
	}
	if cfg.BypassLabel != "version:downgrade-allowed" {
		t.Errorf("BypassLabel = %q, want %q", cfg.BypassLabel, "version:downgrade-allowed")
	}
}

func TestEvaluate_Disabled(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = false
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
	})
	if !res.Allowed {
		t.Errorf("disabled gate should allow, got %+v", res)
	}
	if len(res.Downgrades) != 0 {
		t.Errorf("disabled gate should report no downgrades, got %d", len(res.Downgrades))
	}
}

func TestEvaluate_BypassFlag(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
		AllowDowngradeFlag: true,
	})
	if !res.Allowed || !res.Bypassed {
		t.Errorf("flag bypass should allow+bypass, got %+v", res)
	}
}

func TestEvaluate_BypassLabel(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
		IssueLabels: []string{"type:feature", "version:downgrade-allowed"},
	})
	if !res.Allowed || !res.Bypassed {
		t.Errorf("label bypass should allow+bypass, got %+v", res)
	}
}

func TestEvaluate_TSTargetDowngrade_StrictBlocks(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
	})
	if res.Allowed {
		t.Fatalf("strict mode must block target downgrade, got %+v", res)
	}
	if len(res.Downgrades) != 1 || res.Downgrades[0].Field != FieldTSTarget {
		t.Errorf("expected exactly 1 target downgrade, got %+v", res.Downgrades)
	}
	if res.Downgrades[0].OldValue != "ES2022" || res.Downgrades[0].NewValue != "ES2020" {
		t.Errorf("old/new values incorrect: %+v", res.Downgrades[0])
	}
}

func TestEvaluate_TSTargetDowngrade_WarnAllows(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementWarn
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
	})
	if !res.Allowed {
		t.Fatal("warn mode must allow PR even with downgrade")
	}
	if len(res.Downgrades) != 1 {
		t.Errorf("expected 1 downgrade reported, got %d", len(res.Downgrades))
	}
}

func TestEvaluate_TSTargetUpgrade_NoFlag(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2023"}}`),
		},
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("upgrade should pass cleanly, got %+v", res)
	}
}

func TestEvaluate_TSTargetESNext(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	// Moving to ESNext is always an upgrade.
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ESNext"}}`),
		},
	})
	if !res.Allowed {
		t.Errorf("ES2022 → ESNext is an upgrade, got %+v", res)
	}
	// Moving from ESNext to anything else is a downgrade.
	res2 := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ESNext"}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022"}}`),
		},
	})
	if res2.Allowed {
		t.Errorf("ESNext → ES2022 is a downgrade, got %+v", res2)
	}
}

func TestEvaluate_TSLibDowngrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022","lib":["ES2022","DOM"]}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022","lib":["ES2020","DOM"]}}`),
		},
	})
	if res.Allowed {
		t.Fatalf("lib downgrade should fail in strict mode, got %+v", res)
	}
	found := false
	for _, d := range res.Downgrades {
		if d.Field == FieldTSLibPrefix+"[es2022]" {
			found = true
			if d.OldValue != "ES2022" || d.NewValue != "es2020" {
				t.Errorf("lib downgrade values incorrect: %+v", d)
			}
		}
	}
	if !found {
		t.Errorf("expected es2022 → es2020 downgrade, got %+v", res.Downgrades)
	}
}

func TestEvaluate_TSLibRemoval(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	// Removing an entire lib family (DOM) is a downgrade.
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"lib":["ES2022","DOM"]}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"lib":["ES2022"]}}`),
		},
	})
	if res.Allowed {
		t.Fatalf("lib family removal should fail, got %+v", res)
	}
}

func TestEvaluate_TSLibUpgrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"lib":["ES2020","DOM"]}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"lib":["ES2023","DOM"]}}`),
		},
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("lib upgrade should pass, got %+v", res)
	}
}

func TestEvaluate_PackageDependencyDowngrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"dependencies":{"react":"^18.2.0"}}`),
		CurrentPackageJSON:  []byte(`{"dependencies":{"react":"^17.0.2"}}`),
		PackageJSONPath:     "package.json",
	})
	if res.Allowed {
		t.Fatalf("dep downgrade should fail in strict mode, got %+v", res)
	}
	if len(res.Downgrades) != 1 || res.Downgrades[0].Field != "dependencies.react" {
		t.Errorf("expected dependencies.react downgrade, got %+v", res.Downgrades)
	}
}

func TestEvaluate_PackageDependencyUpgrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"dependencies":{"react":"^17.0.2"}}`),
		CurrentPackageJSON:  []byte(`{"dependencies":{"react":"^18.2.0"}}`),
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("dep upgrade should pass, got %+v", res)
	}
}

func TestEvaluate_PackageDevDependencyDowngrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"devDependencies":{"vitest":"^2.0.0"}}`),
		CurrentPackageJSON:  []byte(`{"devDependencies":{"vitest":"^1.5.0"}}`),
	})
	if res.Allowed {
		t.Fatalf("devDep downgrade should fail, got %+v", res)
	}
	if res.Downgrades[0].Field != "devDependencies.vitest" {
		t.Errorf("expected devDependencies.vitest, got %s", res.Downgrades[0].Field)
	}
}

func TestEvaluate_EnginesNodeDowngrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"engines":{"node":">=20.0.0"}}`),
		CurrentPackageJSON:  []byte(`{"engines":{"node":">=18.0.0"}}`),
	})
	if res.Allowed {
		t.Fatalf("engines.node downgrade should fail, got %+v", res)
	}
	if res.Downgrades[0].Field != FieldEnginesNode {
		t.Errorf("expected engines.node, got %s", res.Downgrades[0].Field)
	}
}

func TestEvaluate_EnginesNodeUpgrade(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"engines":{"node":">=18.0.0"}}`),
		CurrentPackageJSON:  []byte(`{"engines":{"node":">=20.0.0"}}`),
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("engines.node upgrade should pass, got %+v", res)
	}
}

func TestEvaluate_NewlyAddedDep(t *testing.T) {
	// Newly-added deps must not be flagged.
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"dependencies":{"react":"^18.2.0"}}`),
		CurrentPackageJSON:  []byte(`{"dependencies":{"react":"^18.2.0","lodash":"^4.0.0"}}`),
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("adding a new dep should pass, got %+v", res)
	}
}

func TestEvaluate_NewlyAddedTsconfig(t *testing.T) {
	// Newly-added tsconfigs (no baseline) must not be flagged.
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.test.json": []byte(`{"compilerOptions":{"target":"ES2018"}}`),
		},
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("new tsconfig should pass, got %+v", res)
	}
}

func TestEvaluate_MultipleDowngradesReported(t *testing.T) {
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselineTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2022","lib":["ES2022","DOM"]}}`),
		},
		CurrentTSConfigs: map[string][]byte{
			"tsconfig.json": []byte(`{"compilerOptions":{"target":"ES2020","lib":["ES2020","DOM"]}}`),
		},
		BaselinePackageJSON: []byte(`{"dependencies":{"react":"^18.2.0"},"engines":{"node":">=20.0.0"}}`),
		CurrentPackageJSON:  []byte(`{"dependencies":{"react":"^17.0.2"},"engines":{"node":">=18.0.0"}}`),
	})
	if res.Allowed {
		t.Fatalf("strict mode must block, got %+v", res)
	}
	if len(res.Downgrades) < 4 {
		t.Errorf("expected at least 4 downgrades (target, lib, dep, engines), got %d: %+v",
			len(res.Downgrades), res.Downgrades)
	}
}

func TestEvaluate_UnparseableSemverDoesNotFlag(t *testing.T) {
	// Unparseable ranges (e.g., git URLs, workspace:) should not fail the gate.
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		BaselinePackageJSON: []byte(`{"dependencies":{"some-pkg":"workspace:*"}}`),
		CurrentPackageJSON:  []byte(`{"dependencies":{"some-pkg":"file:../local"}}`),
	})
	if !res.Allowed || len(res.Downgrades) != 0 {
		t.Errorf("unparseable ranges should not flag, got %+v", res)
	}
}

func TestEvaluate_NoBaselineNoDowngrade(t *testing.T) {
	// When baseline package.json is absent, the dep section is not evaluated.
	cfg := DefaultGateConfig()
	cfg.Enabled = true
	cfg.EnforcementMode = EnforcementStrict
	res := NewEvaluator(cfg).Evaluate(EvaluateInput{
		CurrentPackageJSON: []byte(`{"dependencies":{"react":"^17.0.0"}}`),
	})
	if !res.Allowed {
		t.Errorf("no baseline should pass, got %+v", res)
	}
}

func TestMinVersionFromRange(t *testing.T) {
	tests := []struct {
		in        string
		wantOK    bool
		wantMajor int64
		wantMinor int64
		wantPatch int64
	}{
		{"1.2.3", true, 1, 2, 3},
		{"^1.2.3", true, 1, 2, 3},
		{"~2.0.0", true, 2, 0, 0},
		{">=18.0.0", true, 18, 0, 0},
		{">18.0.0", true, 18, 0, 0},
		{">=1.2.3 <2.0.0", true, 1, 2, 3},
		{"1.x", true, 1, 0, 0},
		{"1.2.x", true, 1, 2, 0},
		{"v1.2.3", true, 1, 2, 3},
		// Composite via ||: leftmost wins.
		{"1.2.3 || 2.0.0", true, 1, 2, 3},
		// Hyphen range.
		{"1.2.3 - 2.3.4", true, 1, 2, 3},
		// Unparseables.
		{"*", false, 0, 0, 0},
		{"", false, 0, 0, 0},
		{"latest", false, 0, 0, 0},
		{"workspace:*", false, 0, 0, 0},
		{"file:../local", false, 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			v, ok := minVersionFromRange(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if v.Major() != uint64(tc.wantMajor) || v.Minor() != uint64(tc.wantMinor) || v.Patch() != uint64(tc.wantPatch) {
				t.Errorf("got %d.%d.%d, want %d.%d.%d",
					v.Major(), v.Minor(), v.Patch(),
					tc.wantMajor, tc.wantMinor, tc.wantPatch)
			}
		})
	}
}

func TestFamilyOf(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"ES2022", "es"},
		{"es2022", "es"},
		{"DOM", "dom"},
		{"DOM.Iterable", "dom.iterable"},
		{"WebWorker", "webworker"},
		{"ESNext", "esnext"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := familyOf(tc.in); got != tc.want {
				t.Errorf("familyOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsTargetDowngrade(t *testing.T) {
	tests := []struct {
		oldVal string
		newVal string
		want   bool
	}{
		{"ES2022", "ES2020", true},
		{"ES2020", "ES2022", false},
		{"ES2022", "ES2022", false},
		{"ES2022", "ESNext", false},
		{"ESNext", "ES2022", true},
		{"", "ES2022", false},
		{"ES2022", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.oldVal+"_"+tc.newVal, func(t *testing.T) {
			if got := isTargetDowngrade(tc.oldVal, tc.newVal); got != tc.want {
				t.Errorf("isTargetDowngrade(%q, %q) = %v, want %v",
					tc.oldVal, tc.newVal, got, tc.want)
			}
		})
	}
}
