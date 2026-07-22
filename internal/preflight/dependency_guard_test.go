package preflight

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

// fakeRegistry returns a fixed status per package name, defaulting to Exists.
type fakeRegistry struct {
	status map[string]RegistryStatus
}

func (f fakeRegistry) Exists(_ context.Context, _ Ecosystem, name string) RegistryStatus {
	if s, ok := f.status[name]; ok {
		return s
	}
	return RegistryExists
}

func TestParseNpmDeps(t *testing.T) {
	body := []byte(`{
		"name": "x",
		"dependencies": {"react": "^18.0.0", "zod": "^3.0.0"},
		"devDependencies": {"vitest": "^1.0.0"},
		"peerDependencies": {"typescript": "*"}
	}`)
	got := parseNpmDeps(body)
	for _, want := range []string{"react", "zod", "vitest", "typescript"} {
		if _, ok := got[want]; !ok {
			t.Errorf("parseNpmDeps missing %q", want)
		}
	}
	if len(got) != 4 {
		t.Errorf("parseNpmDeps len = %d, want 4", len(got))
	}
	// Malformed JSON yields an empty set, not a panic.
	if len(parseNpmDeps([]byte("not json"))) != 0 {
		t.Error("parseNpmDeps(garbage) should be empty")
	}
}

func TestParseGoModDeps(t *testing.T) {
	body := []byte("module x\n\ngo 1.24\n\nrequire (\n\tgithub.com/spf13/cobra v1.8.0\n\tgopkg.in/yaml.v3 v3.0.1 // indirect\n)\n\nrequire github.com/google/uuid v1.6.0\n")
	got := parseGoModDeps(body)
	for _, want := range []string{"github.com/spf13/cobra", "gopkg.in/yaml.v3", "github.com/google/uuid"} {
		if _, ok := got[want]; !ok {
			t.Errorf("parseGoModDeps missing %q", want)
		}
	}
}

func TestParsePipDeps(t *testing.T) {
	body := []byte("# comment\nrequests==2.31.0\nflask>=2.0\nnumpy\n-r other.txt\npydantic[email]>=2 ; python_version>='3.8'\n")
	got := parsePipDeps(body)
	for _, want := range []string{"requests", "flask", "numpy", "pydantic"} {
		if _, ok := got[want]; !ok {
			t.Errorf("parsePipDeps missing %q", want)
		}
	}
	if _, ok := got["other.txt"]; ok {
		t.Error("parsePipDeps should skip -r flag lines")
	}
}

func TestAddedDeps(t *testing.T) {
	base := []byte(`{"dependencies": {"react": "^18.0.0"}}`)
	cur := []byte(`{"dependencies": {"react": "^18.0.0", "leftpad": "^1.0.0"}}`)
	added := addedDeps(EcoNPM, "package.json", base, cur)
	if len(added) != 1 || added[0].Name != "leftpad" {
		t.Fatalf("addedDeps = %+v, want only leftpad", added)
	}
	// No baseline (new manifest) → every dep is "added".
	all := addedDeps(EcoNPM, "package.json", nil, cur)
	names := depNames(all)
	if !reflect.DeepEqual(names, []string{"leftpad", "react"}) {
		t.Errorf("addedDeps(no baseline) = %v, want [leftpad react]", names)
	}
}

func TestTyposquatMatch(t *testing.T) {
	cases := []struct {
		eco     Ecosystem
		name    string
		wantPop string
		wantHit bool
	}{
		{EcoNPM, "reqeust", "request", true},   // transposition (1 edit)
		{EcoNPM, "lodahs", "lodash", true},     // transposition
		{EcoNPM, "expres", "express", true},    // deletion
		{EcoNPM, "react", "", false},           // exact popular → not a squat
		{EcoNPM, "my-app-utils", "", false},    // unrelated
		{EcoPip, "reqeusts", "requests", true}, // classic slopsquat
		{EcoPip, "numpy", "", false},           // exact
		{EcoNPM, "ax", "", false},              // too short, skipped
	}
	for _, c := range cases {
		pop, hit := typosquatMatch(c.eco, c.name)
		if hit != c.wantHit || pop != c.wantPop {
			t.Errorf("typosquatMatch(%s,%q) = (%q,%v), want (%q,%v)", c.eco, c.name, pop, hit, c.wantPop, c.wantHit)
		}
	}
}

func TestEditDistanceWithin(t *testing.T) {
	if !editDistanceWithin("request", "reqeust", 1) {
		t.Error("adjacent transposition should count as 1 edit (OSA)")
	}
	if editDistanceWithin("request", "reqeust", 0) {
		t.Error("a transposition is not distance 0")
	}
	if editDistanceWithin("abc", "xyz", 1) {
		t.Error("abc vs xyz should exceed 1")
	}
	if !editDistanceWithin("color", "colour", 1) {
		t.Error("color vs colour is 1 insertion")
	}
	if editDistanceWithin("kitten", "sitting", 1) {
		t.Error("kitten vs sitting is distance 3, not ≤1")
	}
}

func TestEvaluateDeps(t *testing.T) {
	reg := fakeRegistry{status: map[string]RegistryStatus{
		"ghost-pkg": RegistryMissing,
		"flaky-pkg": RegistryInconclusive,
		"real-pkg":  RegistryExists,
		"reqeust":   RegistryExists, // a registered typosquat (the dangerous case)
	}}
	added := []AddedDep{
		{EcoNPM, "ghost-pkg", "package.json"},
		{EcoNPM, "flaky-pkg", "package.json"},
		{EcoNPM, "real-pkg", "package.json"},
		{EcoNPM, "reqeust", "package.json"},
	}
	findings, inconclusive := evaluateDeps(context.Background(), added, reg)

	// ghost-pkg → missing (blocking); reqeust → typosquat (blocking); real-pkg → none.
	gotKinds := map[string]string{}
	for _, f := range findings {
		if !f.Blocking {
			t.Errorf("finding %q should be blocking", f.Name)
		}
		gotKinds[f.Name] = f.Kind
	}
	if gotKinds["ghost-pkg"] != "missing" {
		t.Errorf("ghost-pkg kind = %q, want missing", gotKinds["ghost-pkg"])
	}
	if gotKinds["reqeust"] != "typosquat" {
		t.Errorf("reqeust kind = %q, want typosquat", gotKinds["reqeust"])
	}
	if _, ok := gotKinds["real-pkg"]; ok {
		t.Error("real-pkg should produce no finding")
	}
	if len(inconclusive) != 1 || inconclusive[0].Name != "flaky-pkg" || inconclusive[0].Blocking {
		t.Errorf("inconclusive = %+v, want only flaky-pkg (non-blocking)", inconclusive)
	}
}

func TestRunDependencyGuardResultShape(t *testing.T) {
	// HasBlocking reflects blocking findings.
	r := &DependencyGuardResult{Findings: []DepFinding{{Blocking: false}}}
	if r.HasBlocking() {
		t.Error("HasBlocking should be false with no blocking findings")
	}
	r.Findings = append(r.Findings, DepFinding{Blocking: true})
	if !r.HasBlocking() {
		t.Error("HasBlocking should be true once a blocking finding exists")
	}
}

func depNames(deps []AddedDep) []string {
	names := make([]string, len(deps))
	for i, d := range deps {
		names[i] = d.Name
	}
	sort.Strings(names)
	return names
}
