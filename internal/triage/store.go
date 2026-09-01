package triage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Dir is the run-scoped artifact area for triage records.
//
// Beside the other `.nightgauge/` artifact directories rather than under
// `pipeline/`, because an ad-hoc triage has no issue number to be scoped by —
// the entire premise of #1262 is a red check that no issue exists for.
func Dir(workspace string) string {
	return filepath.Join(workspace, ".nightgauge", "triage")
}

// Path is the file for one record id.
func Path(workspace, id string) string {
	return filepath.Join(Dir(workspace), id+".json")
}

// Write persists a record, stamping V and CreatedAt when unset.
//
// It writes an INVALID record too, and reports the violations separately. A
// store that refused to persist a failing investigation would delete the only
// evidence of what was tried, which is the material the next session needs
// most — and it would push the author toward writing whatever the validator
// accepts rather than what happened.
func Write(workspace string, rec Record) (string, []Violation, error) {
	if rec.V == 0 {
		rec.V = SchemaVersion
	}
	if strings.TrimSpace(rec.CreatedAt) == "" {
		rec.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(rec.ID) == "" {
		rec.ID = NewID(rec.Target.Value, time.Now())
	}
	if err := os.MkdirAll(Dir(workspace), 0o755); err != nil {
		return "", nil, fmt.Errorf("create triage dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", nil, fmt.Errorf("encode triage record: %w", err)
	}
	path := Path(workspace, rec.ID)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", nil, fmt.Errorf("write triage record: %w", err)
	}
	return path, rec.Validate(), nil
}

// Read loads one record by id.
func Read(workspace, id string) (Record, error) {
	var rec Record
	data, err := os.ReadFile(Path(workspace, id))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse triage record %s: %w", id, err)
	}
	return rec, nil
}

// List returns every record id in the workspace, newest-looking last (ids carry
// a sortable UTC timestamp suffix).
func List(workspace string) ([]string, error) {
	entries, err := os.ReadDir(Dir(workspace))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".json"))
	}
	sort.Strings(out)
	return out, nil
}
