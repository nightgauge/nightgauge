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
	"github.com/nightgauge/nightgauge/internal/orchestrator"
)

func boolPtr(v bool) *bool { return &v }

// stuckEpicWebhookEnvForTest is a synthetic env var name so the watchdog's
// webhook resolution can be asserted without touching the real
// NIGHTGAUGE_STUCK_EPIC_WEBHOOK a developer machine may have set.
const stuckEpicWebhookEnvForTest = "NIGHTGAUGE_TEST_STUCK_EPIC_WEBHOOK"

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
			PickupBacklog:               boolPtr(true),
			ExcludeLabels:               []string{"needs-human", " design-review "},
			SafetyRails: &config.SafetyRailsConfig{
				BudgetCeiling:     12345,
				CircuitBreakerMax: 4,
				RateLimitPerHour:  9,
				HealthGateMin:     55,
			},
			StuckEpicDetection: &config.StuckEpicDetectionConfig{
				Enabled:           boolPtr(false),
				ReAlertAfter:      config.YAMLDuration(2 * time.Hour),
				DiscordWebhookEnv: stuckEpicWebhookEnvForTest,
			},
		},
		Concurrency: &config.ConcurrencyConfig{
			PerRepoMax:          4,
			RepositoryOverrides: map[string]int{"acme-mobile": 2},
		},
	}
	t.Setenv(stuckEpicWebhookEnvForTest, "https://example.invalid/hook")

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
	if !got.PickupBacklog {
		t.Errorf("PickupBacklog = false, want true (autonomous.pickup_backlog: true was set)")
	}
	// Trimmed and empty-dropped by ResolvedExcludeLabels, never the default set.
	if !reflect.DeepEqual(got.ExcludeLabels, []string{"needs-human", "design-review"}) {
		t.Errorf("ExcludeLabels = %#v, want [\"needs-human\" \"design-review\"]", got.ExcludeLabels)
	}
	if got.SafetyRails == nil {
		t.Fatal("SafetyRails = nil, want the configured autonomous.safety_rails block")
	}
	wantRails := orchestrator.SafetyConfig{
		BudgetCeiling:     12345,
		CircuitBreakerMax: 4,
		RateLimitPerHour:  9,
		// epic_checkpoint was omitted, so ResolveEpicCheckpoint keeps the
		// default true — copying the zero field would opt out silently (#991).
		EpicCheckpoint: true,
		HealthGateMin:  55,
	}
	if *got.SafetyRails != wantRails {
		t.Errorf("SafetyRails = %#v, want %#v", *got.SafetyRails, wantRails)
	}
	if got.PerRepoMax != 4 {
		t.Errorf("PerRepoMax = %d, want 4 (concurrency.per_repo_max: 4 was set)", got.PerRepoMax)
	}
	if !reflect.DeepEqual(got.RepositoryMaxConcurrent, map[string]int{"acme-mobile": 2}) {
		t.Errorf("RepositoryMaxConcurrent = %#v, want {\"acme-mobile\": 2}", got.RepositoryMaxConcurrent)
	}
	if got.StuckEpicDetectionEnabled {
		t.Errorf("StuckEpicDetectionEnabled = true, want false (stuck_epic_detection.enabled: false was set)")
	}
	if got.StuckEpicReAlertAfter != 2*time.Hour {
		t.Errorf("StuckEpicReAlertAfter = %v, want 2h", got.StuckEpicReAlertAfter)
	}
	if got.StuckEpicWebhookURL != "https://example.invalid/hook" {
		t.Errorf("StuckEpicWebhookURL = %q, want the URL from %s", got.StuckEpicWebhookURL, stuckEpicWebhookEnvForTest)
	}
}

// TestBuildAutonomousConfigResolvesEpicCheckpoint pins the two halves of the
// pointer resolution the builder owns: an omitted epic_checkpoint keeps the
// between-epic human pause, an explicit false removes it. A builder that
// copied the field straight across would pass the second case and fail the
// first, which is how the pause was lost once already.
func TestBuildAutonomousConfigResolvesEpicCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *bool
		want  bool
	}{
		{name: "omitted", value: nil, want: true},
		{name: "explicit-false", value: boolPtr(false), want: false},
		{name: "explicit-true", value: boolPtr(true), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Autonomous: &config.AutonomousConfig{
				SafetyRails: &config.SafetyRailsConfig{
					// A real config tunes some other rail; epic_checkpoint
					// rides along.
					CircuitBreakerMax: 3,
					EpicCheckpoint:    tc.value,
				},
			}}
			got := buildAutonomousConfig(cfg, autonomousConfigOverrides{})
			if got.SafetyRails == nil {
				t.Fatal("SafetyRails = nil, want the configured block")
			}
			if got.SafetyRails.EpicCheckpoint != tc.want {
				t.Errorf("EpicCheckpoint = %v, want %v", got.SafetyRails.EpicCheckpoint, tc.want)
			}
		})
	}
}

// TestBuildAutonomousConfigKeepsWatchdogAndRailDefaults is the other arm: with
// no safety_rails, no concurrency and no stuck_epic_detection block, the
// builder must still leave the watchdog armed at its documented cadence and
// resolve the per-repo cap, rather than handing the scheduler a zeroed struct.
func TestBuildAutonomousConfigKeepsWatchdogAndRailDefaults(t *testing.T) {
	got := buildAutonomousConfig(&config.Config{}, autonomousConfigOverrides{})

	if !got.StuckEpicDetectionEnabled {
		t.Error("StuckEpicDetectionEnabled = false, want true (watchdog is on by default)")
	}
	if got.StuckEpicReAlertAfter != 6*time.Hour {
		t.Errorf("StuckEpicReAlertAfter = %v, want 6h", got.StuckEpicReAlertAfter)
	}
	if got.PerRepoMax != config.DefaultPerRepoMax {
		t.Errorf("PerRepoMax = %d, want %d", got.PerRepoMax, config.DefaultPerRepoMax)
	}
	if got.SafetyRails != nil {
		t.Errorf("SafetyRails = %#v, want nil so the orchestrator applies its own rail defaults", *got.SafetyRails)
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
