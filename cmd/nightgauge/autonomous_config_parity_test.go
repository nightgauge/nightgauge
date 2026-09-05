package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
)

func boolPtr(v bool) *bool { return &v }

// TestBuildAutonomousConfigServePathHonorsConfigKeys pins the config.yaml keys
// that the `serve` daemon's autonomous scheduler silently dropped (#1445).
// `serve` passes no overrides, so every value here must come from the file.
func TestBuildAutonomousConfigServePathHonorsConfigKeys(t *testing.T) {
	cfg := &config.Config{
		Autonomous: &config.AutonomousConfig{
			AutoActionable:              boolPtr(true),
			TrustedAuthorAssociations:   []string{"OWNER"},
			DisableEpicBlockedByCascade: true,
			RefinementEnabled:           boolPtr(false),
			RefinementInterval:          config.YAMLDuration(90 * time.Second),
			RefinementMaxConcurrent:     3,
		},
	}

	got := buildAutonomousConfig(cfg, autonomousConfigOverrides{})

	if !got.AutoActionable {
		t.Errorf("AutoActionable = false, want true (autonomous.auto_actionable: true was set)")
	}
	if !reflect.DeepEqual(got.TrustedAuthorAssociations, []string{"OWNER"}) {
		t.Errorf("TrustedAuthorAssociations = %#v, want [\"OWNER\"]", got.TrustedAuthorAssociations)
	}
	if !got.DisableEpicBlockedByCascade {
		t.Errorf("DisableEpicBlockedByCascade = false, want true (autonomous.disable_epic_blockedby_cascade: true was set)")
	}
	if got.RefinementEnabled {
		t.Errorf("RefinementEnabled = true, want false (autonomous.refinement_enabled: false was set)")
	}
	if got.RefinementInterval != 90*time.Second {
		t.Errorf("RefinementInterval = %v, want 90s", got.RefinementInterval)
	}
	if got.RefinementMaxConcurrent != 3 {
		t.Errorf("RefinementMaxConcurrent = %d, want 3", got.RefinementMaxConcurrent)
	}
}

// TestBuildAutonomousConfigOverridesWinOverFile pins the `autonomous run`
// contract: its CLI flags have already absorbed config.yaml where the flag was
// absent, so an explicitly-passed value must survive the builder — including a
// deliberate zero.
func TestBuildAutonomousConfigOverridesWinOverFile(t *testing.T) {
	dryRunFile := true
	cfg := &config.Config{
		Autonomous: &config.AutonomousConfig{
			ScanInterval:  config.YAMLDuration(5 * time.Minute),
			BudgetCeiling: 5000,
			DryRun:        &dryRunFile,
			AllowSelfRepo: true,
		},
	}

	interval := 45 * time.Second
	maxSlots := 7
	var budget int64 // an explicit `--budget 0` means unlimited
	dryRun := false
	allowSelfRepo := false

	got := buildAutonomousConfig(cfg, autonomousConfigOverrides{
		ScanInterval:  &interval,
		MaxConcurrent: &maxSlots,
		BudgetCeiling: &budget,
		DryRun:        &dryRun,
		AllowSelfRepo: &allowSelfRepo,
	})

	if got.ScanInterval != interval {
		t.Errorf("ScanInterval = %v, want %v", got.ScanInterval, interval)
	}
	if got.MaxConcurrent != maxSlots {
		t.Errorf("MaxConcurrent = %d, want %d", got.MaxConcurrent, maxSlots)
	}
	if got.BudgetCeiling != 0 {
		t.Errorf("BudgetCeiling = %d, want 0 (explicit flag beats the file)", got.BudgetCeiling)
	}
	if got.DryRun {
		t.Errorf("DryRun = true, want false (explicit flag beats the file)")
	}
	if got.AllowSelfRepo {
		t.Errorf("AllowSelfRepo = true, want false (explicit flag beats the file)")
	}
}

// TestBuildAutonomousConfigKeepsPackageDefaults pins the fields neither entry
// point configures. The `autonomous run` literal used to omit them entirely,
// so its scheduler ran with a zeroed idle backoff, no rate-aware cadence and
// no graph cache while `serve` got the documented defaults — the other arm of
// the same drift. Both shapes now start from DefaultAutonomousConfig.
func TestBuildAutonomousConfigKeepsPackageDefaults(t *testing.T) {
	interval := 45 * time.Second
	shapes := map[string]autonomousConfigOverrides{
		"serve": {},
		"run":   {ScanInterval: &interval},
	}
	for name, over := range shapes {
		t.Run(name, func(t *testing.T) {
			got := buildAutonomousConfig(nil, over)
			if !got.DebounceRepos {
				t.Error("DebounceRepos = false, want true")
			}
			if !got.RateAwareCadence {
				t.Error("RateAwareCadence = false, want true")
			}
			if got.IdleScanInterval != 5*time.Minute {
				t.Errorf("IdleScanInterval = %v, want 5m", got.IdleScanInterval)
			}
			if got.IdleCyclesBeforeBackoff != 4 {
				t.Errorf("IdleCyclesBeforeBackoff = %d, want 4", got.IdleCyclesBeforeBackoff)
			}
			if got.MaxScanInterval != 10*time.Minute {
				t.Errorf("MaxScanInterval = %v, want 10m", got.MaxScanInterval)
			}
			if got.GraphCacheTTL != 5*time.Minute {
				t.Errorf("GraphCacheTTL = %v, want 5m", got.GraphCacheTTL)
			}
			if got.RefinementCooldown != 5*time.Minute {
				t.Errorf("RefinementCooldown = %v, want 5m", got.RefinementCooldown)
			}
			if !reflect.DeepEqual(got.ExcludeLabels, config.DefaultExcludeLabels) {
				t.Errorf("ExcludeLabels = %#v, want %#v", got.ExcludeLabels, config.DefaultExcludeLabels)
			}
		})
	}
}

// TestAutonomousSchedulerCallSitesShareOneConfigBuilder is the drift guard for
// #1445: the defect was two hand-maintained AutonomousConfig literals, so any
// function that constructs an AutonomousScheduler must get its config from
// buildAutonomousConfig rather than assembling its own.
func TestAutonomousSchedulerCallSitesShareOneConfigBuilder(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse cmd/nightgauge: %v", err)
	}

	found := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				var constructsScheduler bool
				var callsBuilder bool
				ast.Inspect(fn, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch f := call.Fun.(type) {
					case *ast.SelectorExpr:
						if id, ok := f.X.(*ast.Ident); ok &&
							id.Name == "orchestrator" && f.Sel.Name == "NewAutonomousScheduler" {
							constructsScheduler = true
						}
					case *ast.Ident:
						if f.Name == "buildAutonomousConfig" {
							callsBuilder = true
						}
					}
					return true
				})
				if !constructsScheduler {
					continue
				}
				found++
				if !callsBuilder {
					t.Errorf("%s: %s constructs an orchestrator.AutonomousScheduler but does not call buildAutonomousConfig — a second, hand-maintained AutonomousConfig is exactly the drift #1445 fixed",
						path, fn.Name.Name)
				}
			}
		}
	}
	if found == 0 {
		t.Fatal("found no orchestrator.NewAutonomousScheduler call site; the guard is vacuous")
	}
}
