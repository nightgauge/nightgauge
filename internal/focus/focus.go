// Package focus manages the configurable ideation lens for self-improvement.
//
// Focus lenses steer autonomous enhancement, release-watch scoring, and
// continuous-improvement proposals toward a specific quality dimension.
// The active lens is persisted in .nightgauge/focus.yaml and readable
// by all consumers (Go binary, VSCode extension, skills).
package focus

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Lens represents a focus configuration with scoring boosts and metadata.
type Lens struct {
	// Name is the canonical identifier (e.g., "quality", "features").
	Name string `yaml:"name" json:"name"`
	// Description is a human-readable explanation of the lens.
	Description string `yaml:"description" json:"description"`
	// ScoringBoosts maps assessment dimensions to bonus points (0–20).
	// Keys match assessment-engine.md dimension names.
	ScoringBoosts map[string]int `yaml:"scoring_boosts,omitempty" json:"scoringBoosts,omitempty"`
	// Keywords are additional search terms used to match release changes.
	Keywords []string `yaml:"keywords,omitempty" json:"keywords,omitempty"`
	// Builtin marks this as a built-in lens (not user-defined).
	Builtin bool `yaml:"-" json:"builtin"`
}

// State represents the persisted focus state in focus.yaml.
type State struct {
	// ActiveLens is the currently active lens name (empty = "general").
	ActiveLens string `yaml:"active_lens" json:"activeLens"`
	// SetAt is when the focus was last changed.
	SetAt time.Time `yaml:"set_at" json:"setAt"`
	// SetBy records who set the focus ("cli", "vscode", "ipc").
	SetBy string `yaml:"set_by" json:"setBy"`
	// CustomLenses holds user-defined lens definitions.
	CustomLenses []Lens `yaml:"custom_lenses,omitempty" json:"customLenses,omitempty"`
}

// BuiltinLenses returns the predefined lens definitions.
func BuiltinLenses() []Lens {
	return []Lens{
		{
			Name:        "general",
			Description: "Balanced improvement across all dimensions — no specific bias.",
			Builtin:     true,
		},
		{
			Name:        "quality",
			Description: "Focus on code quality, test coverage, linting, type safety, and correctness.",
			ScoringBoosts: map[string]int{
				"safety_reliability":   10,
				"pipeline_stage":       5,
				"developer_experience": 5,
			},
			Keywords: []string{"test", "coverage", "lint", "quality", "type", "strict", "validate", "correctness"},
			Builtin:  true,
		},
		{
			Name:        "features",
			Description: "Focus on new capabilities, tools, integrations, and product value.",
			ScoringBoosts: map[string]int{
				"pipeline_stage":       10,
				"automation_potential": 10,
			},
			Keywords: []string{"feature", "capability", "tool", "integration", "new", "add", "enable"},
			Builtin:  true,
		},
		{
			Name:        "security",
			Description: "Focus on vulnerability remediation, auth hardening, input validation, and compliance.",
			ScoringBoosts: map[string]int{
				"safety_reliability": 15,
				"cross_repo":         5,
			},
			Keywords: []string{"security", "vulnerability", "auth", "permission", "secret", "encrypt", "sanitize", "CVE"},
			Builtin:  true,
		},
		{
			Name:        "performance",
			Description: "Focus on speed, token efficiency, cost reduction, and resource optimization.",
			ScoringBoosts: map[string]int{
				"automation_potential":      10,
				"implementation_complexity": 5,
				"pipeline_stage":            5,
			},
			Keywords: []string{"performance", "speed", "token", "cost", "optimize", "cache", "reduce", "efficient"},
			Builtin:  true,
		},
		{
			Name:        "documentation",
			Description: "Focus on docs accuracy, coverage, onboarding, and knowledge management.",
			ScoringBoosts: map[string]int{
				"developer_experience": 15,
				"cross_repo":           5,
			},
			Keywords: []string{"documentation", "docs", "readme", "guide", "tutorial", "onboard", "reference"},
			Builtin:  true,
		},
		{
			Name:        "reliability",
			Description: "Focus on error handling, recovery, monitoring, health, and fault tolerance.",
			ScoringBoosts: map[string]int{
				"safety_reliability": 15,
				"pipeline_stage":     5,
			},
			Keywords: []string{"reliability", "error", "recovery", "health", "monitor", "retry", "resilient", "fault"},
			Builtin:  true,
		},
		{
			Name:        "ux",
			Description: "Focus on developer experience, CLI ergonomics, VSCode UI, and onboarding friction.",
			ScoringBoosts: map[string]int{
				"developer_experience": 15,
				"cross_repo":           5,
			},
			Keywords: []string{"ux", "experience", "ergonomic", "ui", "interface", "usability", "friction", "onboard"},
			Builtin:  true,
		},
	}
}

// Manager handles focus state operations.
type Manager struct {
	workspaceRoot string
}

// NewManager creates a focus manager for the given workspace.
func NewManager(workspaceRoot string) *Manager {
	return &Manager{workspaceRoot: workspaceRoot}
}

// focusPath returns the path to focus.yaml.
func (m *Manager) focusPath() string {
	return filepath.Join(m.workspaceRoot, ".nightgauge", "focus.yaml")
}

// Load reads the current focus state. Returns default state if file doesn't exist.
func (m *Manager) Load() (*State, error) {
	data, err := os.ReadFile(m.focusPath())
	if err != nil {
		if os.IsNotExist(err) {
			return &State{ActiveLens: "general"}, nil
		}
		return nil, fmt.Errorf("read focus.yaml: %w", err)
	}

	var s State
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse focus.yaml: %w", err)
	}
	if s.ActiveLens == "" {
		s.ActiveLens = "general"
	}
	return &s, nil
}

// Save writes focus state to focus.yaml.
func (m *Manager) Save(s *State) error {
	dir := filepath.Dir(m.focusPath())
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	data, err := yaml.Marshal(s)
	if err != nil {
		return fmt.Errorf("marshal focus.yaml: %w", err)
	}
	if err := os.WriteFile(m.focusPath(), data, 0o644); err != nil {
		return fmt.Errorf("write focus.yaml: %w", err)
	}
	return nil
}

// Set activates a named lens. Returns error if lens not found.
func (m *Manager) Set(name string, source string) (*State, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, fmt.Errorf("lens name cannot be empty")
	}

	// Validate lens exists (built-in or custom)
	if !m.lensExists(name) {
		return nil, fmt.Errorf("unknown lens %q — available: %s", name, m.availableLensNames())
	}

	s, err := m.Load()
	if err != nil {
		s = &State{}
	}

	s.ActiveLens = name
	s.SetAt = time.Now().UTC()
	s.SetBy = source

	if err := m.Save(s); err != nil {
		return nil, err
	}
	return s, nil
}

// Clear resets focus to "general".
func (m *Manager) Clear(source string) (*State, error) {
	return m.Set("general", source)
}

// Show returns the current state and resolved lens definition.
func (m *Manager) Show() (*State, *Lens, error) {
	s, err := m.Load()
	if err != nil {
		return nil, nil, err
	}
	lens := m.ResolveLens(s.ActiveLens, s)
	return s, lens, nil
}

// ResolveLens finds a lens by name from built-ins and custom lenses.
func (m *Manager) ResolveLens(name string, s *State) *Lens {
	name = strings.ToLower(name)
	for _, l := range BuiltinLenses() {
		if l.Name == name {
			return &l
		}
	}
	if s != nil {
		for _, l := range s.CustomLenses {
			if strings.ToLower(l.Name) == name {
				return &l
			}
		}
	}
	// Fallback to general
	general := BuiltinLenses()[0]
	return &general
}

// lensExists checks if a lens name is valid.
func (m *Manager) lensExists(name string) bool {
	for _, l := range BuiltinLenses() {
		if l.Name == name {
			return true
		}
	}
	s, err := m.Load()
	if err != nil {
		return false
	}
	for _, l := range s.CustomLenses {
		if strings.ToLower(l.Name) == name {
			return true
		}
	}
	return false
}

// availableLensNames returns a comma-separated list of all lens names.
func (m *Manager) availableLensNames() string {
	var names []string
	for _, l := range BuiltinLenses() {
		names = append(names, l.Name)
	}
	s, _ := m.Load()
	if s != nil {
		for _, l := range s.CustomLenses {
			names = append(names, l.Name)
		}
	}
	return strings.Join(names, ", ")
}

// AllLenses returns all available lenses (built-in + custom).
func (m *Manager) AllLenses() []Lens {
	lenses := BuiltinLenses()
	s, err := m.Load()
	if err != nil {
		return lenses
	}
	for _, l := range s.CustomLenses {
		lenses = append(lenses, l)
	}
	return lenses
}
