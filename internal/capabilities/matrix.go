package capabilities

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// GeneratedHeader marks docs/CAPABILITIES_MAP.md as generated output. A reader
// who edits the file by hand must be told, in the file, that the edit will be
// overwritten -- and CI must be able to find the marker.
const GeneratedHeader = "<!-- GENERATED FROM capabilities.yaml -- DO NOT EDIT. Run `nightgauge capabilities matrix --write`. -->"

// cellFor renders one capability x surface cell.
//
// Every cell has an explicit value. A BLANK cell is unexpressed data
// masquerading as a negative -- the failure mode #578 names in the model
// registry's transports map, where a missing key had to be distinguished from
// an explicit served=false. The matrix makes the same distinction visible:
// "—" means the capability is not on this surface, and a status word means it
// is, at that maturity.
func cellFor(c *Capability, s Surface) string {
	if !c.HasSurface(s) {
		return "—"
	}
	switch c.Status {
	case StatusGA:
		return "✓"
	case StatusRemoved:
		return "removed"
	default:
		return string(c.Status)
	}
}

// MatrixJSON is the programmatic form of the surface matrix.
type MatrixJSON struct {
	SchemaVersion int                 `json:"schema_version"`
	Surfaces      []string            `json:"surfaces"`
	Rows          []MatrixRow         `json:"rows"`
	Holes         MatrixHoles         `json:"holes"`
	Counts        map[string]int      `json:"counts_by_status"`
	Dispositions  map[string]int      `json:"counts_by_disposition"`
	Dependencies  map[string][]string `json:"dependencies"`
}

// MatrixRow is one capability's row.
type MatrixRow struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Status      string            `json:"status"`
	Disposition string            `json:"disposition"`
	Docs        []string          `json:"docs"`
	Cells       map[string]string `json:"cells"`
}

// MatrixHoles are the two questions the matrix exists to answer.
type MatrixHoles struct {
	// CapabilitiesWithoutSurface should always be empty -- the loader refuses
	// a capability with no surfaces -- but it is reported rather than assumed.
	CapabilitiesWithoutSurface []string `json:"capabilities_without_surface"`
	// SurfacesWithoutCapability is the real signal: a surface no capability
	// claims is a product surface nothing is accountable for.
	SurfacesWithoutCapability []string `json:"surfaces_without_capability"`
}

// BuildMatrix computes the matrix data.
func (r *Registry) BuildMatrix() MatrixJSON {
	m := MatrixJSON{
		SchemaVersion: SchemaVersion,
		Counts:        map[string]int{},
		Dispositions:  map[string]int{},
		Dependencies:  map[string][]string{},
	}
	for _, s := range AllSurfaces {
		m.Surfaces = append(m.Surfaces, string(s))
	}
	for i := range r.Capabilities {
		c := &r.Capabilities[i]
		row := MatrixRow{
			ID:          c.ID,
			Title:       c.Title,
			Status:      string(c.Status),
			Disposition: string(c.Disposition),
			Docs:        c.Docs,
			Cells:       map[string]string{},
		}
		for _, s := range AllSurfaces {
			row.Cells[string(s)] = cellFor(c, s)
		}
		m.Rows = append(m.Rows, row)
		m.Counts[string(c.Status)]++
		m.Dispositions[string(c.Disposition)]++
		if len(c.DependsOn) > 0 {
			m.Dependencies[c.ID] = c.DependsOn
		}
		if len(c.Surfaces) == 0 {
			m.Holes.CapabilitiesWithoutSurface = append(m.Holes.CapabilitiesWithoutSurface, c.ID)
		}
	}
	for _, s := range r.SurfacesWithoutCapability() {
		m.Holes.SurfacesWithoutCapability = append(m.Holes.SurfacesWithoutCapability, string(s))
	}
	return m
}

// RenderJSON returns the matrix as indented JSON.
func (r *Registry) RenderJSON() ([]byte, error) {
	b, err := json.MarshalIndent(r.BuildMatrix(), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// RenderMarkdown returns docs/CAPABILITIES_MAP.md.
//
// Tables are emitted column-padded so the output is already Prettier-formatted;
// the repository runs Prettier over Markdown in CI, and a generator whose output
// Prettier then rewrites can never be byte-compared for currency.
func (r *Registry) RenderMarkdown() string {
	m := r.BuildMatrix()
	var b strings.Builder

	b.WriteString(GeneratedHeader + "\n\n")
	b.WriteString("# Capabilities Map\n\n")
	b.WriteString("Every Nightgauge capability, the surfaces it is exposed on, its open-core\n")
	b.WriteString("home, and its documentation. Generated from `capabilities.yaml`, which is the\n")
	b.WriteString("one hand-authored layer of the workspace knowledge graph (ADR-005).\n\n")
	b.WriteString("Cell values are explicit by design: `✓` is generally available on that\n")
	b.WriteString("surface, a status word is that surface at that maturity, and `—` means the\n")
	b.WriteString("capability is not exposed there. **A blank cell would be unexpressed data\n")
	b.WriteString("posing as a negative, so the generator never emits one.**\n\n")

	// --- surface matrix ---
	head := []string{"Capability", "Status", "Home"}
	for _, s := range m.Surfaces {
		head = append(head, s)
	}
	rows := [][]string{}
	for _, row := range m.Rows {
		cells := []string{row.Title, row.Status, row.Disposition}
		for _, s := range m.Surfaces {
			cells = append(cells, row.Cells[s])
		}
		rows = append(rows, cells)
	}
	b.WriteString("## Capability × surface\n\n")
	b.WriteString(renderTable(head, rows))

	// --- documentation ---
	b.WriteString("\n## Documentation\n\n")
	drows := [][]string{}
	for _, row := range m.Rows {
		links := make([]string, 0, len(row.Docs))
		for _, d := range row.Docs {
			links = append(links, fmt.Sprintf("[%s](%s)", shortDoc(d), relFromDocs(d)))
		}
		drows = append(drows, []string{"`" + row.ID + "`", strings.Join(links, ", ")})
	}
	b.WriteString(renderTable([]string{"Capability", "Docs"}, drows))

	// --- dependencies ---
	if len(m.Dependencies) > 0 {
		b.WriteString("\n## Dependencies\n\n")
		ids := make([]string, 0, len(m.Dependencies))
		for id := range m.Dependencies {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		deprows := [][]string{}
		for _, id := range ids {
			quoted := make([]string, 0, len(m.Dependencies[id]))
			for _, d := range m.Dependencies[id] {
				quoted = append(quoted, "`"+d+"`")
			}
			deprows = append(deprows, []string{"`" + id + "`", strings.Join(quoted, ", ")})
		}
		b.WriteString(renderTable([]string{"Capability", "Depends on"}, deprows))
	}

	// --- totals and holes ---
	b.WriteString("\n## Totals\n\n")
	b.WriteString(fmt.Sprintf("%d capabilities.\n\n", len(m.Rows)))
	b.WriteString(renderTable(
		[]string{"Status", "Count"},
		countRows(m.Counts)))
	b.WriteString("\n")
	b.WriteString(renderTable(
		[]string{"Home", "Count"},
		countRows(m.Dispositions)))

	b.WriteString("\n## Holes\n\n")
	if len(m.Holes.SurfacesWithoutCapability) == 0 {
		b.WriteString("Every declared surface is claimed by at least one capability.\n")
	} else {
		b.WriteString("**Surfaces no capability claims:** ")
		q := make([]string, 0, len(m.Holes.SurfacesWithoutCapability))
		for _, s := range m.Holes.SurfacesWithoutCapability {
			q = append(q, "`"+s+"`")
		}
		b.WriteString(strings.Join(q, ", "))
		b.WriteString(".\n\nThese are product surfaces no capability in this registry is accountable\nfor. In the core repository that is expected for sibling-repo surfaces until\nthe registry is workspace-scoped; it is a defect for any surface this\nrepository owns.\n")
	}
	if len(m.Holes.CapabilitiesWithoutSurface) > 0 {
		b.WriteString("\n**Capabilities with no surface:** ")
		b.WriteString(strings.Join(m.Holes.CapabilitiesWithoutSurface, ", "))
		b.WriteString("\n")
	}
	return b.String()
}

func countRows(m map[string]int) [][]string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, []string{"`" + k + "`", fmt.Sprint(m[k])})
	}
	return out
}

func shortDoc(p string) string {
	i := strings.LastIndex(p, "/")
	return strings.TrimSuffix(p[i+1:], ".md")
}

// relFromDocs rewrites a repo-relative doc path to be relative to docs/, since
// the rendered file lives there.
func relFromDocs(p string) string {
	return strings.TrimPrefix(p, "docs/")
}

// renderTable emits a Prettier-compatible GFM table: every cell padded to the
// widest cell in its column, measured in runes (the matrix uses ✓ and —).
func renderTable(head []string, rows [][]string) string {
	// Prettier pads every column to at least three characters, because a GFM
	// delimiter row conventionally carries three dashes. A two-character
	// column header ("ci") is the case that exposed this -- the generator
	// emitted "--" and Prettier rewrote it to "---", which would have made the
	// currency check permanently red.
	const minColWidth = 3

	w := make([]int, len(head))
	for i, h := range head {
		w[i] = utf8.RuneCountInString(h)
		if w[i] < minColWidth {
			w[i] = minColWidth
		}
	}
	for _, r := range rows {
		for i, c := range r {
			if n := utf8.RuneCountInString(c); n > w[i] {
				w[i] = n
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string) {
		b.WriteString("|")
		for i, c := range cells {
			b.WriteString(" " + c + strings.Repeat(" ", w[i]-utf8.RuneCountInString(c)) + " |")
		}
		b.WriteString("\n")
	}
	writeRow(head)
	b.WriteString("|")
	for i := range head {
		b.WriteString(" " + strings.Repeat("-", w[i]) + " |")
	}
	b.WriteString("\n")
	for _, r := range rows {
		writeRow(r)
	}
	return b.String()
}
