package release

import (
	"strings"
	"testing"
)

func TestClassifyLine_Buckets(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Added `tool` — new helper", TypeFeature},
		{"added foo bar", TypeFeature},
		{"Fixed crash on startup", TypeFix},
		{"FIXED `path` parsing edge", TypeFix},
		{"Breaking: removed `--legacy` flag", TypeBreaking},
		{"breaking change to API", TypeBreaking},
		{"Deprecated old config key", TypeDeprecation},
		{"DEPRECATED `--legacy-flag`", TypeDeprecation},
		{"Improved performance of scheduler", TypeImprovement},
		{"Changed default timeout to 30s", TypeImprovement},
		{"Renamed something arbitrary", TypeImprovement}, // default bucket
		{"Some plain note", TypeImprovement},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got := classifyLine(tc.in)
			if got.Type != tc.want {
				t.Errorf("classifyLine(%q).Type = %q, want %q", tc.in, got.Type, tc.want)
			}
		})
	}
}

func TestClassifyLine_TagsAndDescription(t *testing.T) {
	got := classifyLine("Added `foo` — new helper [VSCode] [SDK]")
	wantTags := []string{"VSCode", "SDK"}
	if len(got.Tags) != len(wantTags) {
		t.Fatalf("Tags = %v, want %v", got.Tags, wantTags)
	}
	for i, tag := range wantTags {
		if got.Tags[i] != tag {
			t.Errorf("Tags[%d] = %q, want %q", i, got.Tags[i], tag)
		}
	}
	if strings.Contains(got.Description, "[") || strings.Contains(got.Description, "`") {
		t.Errorf("Description = %q, want no brackets or backticks", got.Description)
	}
	if !strings.Contains(got.Description, "Added foo") {
		t.Errorf("Description = %q, want it to contain 'Added foo'", got.Description)
	}
}

func TestClassifyLine_NoTags(t *testing.T) {
	got := classifyLine("Fixed crash on startup")
	if len(got.Tags) != 0 {
		t.Errorf("Tags = %v, want empty", got.Tags)
	}
}

func TestClassifyBody_SkipsBlankAndNonDash(t *testing.T) {
	body := `## What's changed

- Added foo
not a bullet
- Fixed bar

  - Improved baz
-
- Breaking: removed legacy
`
	changes := classifyBody(body)
	if len(changes) != 4 {
		t.Fatalf("changes length = %d, want 4 (got=%+v)", len(changes), changes)
	}
	if changes[0].Type != TypeFeature || changes[1].Type != TypeFix ||
		changes[2].Type != TypeImprovement || changes[3].Type != TypeBreaking {
		t.Errorf("change types = [%s %s %s %s]", changes[0].Type, changes[1].Type, changes[2].Type, changes[3].Type)
	}
}

func TestClassify_DropsZeroChangeReleases(t *testing.T) {
	releases := []Release{
		{TagName: "v1.0.0", PublishedAt: "2026-01-01T00:00:00Z", Body: "## Release notes\n\nNo bullets here."},
		{TagName: "v1.1.0", PublishedAt: "2026-02-01T00:00:00Z", Body: "- Added thing"},
		{TagName: "v1.2.0", PublishedAt: "2026-03-01T00:00:00Z", Body: ""},
	}
	out := Classify(releases)
	if len(out) != 1 {
		t.Fatalf("Classify length = %d, want 1 (only v1.1.0 has changes)", len(out))
	}
	if out[0].Version != "1.1.0" {
		t.Errorf("Version = %q, want %q (v-prefix should be stripped)", out[0].Version, "1.1.0")
	}
	if out[0].PublishedAt != "2026-02-01T00:00:00Z" {
		t.Errorf("PublishedAt = %q, want preserved", out[0].PublishedAt)
	}
}

func TestClassify_VersionPrefixStrip(t *testing.T) {
	for _, tag := range []string{"v2.1.75", "V2.1.75", "2.1.75"} {
		out := Classify([]Release{{TagName: tag, Body: "- Added thing"}})
		if len(out) != 1 || out[0].Version != "2.1.75" {
			t.Errorf("tag=%q → version=%v, want 2.1.75", tag, out)
		}
	}
}

func TestReadInput_FetchResultShape(t *testing.T) {
	doc := `{"v":1,"source":"x/y","limit":10,"fetched_at":"2026-05-03T00:00:00Z","releases":[
		{"tag_name":"v1.0.0","body":"- Added one"}
	]}`
	releases, err := ReadInput(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "v1.0.0" {
		t.Errorf("releases = %+v, want one v1.0.0", releases)
	}
}

func TestReadInput_BareArrayShape(t *testing.T) {
	doc := `[{"tag_name":"v2.0.0","body":"- Fixed thing"}]`
	releases, err := ReadInput(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ReadInput: %v", err)
	}
	if len(releases) != 1 || releases[0].TagName != "v2.0.0" {
		t.Errorf("releases = %+v, want one v2.0.0", releases)
	}
}

func TestReadInput_Malformed(t *testing.T) {
	if _, err := ReadInput(strings.NewReader(`{not-json`)); err == nil {
		t.Errorf("expected error for malformed JSON, got nil")
	}
}

// TestClassify_RealClaudeCodeFixture locks the five-bucket mapping against a
// body block lifted from a real Claude Code release. See ADR-004 for why
// preserving Python-parser semantics is the contract here.
func TestClassify_RealClaudeCodeFixture(t *testing.T) {
	body := `## What's changed

- Added ` + "`/loop`" + ` — autonomous self-paced loops [SDK] [Hooks]
- Fixed crash when ` + "`vscode`" + ` extension activates without workspace [VSCode]
- Improved Bash tool latency by 40% on macOS
- Changed default model routing for cheap-mode pipelines
- Breaking: removed deprecated ` + "`--legacy-auth`" + ` flag
- Deprecated ` + "`feature_flags.experimental`" + ` config key
`
	releases := []Release{{TagName: "v2.1.75", Body: body}}
	out := Classify(releases)
	if len(out) != 1 {
		t.Fatalf("Classify length = %d, want 1", len(out))
	}
	changes := out[0].Changes
	wantTypes := []string{TypeFeature, TypeFix, TypeImprovement, TypeImprovement, TypeBreaking, TypeDeprecation}
	if len(changes) != len(wantTypes) {
		t.Fatalf("changes length = %d, want %d (changes=%+v)", len(changes), len(wantTypes), changes)
	}
	for i, want := range wantTypes {
		if changes[i].Type != want {
			t.Errorf("changes[%d].Type = %q, want %q", i, changes[i].Type, want)
		}
	}
	// First change has [SDK] [Hooks] tags
	if len(changes[0].Tags) != 2 || changes[0].Tags[0] != "SDK" || changes[0].Tags[1] != "Hooks" {
		t.Errorf("changes[0].Tags = %v, want [SDK Hooks]", changes[0].Tags)
	}
	// Second change has [VSCode] tag
	if len(changes[1].Tags) != 1 || changes[1].Tags[0] != "VSCode" {
		t.Errorf("changes[1].Tags = %v, want [VSCode]", changes[1].Tags)
	}
	// Description has backticks stripped
	for i, c := range changes {
		if strings.Contains(c.Description, "`") {
			t.Errorf("changes[%d].Description still has backticks: %q", i, c.Description)
		}
	}
}
