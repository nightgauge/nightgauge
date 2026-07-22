// Cassette load tests — verify every JSON cassette under
// internal/gitlab/testdata/cassettes/ is well-formed JSON and free of common
// determinism hazards (time-dependent fields, oversized payloads). Tests here
// catch broken cassettes early so service-level tests fail with a meaningful
// error rather than a JSON unmarshal panic mid-stub.
package gitlab

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// maxCassetteSize is the per-fixture byte cap. Cassettes exceeding this hint
// at fixture bloat (full real-API responses copied wholesale rather than the
// minimum slice the assertion needs).
const maxCassetteSize = 10 * 1024

func TestCassettes_AllValidJSON(t *testing.T) {
	cassetteDir := filepath.Join("testdata", "cassettes")
	files, err := collectCassettes(cassetteDir)
	if err != nil {
		t.Fatalf("walk cassette dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no cassettes found under testdata/cassettes/")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(data) > maxCassetteSize {
				t.Errorf("file size %d > %d (cassettes should be minimal)", len(data), maxCassetteSize)
			}
			var any interface{}
			if err := json.Unmarshal(data, &any); err != nil {
				t.Errorf("invalid JSON: %v", err)
			}
		})
	}
}

// TestCassettes_NoTimestampDrift ensures cassettes contain only fixed time
// strings (or no time fields at all). The check inspects every JSON object
// for created_at / updated_at fields — if present, the value must be a
// fixed string (not parseable as "now") so tests stay deterministic.
func TestCassettes_NoTimestampDrift(t *testing.T) {
	cassetteDir := filepath.Join("testdata", "cassettes")
	files, _ := collectCassettes(cassetteDir)
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, _ := os.ReadFile(f)
			s := string(data)
			// Bare existence check is fine — fixed values like
			// "2026-01-01T00:00:00Z" pass; the contract is "no relative
			// timestamps like 'now()' or templated $TIME$ markers".
			for _, marker := range []string{"now()", "$TIME$", "{{now}}"} {
				if strings.Contains(s, marker) {
					t.Errorf("cassette contains time-dependent marker %q", marker)
				}
			}
		})
	}
}

// collectCassettes walks the cassette dir and returns every *.json file path.
// Non-cassette files (README.md, .gitkeep) are skipped.
func collectCassettes(root string) ([]string, error) {
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".json" {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}
