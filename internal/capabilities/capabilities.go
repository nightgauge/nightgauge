// Package capabilities loads and validates the capability registry —
// capabilities.yaml at the repository root.
//
// This is the ONE authored layer of the workspace knowledge graph (ADR-005,
// Decision 2). Every other node kind in the graph is derived from the tree or
// the forge; this file is written by hand, and everything else attaches to a
// capability declared here.
//
// Validation exists because a hand-maintained index rots. Four already have in
// this workspace: the knowledge-base README, the capability table, the strategy
// alignment sweep, and the adapter matrix. The anti-rot mechanism is
// ValidateAgainstTree: an `owns:` glob that matches nothing is a validation
// FAILURE, not an empty set. A capability whose code was deleted or moved fails
// the build instead of silently describing a product that no longer exists.
package capabilities

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the registry format this package understands. A file
// declaring anything else is refused rather than best-effort parsed.
const SchemaVersion = 1

// Status is a capability's maturity. The set is closed: an unknown value is a
// named error, never a pass-through string, because downstream consumers
// (the surface matrix, INV-CAPABILITY) branch on it.
type Status string

const (
	StatusPlanned    Status = "planned"
	StatusAlpha      Status = "alpha"
	StatusBeta       Status = "beta"
	StatusGA         Status = "ga"
	StatusDeprecated Status = "deprecated"
	StatusRemoved    Status = "removed"
)

// Disposition is the open-core home of a capability, per ADR-004 Decision 1 as
// refined by ADR-005 Decision 12. Every capability has exactly one home.
type Disposition string

const (
	// DispositionCore ships in the Apache-2.0 local core.
	DispositionCore Disposition = "core"
	// DispositionHosted lives only in the closed-source platform.
	DispositionHosted Disposition = "hosted"
	// DispositionBoth has a core half and a hosted half across a documented
	// contract — the platform client is the canonical example.
	DispositionBoth Disposition = "both"
)

// Surface is a place a capability is exposed to a user or an agent.
type Surface string

const (
	SurfaceCLI       Surface = "cli"
	SurfaceSDK       Surface = "sdk"
	SurfaceVSCode    Surface = "vscode"
	SurfaceSkills    Surface = "skills"
	SurfaceDashboard Surface = "dashboard"
	SurfaceFlutter   Surface = "flutter"
	SurfacePlatform  Surface = "platform"
	SurfaceSite      Surface = "site"
	SurfaceCI        Surface = "ci"
)

// AllSurfaces is the canonical surface vocabulary, in matrix-column order.
// The surface matrix renders these columns for every capability, so the order
// is part of the generated output's stability.
var AllSurfaces = []Surface{
	SurfaceCLI, SurfaceSDK, SurfaceVSCode, SurfaceSkills,
	SurfaceDashboard, SurfaceFlutter, SurfacePlatform, SurfaceSite, SurfaceCI,
}

var (
	validStatus = map[Status]bool{
		StatusPlanned: true, StatusAlpha: true, StatusBeta: true,
		StatusGA: true, StatusDeprecated: true, StatusRemoved: true,
	}
	validDisposition = map[Disposition]bool{
		DispositionCore: true, DispositionHosted: true, DispositionBoth: true,
	}
	validSurface = func() map[Surface]bool {
		m := make(map[Surface]bool, len(AllSurfaces))
		for _, s := range AllSurfaces {
			m[s] = true
		}
		return m
	}()
)

// Capability is one row of the registry.
type Capability struct {
	ID          string      `yaml:"id"`
	Title       string      `yaml:"title"`
	Status      Status      `yaml:"status"`
	Disposition Disposition `yaml:"disposition"`
	Surfaces    []Surface   `yaml:"surfaces"`
	// Docs are repo-relative paths to the capability's documentation. Every
	// entry must exist; a capability documented by a file that was deleted is
	// the exact rot this registry exists to prevent.
	Docs []string `yaml:"docs"`
	// Owns are repo-relative globs naming the implementation. These are the
	// source of the file -> capability edge in the graph. A glob matching
	// nothing FAILS validation.
	Owns []string `yaml:"owns"`
	// DependsOn names other capability IDs. Every ID must resolve.
	DependsOn []string `yaml:"depends_on"`
}

// Registry is the parsed capabilities.yaml.
type Registry struct {
	SchemaVersion int          `yaml:"schema_version"`
	Capabilities  []Capability `yaml:"capabilities"`
}

// Load reads and structurally validates the registry at path. Structural
// validation covers everything checkable without touching the tree; call
// ValidateAgainstTree for the glob and doc-existence checks.
func Load(path string) (*Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("capabilities: read %s: %w", path, err)
	}

	var reg Registry
	// KnownFields makes a typo in a key an error rather than a silently
	// ignored field. A misspelled `dispositon:` that parses to the zero value
	// would otherwise fail the closed-set check with a confusing message.
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&reg); err != nil {
		return nil, fmt.Errorf("capabilities: parse %s: %w", path, err)
	}

	if reg.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf(
			"capabilities: schema_version %d is not supported (want %d)",
			reg.SchemaVersion, SchemaVersion)
	}
	if len(reg.Capabilities) == 0 {
		return nil, fmt.Errorf("capabilities: %s declares no capabilities", path)
	}
	if err := reg.validateStructure(); err != nil {
		return nil, err
	}
	return &reg, nil
}

func (r *Registry) validateStructure() error {
	seen := make(map[string]bool, len(r.Capabilities))
	ids := make(map[string]bool, len(r.Capabilities))
	for _, c := range r.Capabilities {
		ids[c.ID] = true
	}

	for i, c := range r.Capabilities {
		where := fmt.Sprintf("capabilities[%d]", i)
		if c.ID == "" {
			return fmt.Errorf("capabilities: %s has no id", where)
		}
		if seen[c.ID] {
			return fmt.Errorf("capabilities: duplicate id %q", c.ID)
		}
		seen[c.ID] = true

		if c.Title == "" {
			return fmt.Errorf("capabilities: %s has no title", c.ID)
		}
		if !validStatus[c.Status] {
			return fmt.Errorf("capabilities: %s has unknown status %q (want one of %s)",
				c.ID, c.Status, joinStatuses())
		}
		if !validDisposition[c.Disposition] {
			return fmt.Errorf("capabilities: %s has unknown disposition %q (want core|hosted|both)",
				c.ID, c.Disposition)
		}
		if len(c.Surfaces) == 0 {
			return fmt.Errorf("capabilities: %s declares no surfaces", c.ID)
		}
		for _, s := range c.Surfaces {
			if !validSurface[s] {
				return fmt.Errorf("capabilities: %s has unknown surface %q (want one of %s)",
					c.ID, s, joinSurfaces())
			}
		}
		if len(c.Docs) == 0 {
			return fmt.Errorf("capabilities: %s declares no docs", c.ID)
		}
		if len(c.Owns) == 0 && c.Status != StatusPlanned {
			return fmt.Errorf("capabilities: %s declares no owns globs "+
				"(only status:planned may own nothing)", c.ID)
		}
		for _, dep := range c.DependsOn {
			if !ids[dep] {
				return fmt.Errorf("capabilities: %s depends_on %q, which is not defined",
					c.ID, dep)
			}
			if dep == c.ID {
				return fmt.Errorf("capabilities: %s depends_on itself", c.ID)
			}
		}
	}
	return nil
}

// TreeViolation is one failed tree check, named so the caller can report every
// problem in one pass instead of one-per-run.
type TreeViolation struct {
	Capability string
	Kind       string // "missing-doc" | "empty-glob"
	Value      string
}

func (v TreeViolation) String() string {
	switch v.Kind {
	case "missing-doc":
		return fmt.Sprintf("%s: doc %q does not exist", v.Capability, v.Value)
	case "empty-glob":
		return fmt.Sprintf("%s: owns glob %q matches nothing", v.Capability, v.Value)
	default:
		return fmt.Sprintf("%s: %s %q", v.Capability, v.Kind, v.Value)
	}
}

// ValidateAgainstTree checks every doc path exists and every owns glob matches
// at least one path, rooted at root.
//
// An empty glob is a violation, not an empty result. This is the registry's
// anti-rot mechanism: it is what makes a capability whose implementation was
// deleted or relocated fail the build rather than quietly persist as a claim
// about a product that no longer exists.
func (r *Registry) ValidateAgainstTree(root string) ([]TreeViolation, error) {
	var out []TreeViolation
	for _, c := range r.Capabilities {
		for _, d := range c.Docs {
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err != nil {
				out = append(out, TreeViolation{c.ID, "missing-doc", d})
			}
		}
		for _, g := range c.Owns {
			n, err := countMatches(root, g)
			if err != nil {
				return nil, fmt.Errorf("capabilities: %s: glob %q: %w", c.ID, g, err)
			}
			if n == 0 {
				out = append(out, TreeViolation{c.ID, "empty-glob", g})
			}
		}
	}
	return out, nil
}

// countMatches counts tree entries matching a repo-relative glob.
//
// filepath.Glob does not implement `**`, and the registry leans on it heavily
// ("internal/orchestrator/**"), so a `**` suffix is handled as a prefix walk:
// the glob is satisfied by any entry beneath the literal directory. Everything
// else falls through to filepath.Glob.
func countMatches(root, pattern string) (int, error) {
	p := filepath.ToSlash(pattern)

	if strings.HasSuffix(p, "/**") {
		// The leading segment may itself contain wildcards
		// ("skills/nightgauge-feature-*/**"), so it is globbed rather than
		// joined literally. An earlier version os.Stat'd the raw pattern and
		// reported every wildcard directory as empty — caught by this
		// package's own tree check on first run.
		lead := filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(p, "/**")))
		dirs, err := filepath.Glob(lead)
		if err != nil {
			return 0, err
		}
		total := 0
		for _, d := range dirs {
			info, err := os.Stat(d)
			if err != nil || !info.IsDir() {
				continue
			}
			entries, err := os.ReadDir(d)
			if err != nil {
				return 0, err
			}
			total += len(entries)
		}
		return total, nil
	}

	// A `**` in the middle (e.g. "skills/nightgauge-feature-*/**") is split at
	// the first `**` and resolved as: expand the leading glob, then require at
	// least one entry beneath any match.
	if i := strings.Index(p, "/**/"); i >= 0 {
		leading := p[:i]
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(leading)))
		if err != nil {
			return 0, err
		}
		return len(matches), nil
	}

	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(p)))
	if err != nil {
		return 0, err
	}
	return len(matches), nil
}

// ByID returns the capability with the given id.
func (r *Registry) ByID(id string) (*Capability, bool) {
	for i := range r.Capabilities {
		if r.Capabilities[i].ID == id {
			return &r.Capabilities[i], true
		}
	}
	return nil, false
}

// HasSurface reports whether the capability is exposed on s.
func (c *Capability) HasSurface(s Surface) bool {
	for _, x := range c.Surfaces {
		if x == s {
			return true
		}
	}
	return false
}

// SurfacesWithoutCapability returns declared surfaces that no capability
// claims — a hole in the product, reported by the matrix.
func (r *Registry) SurfacesWithoutCapability() []Surface {
	used := make(map[Surface]bool)
	for _, c := range r.Capabilities {
		for _, s := range c.Surfaces {
			used[s] = true
		}
	}
	var out []Surface
	for _, s := range AllSurfaces {
		if !used[s] {
			out = append(out, s)
		}
	}
	return out
}

func joinSurfaces() string {
	parts := make([]string, 0, len(AllSurfaces))
	for _, s := range AllSurfaces {
		parts = append(parts, string(s))
	}
	return strings.Join(parts, "|")
}

func joinStatuses() string {
	parts := make([]string, 0, len(validStatus))
	for s := range validStatus {
		parts = append(parts, string(s))
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}
