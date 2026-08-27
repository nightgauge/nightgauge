package github

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// createLabelLine matches the repo-init skill's provisioning idiom:
//
//	create_label "pipeline:refined" "0969da" "Issue has been refined..."
var createLabelLine = regexp.MustCompile(`create_label\s+"([^"]+)"\s+"([^"]+)"\s+"([^"]*)"`)

// TestRequiredLabels_MatchTheRepoInitSkill pins the two provisioners together.
//
// `nightgauge label ensure` and the repo-init skill both create these labels,
// and BOTH are idempotent by name — so neither ever repairs the other. Whichever
// runs first silently decides the colour and description, and a repo provisioned
// one way looks different from a repo provisioned the other with nothing
// reporting it. That drift was real: the registry briefly gave pipeline:refined
// the same blue as type:feature.
//
// This reads the skill file rather than restating its values, so it fails when
// either side moves.
func TestRequiredLabels_MatchTheRepoInitSkill(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "nightgauge-repo-init", "_includes", "labels.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	skill := map[string][2]string{} // name → {color, description}
	for _, m := range createLabelLine.FindAllStringSubmatch(string(data), -1) {
		skill[m[1]] = [2]string{m[2], m[3]}
	}
	if len(skill) == 0 {
		t.Fatalf("parsed no create_label lines out of %s — the skill's provisioning "+
			"idiom changed and this guard has gone blind", path)
	}

	for _, want := range RequiredLabels {
		got, ok := skill[want.Name]
		if !ok {
			t.Errorf("%s does not provision required label %q — a repo set up by the "+
				"skill alone will be missing it", path, want.Name)
			continue
		}
		if !strings.EqualFold(got[0], want.Color) {
			t.Errorf("label %q colour: registry has %q, %s has %q — whichever provisioner "+
				"runs first wins and neither repairs the other",
				want.Name, want.Color, path, got[0])
		}
		if got[1] != want.Description {
			t.Errorf("label %q description: registry has %q, %s has %q",
				want.Name, want.Description, path, got[1])
		}
	}
}

// TestRequiredLabels_ColoursAreDistinct catches the specific mistake that
// prompted the guard above: giving a pipeline label a colour already in use by
// a different label, which makes the two indistinguishable in the GitHub UI.
func TestRequiredLabels_ColoursAreDistinct(t *testing.T) {
	path := filepath.Join("..", "..", "skills", "nightgauge-repo-init", "_includes", "labels.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	byColour := map[string][]string{}
	for _, m := range createLabelLine.FindAllStringSubmatch(string(data), -1) {
		c := strings.ToLower(m[2])
		byColour[c] = append(byColour[c], m[1])
	}

	// Collisions that already exist in every provisioned repository. A label's
	// colour is durable state on GitHub and `Create` is idempotent by NAME, so
	// changing one of these in the registry would repaint nothing that exists
	// and would give only FUTURE repos a different colour — strictly more drift
	// than leaving it. They are listed rather than skipped so that a NEW
	// collision still fails this test.
	known := map[string]string{
		"owner-action": "type:refactor", // both fbca04, predates the registry
	}

	for _, l := range RequiredLabels {
		names := byColour[strings.ToLower(l.Color)]
		for _, other := range names {
			// Pipeline labels may share with each other — type:epic and
			// auto-process deliberately share 8957e5 in the shipped taxonomy.
			if other == l.Name || isRequiredLabelName(other) {
				continue
			}
			if known[l.Name] == other {
				continue
			}
			t.Errorf("required label %q uses colour %q, already used by %q — the two are "+
				"indistinguishable in the GitHub UI. If this is deliberate and already "+
				"provisioned everywhere, add it to the `known` map with the reason.",
				l.Name, l.Color, other)
		}
	}
}

func isRequiredLabelName(name string) bool {
	for _, l := range RequiredLabels {
		if l.Name == name {
			return true
		}
	}
	return false
}
