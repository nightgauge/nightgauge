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
// Under `.nightgauge/` rather than `.nightgauge/pipeline/` because an ad-hoc
// triage has no issue number to be scoped by — the entire premise of #1262 is a
// red check that no issue exists for.
//
// The `checks/` segment is not decoration (#1269). `.nightgauge/triage/` already
// existed and already had tenants: `backlog-groom` writes its report JSON and
// Markdown there, and `_shared/RUN_REFLECTION.md` points skills at
// `runs.jsonl` in the same directory. Writing records flat into that namespace
// made `triage list` report backlog-groom reports as records, and made
// `triage check` parse a 947 KB grooming report and emit the full contract
// violation list against it — every line false, stated confidently. That is the
// misreporting probe from #1263's worked example, reproduced inside the tooling
// built to prevent it.
func Dir(workspace string) string {
	return filepath.Join(workspace, ".nightgauge", "triage", "checks")
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
//
// A document whose schema version is not this one is rejected as "not a record"
// rather than decoded and handed to Validate. JSON decoding ignores unknown
// fields, so any object at all unmarshals into Record successfully — with every
// field zero. Validating that produces a confident list of contract violations
// about a file that was never a triage record, which is worse than an error in
// exactly the way #1269 demonstrated: it is wrong, specific, and sounds
// authoritative.
//
// A genuine record carrying a version this binary does not know reaches the
// same branch, and the message says so.
func Read(workspace, id string) (Record, error) {
	var rec Record
	data, err := os.ReadFile(Path(workspace, id))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse triage record %s: %w", id, err)
	}
	if rec.V != SchemaVersion {
		return Record{}, fmt.Errorf(
			"%s is not a v%d triage record (found schema version %d) — nothing to validate",
			id, SchemaVersion, rec.V)
	}
	return rec, nil
}

// List returns every record id in the workspace, newest-looking last (ids carry
// a sortable UTC timestamp suffix).
//
// A file is listed only if it actually parses as a record of this schema
// version. The `checks/` directory makes a foreign file unlikely; this makes it
// impossible, and it also hides a half-written file mid-save. Reporting a name
// on the strength of its extension is a claim about content taken from the
// filename — the same shape of unchecked assertion this package exists to
// refuse.
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
		id := strings.TrimSuffix(e.Name(), ".json")
		if _, err := Read(workspace, id); err != nil {
			continue
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}
