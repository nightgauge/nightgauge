// Package metrics aggregates knowledge-events.jsonl into the typed Result
// surfaced by both `nightgauge knowledge metrics --json` and the
// `knowledge.metrics` IPC method.
//
// The aggregator is the single source of truth for the KB Value dashboard
// numbers (#3600). It streams the JSONL file line-by-line so a missing or
// large file never blocks aggregation, and returns a Status enum so the
// caller can branch on enabled/disabled/empty without inspecting Totals.
package metrics

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/knowledge/telemetry"
)

// Status enumerates the high-level state the UI uses to pick an empty-state
// branch. The aggregator sets it after a single pass.
type Status string

const (
	// StatusEnabled means at least one event matched the window.
	StatusEnabled Status = "enabled"
	// StatusEmpty means telemetry is enabled but no events were found
	// (either the JSONL file is missing or no events fall in the window).
	StatusEmpty Status = "empty"
	// StatusDisabled is reserved for future plumbing — the aggregator itself
	// cannot read config; callers may overwrite Status with "disabled" when
	// KnowledgeConfig.IsTelemetryEnabled() is false.
	StatusDisabled Status = "disabled"
)

// Result is the typed payload returned by Aggregate. The JSON shape is the
// IPC contract — IPC and CLI both serialize this struct verbatim.
type Result struct {
	WindowDays        int                      `json:"window_days"`
	StaleDays         int                      `json:"stale_days"`
	Status            Status                   `json:"status"`
	GeneratedAt       string                   `json:"generated_at"`
	HitRate           *float64                 `json:"hit_rate,omitempty"`
	Totals            Totals                   `json:"totals"`
	PerStage          []PerStageEntry          `json:"per_stage"`
	TopRecalled       []TopRecalledEntry       `json:"top_recalled"`
	StaleEntries      []StaleEntry             `json:"stale_entries"`
	GraduationHistory []GraduationHistoryEntry `json:"graduation_history"`
}

// Totals is the header-card counter block. Each field maps to one event type
// the UI surfaces.
type Totals struct {
	Writes        int `json:"writes"`
	Reads         int `json:"reads"`
	Recalls       int `json:"recalls"`
	RecallHits    int `json:"recall_hits"`
	Graduations   int `json:"graduations"`
	Scaffolds     int `json:"scaffolds"`
	Prunes        int `json:"prunes"`
	Indexes       int `json:"indexes"`
	Validates     int `json:"validates"`
	Stats         int `json:"stats"`
	EventsInRange int `json:"events_in_range"`
}

// PerStageEntry is one row in the per-stage bar chart. Reads and Writes are
// the two visualized counters; the others are reserved for future stacked
// views and are emitted so the UI doesn't need a second pass.
type PerStageEntry struct {
	Stage      string `json:"stage"`
	Reads      int    `json:"reads"`
	Writes     int    `json:"writes"`
	Recalls    int    `json:"recalls"`
	RecallHits int    `json:"recall_hits"`
}

// TopRecalledEntry is one row in the top-recalled table. Hits combines read
// and recall_hit events because both signal "this entry was used".
type TopRecalledEntry struct {
	Path string `json:"path"`
	Hits int    `json:"hits"`
}

// StaleEntry is one row in the stale-entries table. DaysSinceTouch is the
// floor of (now - LastTouchedAt) in days; entries never touched in-window
// receive DaysSinceTouch = staleDays + 1 to surface them in the same place.
type StaleEntry struct {
	Path           string `json:"path"`
	LastTouchedAt  string `json:"last_touched_at,omitempty"`
	DaysSinceTouch int    `json:"days_since_touch"`
}

// GraduationHistoryEntry is one row in the graduation history. Mode is the
// telemetry.Event.Mode field ("manual" or "auto"; empty maps to "manual" to
// preserve legacy semantics).
type GraduationHistoryEntry struct {
	Timestamp   string `json:"timestamp"`
	IssueNumber int    `json:"issue_number,omitempty"`
	Path        string `json:"path,omitempty"`
	Mode        string `json:"mode"`
}

// Aggregate streams telemetry.Path(workspaceRoot) and returns a Result
// filtered to events in [now-windowDays, now]. Missing file returns a
// StatusEmpty Result with zero totals and a populated GeneratedAt — the
// caller decides whether to overlay StatusDisabled based on config.
//
// windowDays must be > 0; staleDays must be >= 0. staleDays governs the
// stale-entries cutoff (entries whose latest read/recall_hit event is older
// than staleDays days, OR who have no read/recall_hit events at all in the
// window, are flagged).
func Aggregate(workspaceRoot string, windowDays, staleDays int) (Result, error) {
	return AggregateAt(workspaceRoot, windowDays, staleDays, time.Now())
}

// AggregateAt is Aggregate with an injectable "now" for deterministic tests.
func AggregateAt(workspaceRoot string, windowDays, staleDays int, now time.Time) (Result, error) {
	if windowDays <= 0 {
		return Result{}, errors.New("windowDays must be > 0")
	}
	if staleDays < 0 {
		return Result{}, errors.New("staleDays must be >= 0")
	}

	result := Result{
		WindowDays:        windowDays,
		StaleDays:         staleDays,
		Status:            StatusEmpty,
		GeneratedAt:       now.UTC().Format(time.RFC3339),
		PerStage:          []PerStageEntry{},
		TopRecalled:       []TopRecalledEntry{},
		StaleEntries:      []StaleEntry{},
		GraduationHistory: []GraduationHistoryEntry{},
	}

	path := telemetry.Path(workspaceRoot)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, fmt.Errorf("open knowledge events: %w", err)
	}
	defer f.Close()

	cutoff := now.Add(-time.Duration(windowDays) * 24 * time.Hour)
	staleCutoff := now.Add(-time.Duration(staleDays) * 24 * time.Hour)

	perStage := map[string]*PerStageEntry{}
	hitsByPath := map[string]int{}
	lastTouchByPath := map[string]time.Time{}
	allPathsTouched := map[string]struct{}{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	for scanner.Scan() {
		var ev telemetry.Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Timestamp == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339, ev.Timestamp)
		if err != nil {
			continue
		}
		if ts.Before(cutoff) || ts.After(now) {
			continue
		}

		result.Totals.EventsInRange++

		stage := ev.Stage
		if stage == "" {
			stage = "unknown"
		}
		ps, ok := perStage[stage]
		if !ok {
			ps = &PerStageEntry{Stage: stage}
			perStage[stage] = ps
		}

		switch ev.Type {
		case telemetry.EventWrite:
			result.Totals.Writes++
			ps.Writes++
			if ev.Path != "" {
				allPathsTouched[normalizePath(workspaceRoot, ev.Path)] = struct{}{}
			}
		case telemetry.EventRead:
			result.Totals.Reads++
			ps.Reads++
			if ev.Path != "" {
				key := normalizePath(workspaceRoot, ev.Path)
				hitsByPath[key]++
				if prev, ok := lastTouchByPath[key]; !ok || ts.After(prev) {
					lastTouchByPath[key] = ts
				}
				allPathsTouched[key] = struct{}{}
			}
		case telemetry.EventRecall:
			result.Totals.Recalls++
			ps.Recalls++
		case telemetry.EventRecallHit:
			result.Totals.RecallHits++
			ps.RecallHits++
			if ev.Path != "" {
				key := normalizePath(workspaceRoot, ev.Path)
				hitsByPath[key]++
				if prev, ok := lastTouchByPath[key]; !ok || ts.After(prev) {
					lastTouchByPath[key] = ts
				}
				allPathsTouched[key] = struct{}{}
			}
		case telemetry.EventGraduate:
			result.Totals.Graduations++
			mode := ev.Mode
			if mode == "" {
				mode = "manual"
			}
			result.GraduationHistory = append(result.GraduationHistory, GraduationHistoryEntry{
				Timestamp:   ts.UTC().Format(time.RFC3339),
				IssueNumber: ev.IssueNumber,
				Path:        ev.Path,
				Mode:        mode,
			})
		case telemetry.EventScaffold:
			result.Totals.Scaffolds++
			if ev.Path != "" {
				allPathsTouched[normalizePath(workspaceRoot, ev.Path)] = struct{}{}
			}
		case telemetry.EventPrune:
			result.Totals.Prunes++
		case telemetry.EventIndex:
			result.Totals.Indexes++
		case telemetry.EventValidate:
			result.Totals.Validates++
		case telemetry.EventStats:
			result.Totals.Stats++
		}
	}
	if err := scanner.Err(); err != nil {
		return result, fmt.Errorf("scan knowledge events: %w", err)
	}

	if result.Totals.EventsInRange == 0 {
		return result, nil
	}
	result.Status = StatusEnabled

	if result.Totals.Recalls > 0 {
		hr := float64(result.Totals.RecallHits) / float64(result.Totals.Recalls)
		result.HitRate = &hr
	}

	// Per-stage in deterministic alphabetical order.
	stages := make([]string, 0, len(perStage))
	for s := range perStage {
		stages = append(stages, s)
	}
	sort.Strings(stages)
	for _, s := range stages {
		result.PerStage = append(result.PerStage, *perStage[s])
	}

	// Top-recalled: descending by hits, then ascending by path for stability.
	for path, hits := range hitsByPath {
		result.TopRecalled = append(result.TopRecalled, TopRecalledEntry{Path: path, Hits: hits})
	}
	sort.SliceStable(result.TopRecalled, func(i, j int) bool {
		if result.TopRecalled[i].Hits != result.TopRecalled[j].Hits {
			return result.TopRecalled[i].Hits > result.TopRecalled[j].Hits
		}
		return result.TopRecalled[i].Path < result.TopRecalled[j].Path
	})
	if len(result.TopRecalled) > 10 {
		result.TopRecalled = result.TopRecalled[:10]
	}

	// Stale: a touched path with no read/recall_hit since staleCutoff, OR a
	// path that was scaffolded/written in the window but never read.
	for p := range allPathsTouched {
		last, hasRead := lastTouchByPath[p]
		if !hasRead {
			result.StaleEntries = append(result.StaleEntries, StaleEntry{
				Path:           p,
				DaysSinceTouch: staleDays + 1,
			})
			continue
		}
		if last.Before(staleCutoff) {
			result.StaleEntries = append(result.StaleEntries, StaleEntry{
				Path:           p,
				LastTouchedAt:  last.UTC().Format(time.RFC3339),
				DaysSinceTouch: int(now.Sub(last).Hours() / 24),
			})
		}
	}
	sort.SliceStable(result.StaleEntries, func(i, j int) bool {
		if result.StaleEntries[i].DaysSinceTouch != result.StaleEntries[j].DaysSinceTouch {
			return result.StaleEntries[i].DaysSinceTouch > result.StaleEntries[j].DaysSinceTouch
		}
		return result.StaleEntries[i].Path < result.StaleEntries[j].Path
	})
	if len(result.StaleEntries) > 25 {
		result.StaleEntries = result.StaleEntries[:25]
	}

	// Graduation history: most-recent first.
	sort.SliceStable(result.GraduationHistory, func(i, j int) bool {
		return result.GraduationHistory[i].Timestamp > result.GraduationHistory[j].Timestamp
	})

	return result, nil
}

// normalizePath converts an absolute or workspace-relative path emitted by
// telemetry into a stable workspace-relative key.
func normalizePath(workspaceRoot, p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(workspaceRoot, p); err == nil {
			return rel
		}
	}
	// Defensive: collapse leading ./
	return strings.TrimPrefix(p, "./")
}
