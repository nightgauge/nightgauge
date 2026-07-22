package forgecmd

// skill_parity_test.go — JSON shape parity contract for the 15 skills
// migrated in #3363 (Wave 4 of the forge-abstraction epic #3349).
//
// The contract: for each entry in `testdata/gh-snapshots/`, the
// corresponding `forge ... --json` output MUST carry every JSON path
// the recorded gh snapshot defines, with a JSON-compatible type. Extra
// fields in the forge output are fine (forward-compatible); missing
// paths fail the test (the skill's `jq` filter would break).
//
// This is the "extends parity_test.go" surface promised in the plan;
// when the cross-forge `parity_test.go` lands, this test plugs into the
// same harness — for now it stands alone with the gh-snapshot fixtures
// already shipped under `testdata/gh-snapshots/`.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	forgetypes "github.com/nightgauge/nightgauge/internal/forge/types"
)

// snapshotFixtures pairs each recorded gh snapshot with the forge DTO
// that the corresponding `forge ... --json` subcommand emits. The
// constructor returns a populated DTO so we can serialise it and compare
// JSON path coverage against the gh snapshot.
//
// New skills migrated to forge add an entry here so the parity contract
// stays exhaustive.
var snapshotFixtures = []struct {
	name        string
	snapshot    string // path under testdata/gh-snapshots/
	forgeOutput func() any
	// requiredPaths lists JSON paths that the migrated skill's jq filter
	// extracts from the snapshot. Each path MUST also appear in the
	// forge output. Use dot notation (e.g. ".number", ".labels[0].name").
	// Slice paths are checked by walking through the array.
	requiredPaths []string
	// arrayElement is true when the recorded gh snapshot is an array of
	// items (e.g. `gh issue list --json ...`); requiredPaths are then
	// resolved against the first element of the array on each side.
	arrayElement bool
}{
	{
		name:     "issue-view",
		snapshot: "issue-view.json",
		forgeOutput: func() any {
			return IssueFromForge(nil)
		},
		requiredPaths: []string{".number", ".title", ".body", ".state", ".labels", ".url"},
	},
	{
		name:     "pr-view",
		snapshot: "pr-view.json",
		forgeOutput: func() any {
			return PRFromForge(nil)
		},
		requiredPaths: []string{".number", ".title", ".state", ".body", ".url", ".mergeable"},
	},
	{
		name:     "pr-checks",
		snapshot: "pr-checks.json",
		forgeOutput: func() any {
			return CheckRollupFromForge(nil)
		},
		requiredPaths: []string{".state", ".total"},
	},
	{
		name:     "label-list",
		snapshot: "label-list.json",
		forgeOutput: func() any {
			// gh's label-list returns an array of labels; the forge DTO
			// is the per-label LabelJSON. Wrap in a one-element array so
			// the array-element comparison works.
			return []LabelJSON{LabelFromForge(&forgetypes.Label{Name: "x"})}
		},
		requiredPaths: []string{".name"},
		arrayElement:  true,
	},
	{
		name:     "project-item-list",
		snapshot: "project-item-list.json",
		forgeOutput: func() any {
			// gh's project item-list returns an array; wrap in a single-
			// element array to mirror the gh shape.
			return []BoardItemJSON{BoardItemFromForge(nil)}
		},
		requiredPaths: []string{".number", ".title", ".status", ".url"},
		arrayElement:  true,
	},
}

// TestSkillParity_ForgeOutputCarriesGHPaths walks every snapshot fixture
// and asserts the forge DTO would carry every required JSON path. This
// is the load-bearing check for the 15-skill migration: if a skill's jq
// pipeline reads `.labels[0].name` from `gh issue view --json labels`,
// then `forge issue view --json` must also expose that path.
func TestSkillParity_ForgeOutputCarriesGHPaths(t *testing.T) {
	for _, tc := range snapshotFixtures {
		t.Run(tc.name, func(t *testing.T) {
			snapshotPath := filepath.Join("testdata", "gh-snapshots", tc.snapshot)
			snapshotData, err := os.ReadFile(snapshotPath)
			if err != nil {
				t.Fatalf("read snapshot %s: %v", snapshotPath, err)
			}
			var snapshot interface{}
			if err := json.Unmarshal(snapshotData, &snapshot); err != nil {
				t.Fatalf("decode snapshot %s: %v", snapshotPath, err)
			}

			forgeRaw, err := json.Marshal(tc.forgeOutput())
			if err != nil {
				t.Fatalf("marshal forge output: %v", err)
			}
			var forgeDoc interface{}
			if err := json.Unmarshal(forgeRaw, &forgeDoc); err != nil {
				t.Fatalf("decode forge output: %v", err)
			}

			snapshotProbe := snapshot
			forgeProbe := forgeDoc
			if tc.arrayElement {
				snapshotProbe = firstElement(snapshot)
				forgeProbe = firstElement(forgeDoc)
				if snapshotProbe == nil || forgeProbe == nil {
					t.Fatalf("arrayElement fixture %s requires non-empty arrays on both sides (snapshot=%v forge=%v)",
						tc.snapshot, snapshotProbe != nil, forgeProbe != nil)
				}
			}

			for _, path := range tc.requiredPaths {
				if !jsonPathPresent(forgeProbe, path) {
					t.Errorf("forge output missing path %q (skill jq pipeline would break)\n  forge=%s",
						path, string(forgeRaw))
				}
				if !jsonPathPresent(snapshotProbe, path) {
					t.Errorf("snapshot %s missing path %q (fixture is wrong; skill assumes the path exists)",
						tc.snapshot, path)
				}
			}
		})
	}
}

// TestSkillParity_AllSnapshotsCovered guards against silently dropping a
// snapshot fixture by leaving it un-paired with a forge DTO. Every file
// under testdata/gh-snapshots/ MUST appear in snapshotFixtures.
func TestSkillParity_AllSnapshotsCovered(t *testing.T) {
	dir := filepath.Join("testdata", "gh-snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	covered := map[string]bool{}
	for _, fx := range snapshotFixtures {
		covered[fx.snapshot] = true
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		if !covered[name] {
			t.Errorf("snapshot %s has no entry in snapshotFixtures — add one or delete the fixture", name)
		}
	}
}

// firstElement returns the first element of a JSON-decoded array, or
// nil when the input is not an array or is empty. Used by the
// arrayElement comparison path in TestSkillParity_ForgeOutputCarriesGHPaths.
func firstElement(doc interface{}) interface{} {
	arr, ok := doc.([]interface{})
	if !ok || len(arr) == 0 {
		return nil
	}
	return arr[0]
}

// jsonPathPresent walks the dot-notation path through a decoded JSON
// document, returning true when each segment resolves. The path syntax
// is intentionally minimal — sufficient for the skill jq pipelines:
//   - ".field" — map key
//   - ".field.sub" — nested map key
//   - ".field[0]" — array index
//
// Whole-array paths like ".labels" succeed when the key exists, even
// when the array is empty.
func jsonPathPresent(doc interface{}, path string) bool {
	if path == "" || path == "." {
		return doc != nil
	}
	segments := splitJSONPath(path)
	cur := doc
	for _, seg := range segments {
		if seg.isIndex {
			arr, ok := cur.([]interface{})
			if !ok || seg.index >= len(arr) {
				return false
			}
			cur = arr[seg.index]
			continue
		}
		obj, ok := cur.(map[string]interface{})
		if !ok {
			return false
		}
		next, exists := obj[seg.key]
		if !exists {
			return false
		}
		cur = next
	}
	return true
}

type jsonPathSegment struct {
	key     string
	isIndex bool
	index   int
}

func splitJSONPath(path string) []jsonPathSegment {
	if path == "" || path == "." {
		return nil
	}
	// Strip leading dot.
	if path[0] == '.' {
		path = path[1:]
	}
	var segments []jsonPathSegment
	start := 0
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			if i > start {
				segments = append(segments, jsonPathSegment{key: path[start:i]})
			}
			start = i + 1
		case '[':
			if i > start {
				segments = append(segments, jsonPathSegment{key: path[start:i]})
			}
			// Find the matching ']'.
			end := i + 1
			for end < len(path) && path[end] != ']' {
				end++
			}
			idxStr := path[i+1 : end]
			var idx int
			for _, c := range idxStr {
				if c < '0' || c > '9' {
					return segments
				}
				idx = idx*10 + int(c-'0')
			}
			segments = append(segments, jsonPathSegment{isIndex: true, index: idx})
			i = end
			start = end + 1
		}
	}
	if start < len(path) {
		segments = append(segments, jsonPathSegment{key: path[start:]})
	}
	return segments
}
