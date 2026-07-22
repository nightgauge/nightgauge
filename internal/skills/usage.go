// Package skills provides deterministic readers and writers for skill-usage
// telemetry stored at .nightgauge/skills/usage.jsonl. The log is appended
// by the PreToolUse(Skill) hook and aggregated by `nightgauge skills usage`.
package skills

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// usageRelPath is the in-repo location of the append-only usage log.
const usageRelPath = ".nightgauge/skills/usage.jsonl"

// Record is one skill invocation, one JSON object per line.
type Record struct {
	TS      string `json:"ts"`                // RFC3339 UTC
	Skill   string `json:"skill"`             // skill name (e.g. nightgauge-security-audit)
	Session string `json:"session,omitempty"` // Claude Code session id, when available
}

// Stats is the aggregated telemetry for one skill.
type Stats struct {
	Skill        string `json:"skill"`
	TriggerCount int    `json:"trigger_count"`
	FirstSeen    string `json:"first_seen,omitempty"`
	LastSeen     string `json:"last_seen,omitempty"`
	NeverSeen    bool   `json:"never_seen"`
}

// UsageFilePath returns the absolute path to the usage log for the given root.
func UsageFilePath(root string) string {
	return filepath.Join(root, usageRelPath)
}

// AppendRecord appends one record to the usage log, creating parent dirs as
// needed. It is best-effort: a write error is returned but callers in the hook
// path should never let it block the tool.
func AppendRecord(root string, rec Record) error {
	if rec.TS == "" {
		rec.TS = time.Now().UTC().Format(time.RFC3339)
	}
	path := UsageFilePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create skills usage dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open skills usage log: %w", err)
	}
	defer f.Close()
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal usage record: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		return fmt.Errorf("write usage record: %w", err)
	}
	return nil
}

// ReadUsage parses every record from the usage log. A missing file is not an
// error (returns nil). Malformed lines are skipped, not fatal.
func ReadUsage(root string) ([]Record, error) {
	path := UsageFilePath(root)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open skills usage log: %w", err)
	}
	defer f.Close()

	var out []Record
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(line, &rec); err != nil {
			fmt.Fprintf(os.Stderr, "warning: skipping malformed line in %s: %v\n", path, err)
			continue
		}
		if rec.Skill == "" {
			continue
		}
		out = append(out, rec)
	}
	if err := scanner.Err(); err != nil {
		return out, fmt.Errorf("scan skills usage log: %w", err)
	}
	return out, nil
}

// CatalogNames returns the skill names discovered under <root>/skills/*/SKILL.md.
// Used to flag never-triggered skills. A missing skills/ dir returns nil.
func CatalogNames(root string) ([]string, error) {
	matches, err := filepath.Glob(filepath.Join(root, "skills", "*", "SKILL.md"))
	if err != nil {
		return nil, fmt.Errorf("glob skills catalog: %w", err)
	}
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, filepath.Base(filepath.Dir(m)))
	}
	sort.Strings(names)
	return names, nil
}

// Aggregate folds records into per-skill stats, sorted by trigger count
// descending then name. When catalog is non-empty, skills present in the catalog
// but absent from the log are appended with NeverSeen=true and TriggerCount=0.
func Aggregate(records []Record, catalog []string) []Stats {
	bySkill := make(map[string]*Stats)
	for _, r := range records {
		s, ok := bySkill[r.Skill]
		if !ok {
			s = &Stats{Skill: r.Skill, FirstSeen: r.TS}
			bySkill[r.Skill] = s
		}
		s.TriggerCount++
		if r.TS != "" {
			if s.FirstSeen == "" || r.TS < s.FirstSeen {
				s.FirstSeen = r.TS
			}
			if r.TS > s.LastSeen {
				s.LastSeen = r.TS
			}
		}
	}

	for _, name := range catalog {
		if _, ok := bySkill[name]; !ok {
			bySkill[name] = &Stats{Skill: name, NeverSeen: true}
		}
	}

	out := make([]Stats, 0, len(bySkill))
	for _, s := range bySkill {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TriggerCount != out[j].TriggerCount {
			return out[i].TriggerCount > out[j].TriggerCount
		}
		return out[i].Skill < out[j].Skill
	})
	return out
}
