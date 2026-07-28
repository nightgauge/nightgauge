package deliverable

import (
	"reflect"
	"testing"
)

// The run that produced #152: a Flutter app whose entire deliverable was a new
// end-to-end suite under integration_test/. The pre-existing unit suite ran and
// passed (1,361 tests, touching none of the new files); integration and e2e did
// not run because wiring them was declared out of scope by the same plan the
// validating stage then read.
func TestUnexercised_TheReportedRun(t *testing.T) {
	changed := []string{
		"integration_test/signup_flow_test.dart",
		"integration_test/helpers/mailpit_client.dart",
		"lib/src/auth/signup_controller.dart",
		"README.md",
	}
	ran := Execution{Unit: true, Integration: false, E2E: false}

	got := Unexercised(changed, ran)

	want := []TestArtifact{
		{Path: "integration_test/helpers/mailpit_client.dart", Tiers: []Tier{TierIntegration, TierE2E}},
		{Path: "integration_test/signup_flow_test.dart", Tiers: []Tier{TierIntegration, TierE2E}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Unexercised()\n got: %+v\nwant: %+v", got, want)
	}

	// The unit tests passing is exactly what made this look clean. It must not
	// launder the untouched suite.
	if tiers := Tiers(got); !reflect.DeepEqual(tiers, []Tier{TierIntegration, TierE2E}) {
		t.Fatalf("Tiers() = %v, want [integration e2e]", tiers)
	}
}

// The whole point of the check is that it stays quiet on ordinary runs. A check
// that fires on healthy work gets muted, and a muted check is worth less than
// no check because it also carries false assurance.
func TestUnexercised_SilentWhenTheSuiteActuallyRan(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		ran     Execution
	}{
		{
			name:    "unit tests added and the unit tier ran",
			changed: []string{"internal/auth/token_test.go", "internal/auth/token.go"},
			ran:     Execution{Unit: true},
		},
		{
			name:    "e2e spec added and the e2e tier ran",
			changed: []string{"e2e/checkout.spec.ts"},
			ran:     Execution{Unit: true, E2E: true},
		},
		{
			name:    "flutter integration_test satisfied by the integration tier alone",
			changed: []string{"integration_test/app_test.dart"},
			ran:     Execution{Integration: true},
		},
		{
			name:    "flutter integration_test satisfied by the e2e tier alone",
			changed: []string{"integration_test/app_test.dart"},
			ran:     Execution{E2E: true},
		},
		{
			name:    "no test files in the change at all",
			changed: []string{"lib/main.dart", "docs/ARCHITECTURE.md", "package.json"},
			ran:     Execution{},
		},
		{
			name:    "empty change",
			changed: nil,
			ran:     Execution{},
		},
		{
			name: "helpers and fixtures under a plain test dir are not themselves deliverables",
			// Only the filename marks a test here, so a shared factory beside
			// the suite must not inflate the report.
			changed: []string{"test/helpers/factory.dart", "test/fixtures/user.json"},
			ran:     Execution{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unexercised(tc.changed, tc.ran); len(got) != 0 {
				t.Fatalf("expected no findings, got %+v", got)
			}
		})
	}
}

func TestUnexercised_FiresWhenTheOwningTierIdled(t *testing.T) {
	cases := []struct {
		name    string
		changed []string
		ran     Execution
		want    []string
	}{
		{
			name:    "go unit test added, nothing ran",
			changed: []string{"internal/auth/token_test.go"},
			ran:     Execution{},
			want:    []string{"internal/auth/token_test.go"},
		},
		{
			name:    "playwright suite added while only unit ran",
			changed: []string{"playwright/checkout.spec.ts"},
			ran:     Execution{Unit: true},
			want:    []string{"playwright/checkout.spec.ts"},
		},
		{
			name:    "cypress directory contents count wholesale",
			changed: []string{"cypress/support/commands.js", "cypress/e2e/login.cy.ts"},
			ran:     Execution{Unit: true},
			want:    []string{"cypress/e2e/login.cy.ts", "cypress/support/commands.js"},
		},
		{
			name:    "dotted e2e filename outside an e2e directory",
			changed: []string{"src/checkout.e2e.spec.ts"},
			ran:     Execution{Unit: true},
			want:    []string{"src/checkout.e2e.spec.ts"},
		},
		{
			name:    "python test_ prefix",
			changed: []string{"tests/test_signup.py"},
			ran:     Execution{},
			want:    []string{"tests/test_signup.py"},
		},
		{
			name:    "gherkin feature file",
			changed: []string{"features/checkout.feature"},
			ran:     Execution{Unit: true},
			want:    []string{"features/checkout.feature"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Unexercised(tc.changed, tc.ran)
			var paths []string
			for _, a := range got {
				paths = append(paths, a.Path)
			}
			if !reflect.DeepEqual(paths, tc.want) {
				t.Fatalf("paths = %v, want %v", paths, tc.want)
			}
		})
	}
}

// The ambiguous-directory rule decides the reported case, so pin it directly:
// integration_test/ means the integration tier in most ecosystems and a
// device-driven e2e suite in Flutter. Either reading flags the file when
// neither tier ran, and neither reading flags it when one did.
func TestTiersFor_AmbiguousIntegrationTestDirectory(t *testing.T) {
	got := tiersFor("integration_test/app_test.dart")
	want := []Tier{TierIntegration, TierE2E}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tiersFor = %v, want %v", got, want)
	}
}

func TestTiersFor_NonTestPathsAreNotMaterial(t *testing.T) {
	for _, p := range []string{
		"lib/main.dart",
		"docs/TESTING.md", // documentation ABOUT tests is not a test
		"package.json",
		"internal/auth/token.go",
		"src/components/Latest.tsx", // "test" is not a substring trap
	} {
		if got := tiersFor(p); got != nil {
			t.Fatalf("tiersFor(%q) = %v, want nil", p, got)
		}
	}
}

func TestUnexercised_SortedForAStableCard(t *testing.T) {
	// The finding drives a decision request and a PR annotation, both of which
	// are fingerprinted. Unstable ordering would churn the fingerprint and
	// re-raise a card the operator already dismissed.
	changed := []string{"e2e/z.spec.ts", "e2e/a.spec.ts", "e2e/m.spec.ts"}
	got := Unexercised(changed, Execution{Unit: true})
	if len(got) != 3 || got[0].Path != "e2e/a.spec.ts" || got[2].Path != "e2e/z.spec.ts" {
		t.Fatalf("not sorted: %+v", got)
	}
}
