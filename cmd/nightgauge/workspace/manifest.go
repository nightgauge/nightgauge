package workspacecmd

// Workspace-manifest writer (#703).
//
// Nothing in Nightgauge could write .vscode/nightgauge-workspace.yaml before
// this file existed. Every consumer read it and every mutation was a human
// editing YAML by hand, which is why the `coverage-gap` attention card ships
// with no repair verb and the settings panel can only pick among repositories
// that already exist.
//
// WHY A LINE SPLICER AND NOT yaml.Marshal
//
// The obvious implementation — unmarshal into a struct, mutate, marshal back —
// cannot satisfy the round-trip requirement. Measured against this repository's
// own manifest, a gopkg.in/yaml.v3 Node round-trip with SetIndent(2) preserves
// comments, key order and indentation, but silently drops every blank line: 9
// of them here, one between each repository entry and each section. The very
// first write would reflow a hand-maintained file that carries load-bearing
// explanatory comments (the `project_number: 0` footgun is documented ONLY in a
// YAML comment), and an add-then-remove cycle could never return the file to a
// byte-identical state.
//
// So writes are performed as line splices against the original bytes: locate
// the target entry's physical line range using position information from the
// parsed yaml.Node, replace only those lines, and leave every other byte of the
// file untouched. yaml is used to READ structure and positions, never to
// re-emit the document.
//
// COMMENT OWNERSHIP
//
// yaml.v3 attaches a comment block to the node BELOW it. In this repository the
// four-line `project_number` block — which documents the whole list —
// is therefore owned by repositories[0], so a naive removal of that one entry
// would delete guidance meant for every entry. Removal deletes an entry's own
// body lines and never its leading comment block: the comment stays where it
// is and ends up heading whatever follows. Text is never lost; a comment may
// end up above an entry it does not describe, which is the tradeoff chosen
// deliberately over silently destroying it.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// validRoles mirrors the role enum in the extension's validateWorkspaceConfig
// (packages/nightgauge-vscode/src/utils/workspaceDetection.ts). The two
// validators must agree; a divergence is a defect, so this list is asserted
// against the TypeScript source by TestRoleEnumMatchesExtension.
var validRoles = []string{"primary", "secondary", "shared"}

// manifestEntry is one repositories[] element plus the physical line range it
// occupies in the source file.
type manifestEntry struct {
	Name          string
	Path          string
	Role          string
	ProjectNumber int

	// startLine is the 1-indexed line holding this entry's `- ` dash.
	startLine int
	// endLine is the 1-indexed last line of this entry's own body, excluding
	// any trailing blank separator.
	endLine int
	// commentStart is the 1-indexed first line of the contiguous `#` block
	// directly above this entry, or 0 when it has none. Never spliced away.
	commentStart int
}

// manifest is a parsed workspace manifest that remembers its own source text.
type manifest struct {
	path  string
	lines []string
	// trailingNewline records whether the source ended with "\n", so a splice
	// cannot silently add or remove one.
	trailingNewline bool

	entries []manifestEntry

	// seqKeyLine is the 1-indexed line of the `repositories:` key.
	seqKeyLine int
	// dashIndent is the column (0-indexed) of the `-` in a sequence item.
	dashIndent int
	// contentIndent is the column (0-indexed) of an item's mapping keys.
	contentIndent int
	// blankSeparated records whether existing entries are separated by a blank
	// line, so an appended entry matches the file's existing rhythm.
	blankSeparated bool

	// routingDefault and routingPreferred capture the routing references that
	// removal must refuse to orphan.
	routingDefault   string
	routingPreferred map[string][]string // repo name -> pattern ids referencing it
}

func manifestPath(root string) string {
	return filepath.Join(root, ".vscode", "nightgauge-workspace.yaml")
}

// loadManifest reads and parses the manifest, recording both its structure and
// the physical layout a splice needs.
func loadManifest(path string) (*manifest, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workspace manifest %s: %w", path, err)
	}
	return parseManifest(path, src)
}

func parseManifest(path string, src []byte) (*manifest, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("parse workspace manifest %s: %w", path, err)
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("workspace manifest %s is empty", path)
	}

	text := string(src)
	m := &manifest{
		path:             path,
		trailingNewline:  strings.HasSuffix(text, "\n"),
		dashIndent:       2,
		contentIndent:    4,
		routingPreferred: map[string][]string{},
	}
	m.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")

	doc := root.Content[0]
	var seq *yaml.Node
	for i := 0; i+1 < len(doc.Content); i += 2 {
		key, val := doc.Content[i], doc.Content[i+1]
		switch key.Value {
		case "repositories":
			m.seqKeyLine = key.Line
			seq = val
		case "routing":
			m.readRouting(val)
		}
	}
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("workspace manifest %s has no repositories[] sequence", path)
	}

	for _, item := range seq.Content {
		e := manifestEntry{startLine: item.Line}
		for j := 0; j+1 < len(item.Content); j += 2 {
			k, v := item.Content[j], item.Content[j+1]
			switch k.Value {
			case "name":
				e.Name = v.Value
			case "path":
				e.Path = v.Value
			case "role":
				e.Role = v.Value
			case "project_number":
				// Decode rather than Atoi so a non-integer scalar surfaces as a
				// parse error instead of a silent zero — zero is the exact value
				// this command exists to keep out of the file.
				var n int
				if err := v.Decode(&n); err != nil {
					return nil, fmt.Errorf("workspace manifest %s: repositories[%q].project_number is not an integer: %w", path, e.Name, err)
				}
				e.ProjectNumber = n
			}
		}
		if len(item.Content) >= 2 {
			m.contentIndent = item.Column - 1
			m.dashIndent = item.Column - 3
			if m.dashIndent < 0 {
				m.dashIndent = 0
			}
		}
		e.endLine = m.entryEndLine(e.startLine)
		e.commentStart = m.commentBlockStart(e.startLine)
		m.entries = append(m.entries, e)
	}

	if len(m.entries) >= 2 {
		gapStart := m.entries[0].endLine // 1-indexed last body line of entry 0
		next := m.entries[1]
		firstOwned := next.startLine
		if next.commentStart > 0 {
			firstOwned = next.commentStart
		}
		for ln := gapStart + 1; ln < firstOwned; ln++ {
			if strings.TrimSpace(m.line(ln)) == "" {
				m.blankSeparated = true
				break
			}
		}
	}

	return m, nil
}

func (m *manifest) readRouting(node *yaml.Node) {
	if node == nil || node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		switch k.Value {
		case "default_repository":
			m.routingDefault = v.Value
		case "patterns":
			if v.Kind != yaml.SequenceNode {
				continue
			}
			for _, pat := range v.Content {
				var id, preferred string
				for j := 0; j+1 < len(pat.Content); j += 2 {
					pk, pv := pat.Content[j], pat.Content[j+1]
					switch pk.Value {
					case "id":
						id = pv.Value
					case "preferred_repo":
						preferred = pv.Value
					}
				}
				if preferred != "" {
					if id == "" {
						id = "(unnamed pattern)"
					}
					m.routingPreferred[preferred] = append(m.routingPreferred[preferred], id)
				}
			}
		}
	}
}

// line returns the 1-indexed line, or "" when out of range.
func (m *manifest) line(n int) string {
	if n < 1 || n > len(m.lines) {
		return ""
	}
	return m.lines[n-1]
}

func indentOf(s string) int {
	return len(s) - len(strings.TrimLeft(s, " "))
}

// entryEndLine walks forward from a sequence item's dash line and returns the
// last line belonging to that item, excluding trailing blanks. A continuation
// line is any line indented deeper than the dash; the next item's dash, or any
// dedent out of the sequence, terminates the entry.
func (m *manifest) entryEndLine(startLine int) int {
	last := startLine
	for ln := startLine + 1; ln <= len(m.lines); ln++ {
		raw := m.line(ln)
		if strings.TrimSpace(raw) == "" {
			continue // may be interior padding; only counts if content follows
		}
		if indentOf(raw) > m.dashIndent {
			last = ln
			continue
		}
		break
	}
	return last
}

// commentBlockStart returns the first line of the contiguous `#` block directly
// above startLine, or 0 when there is none. Derived from the raw text rather
// than yaml's HeadComment attribution so the physical extent is exact.
func (m *manifest) commentBlockStart(startLine int) int {
	first := 0
	for ln := startLine - 1; ln >= 1; ln-- {
		trimmed := strings.TrimSpace(m.line(ln))
		if strings.HasPrefix(trimmed, "#") {
			first = ln
			continue
		}
		break
	}
	return first
}

func (m *manifest) find(name string) (manifestEntry, bool) {
	for _, e := range m.entries {
		if e.Name == name {
			return e, true
		}
	}
	return manifestEntry{}, false
}

// render produces the current line buffer as file bytes.
func (m *manifest) render() []byte {
	out := strings.Join(m.lines, "\n")
	if m.trailingNewline {
		out += "\n"
	}
	return []byte(out)
}

// yamlScalar emits a value the way yaml would, so a name or path needing quotes
// gets them rather than producing a file that no longer parses.
func yamlScalar(v string) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%q", v)
	}
	return strings.TrimSuffix(string(b), "\n")
}

// renderEntry formats a new repositories[] element at the file's own indent.
func (m *manifest) renderEntry(e manifestEntry) []string {
	dash := strings.Repeat(" ", m.dashIndent)
	body := strings.Repeat(" ", m.contentIndent)
	lines := []string{
		fmt.Sprintf("%s- name: %s", dash, yamlScalar(e.Name)),
		fmt.Sprintf("%spath: %s", body, yamlScalar(e.Path)),
	}
	if e.Role != "" {
		lines = append(lines, fmt.Sprintf("%srole: %s", body, yamlScalar(e.Role)))
	}
	lines = append(lines, fmt.Sprintf("%sproject_number: %d", body, e.ProjectNumber))
	return lines
}

// addEntry splices a new entry in after the last existing one.
func (m *manifest) addEntry(e manifestEntry) error {
	if _, exists := m.find(e.Name); exists {
		return fmt.Errorf("repository %q is already in the manifest — names must be unique", e.Name)
	}
	block := m.renderEntry(e)

	insertAfter := m.seqKeyLine
	if len(m.entries) > 0 {
		insertAfter = m.entries[len(m.entries)-1].endLine
	}
	if m.blankSeparated && len(m.entries) > 0 {
		block = append([]string{""}, block...)
	}

	// slices.Insert rather than a hand-rolled three-append with a
	// len(a)+len(b) capacity hint: that arithmetic is what CodeQL's
	// go/allocation-size-overflow flags, and the stdlib does the same job
	// without an size computation of our own to get wrong.
	m.lines = slices.Insert(m.lines, insertAfter, block...)
	return nil
}

// removeEntry splices out an entry's own body lines. Its leading comment block
// is deliberately retained — see the file header on comment ownership.
func (m *manifest) removeEntry(name string) (keptComment bool, err error) {
	e, ok := m.find(name)
	if !ok {
		return false, fmt.Errorf("repository %q is not in the manifest", name)
	}

	from, to := e.startLine, e.endLine

	// Absorb one blank separator so removal does not leave a widening gap.
	// Prefer the blank AFTER the entry; fall back to the one before, which is
	// the only option when removing the final entry.
	if strings.TrimSpace(m.line(to+1)) == "" && to+1 <= len(m.lines) {
		to++
	} else if from-1 >= 1 && strings.TrimSpace(m.line(from-1)) == "" && e.commentStart == 0 {
		from--
	}

	m.lines = slices.Delete(m.lines, from-1, to)

	return e.commentStart > 0, nil
}

// writeAtomic validates the rendered document and then replaces the file via a
// temp-file rename, so an interrupted write cannot truncate the manifest and an
// invalid document never reaches disk at all.
func (m *manifest) writeAtomic() error {
	data := m.render()
	if err := validateManifestBytes(data); err != nil {
		return fmt.Errorf("refusing to write: the result would be an invalid workspace manifest (original left untouched): %w", err)
	}

	dir := filepath.Dir(m.path)
	tmp, err := os.CreateTemp(dir, ".nightgauge-workspace-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	mode := os.FileMode(0o644)
	if st, statErr := os.Stat(m.path); statErr == nil {
		mode = st.Mode().Perm()
	}

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, m.path); err != nil {
		return fmt.Errorf("rename temp file over %s: %w", m.path, err)
	}
	return nil
}

// validateManifestBytes applies the same rules the readers apply. It mirrors
// validateWorkspaceConfig in the extension; the two must agree.
func validateManifestBytes(data []byte) error {
	var doc struct {
		Workspace *struct {
			Name                string `yaml:"name"`
			Description         *string
			SharedProjectNumber *int `yaml:"shared_project_number"`
		} `yaml:"workspace"`
		Repositories []struct {
			Name          string `yaml:"name"`
			Path          string `yaml:"path"`
			Role          string `yaml:"role"`
			ProjectNumber *int   `yaml:"project_number"`
		} `yaml:"repositories"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("document does not parse: %w", err)
	}

	var errs []string
	if doc.Workspace == nil {
		errs = append(errs, `workspace: required field "workspace" is missing`)
	} else if strings.TrimSpace(doc.Workspace.Name) == "" {
		errs = append(errs, `workspace.name: required and cannot be empty`)
	} else if doc.Workspace.SharedProjectNumber != nil && *doc.Workspace.SharedProjectNumber <= 0 {
		errs = append(errs, `workspace.shared_project_number: must be a positive integer`)
	}

	hasShared := doc.Workspace != nil && doc.Workspace.SharedProjectNumber != nil
	if doc.Repositories == nil {
		errs = append(errs, `repositories: required field "repositories" is missing`)
	} else if len(doc.Repositories) == 0 && !hasShared {
		errs = append(errs, `repositories: cannot be empty (or set workspace.shared_project_number)`)
	}

	seen := map[string]bool{}
	for i, r := range doc.Repositories {
		if strings.TrimSpace(r.Name) == "" {
			errs = append(errs, fmt.Sprintf("repositories[%d].name: required field is missing", i))
		} else {
			if seen[r.Name] {
				errs = append(errs, fmt.Sprintf("repositories[%d].name: duplicate repository name %q", i, r.Name))
			}
			seen[r.Name] = true
		}
		if strings.TrimSpace(r.Path) == "" {
			errs = append(errs, fmt.Sprintf("repositories[%d].path: required field is missing", i))
		}
		if r.Role != "" && !containsString(validRoles, r.Role) {
			errs = append(errs, fmt.Sprintf("repositories[%d].role: must be one of: %s", i, strings.Join(validRoles, ", ")))
		}
		// A zero project_number resolves to project 0 and silently misroutes
		// issues. Until #703 this was enforced only by a YAML comment.
		if r.ProjectNumber != nil && *r.ProjectNumber <= 0 {
			errs = append(errs, fmt.Sprintf("repositories[%d].project_number: must be a positive integer", i))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// bytesEqual is a thin alias kept for readability at call sites in tests.
func bytesEqual(a, b []byte) bool { return bytes.Equal(a, b) }
