package okf

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FrontmatterBlock holds parsed YAML frontmatter from a knowledge file.
// When Repos is nil or empty, the entry applies to all repositories in the workspace.
//
// The field set is the single knowledge frontmatter contract and is mirrored
// exactly by KnowledgeEntrySchema in the TypeScript SDK.
type FrontmatterBlock struct {
	// Type is the entry kind (prd, decisions, architecture, glossary,
	// runbook, adr, reference, note, ...). Required on every non-reserved
	// entry; unknown values are accepted for forward compatibility.
	Type string `yaml:"type" json:"type"`

	// Title is the human-readable entry title. Falls back to the H1 when absent.
	Title string `yaml:"title" json:"title,omitempty"`

	// Description is a one-line summary used in generated index pages.
	Description string `yaml:"description" json:"description,omitempty"`

	// Repos lists the repository names this knowledge entry applies to.
	// Nil or empty means workspace-wide (applies to all repos).
	Repos []string `yaml:"repos" json:"repos,omitempty"`

	// Tags holds optional topic tags for discovery.
	Tags []string `yaml:"tags" json:"tags,omitempty"`

	// Related holds related issue/PR references, e.g. ["#12", "#13"].
	Related []string `yaml:"related" json:"related,omitempty"`

	// Status is the lifecycle status of this knowledge entry: draft, stable
	// or deprecated. Empty means DefaultStatus. `superseded` is rejected.
	Status string `yaml:"status" json:"status,omitempty"`

	// SupersededBy holds the issue/PR reference or entry path that replaces
	// this entry (used alongside Status=deprecated).
	SupersededBy string `yaml:"superseded_by" json:"superseded_by,omitempty"`

	// Generated records the actor that produced this entry and when.
	Generated *Provenance `yaml:"generated" json:"generated,omitempty"`

	// Verified records every confirmation event on this entry, oldest first.
	Verified []Provenance `yaml:"verified" json:"verified,omitempty"`

	// Sources records the material this entry was derived from.
	Sources []Source `yaml:"sources" json:"sources,omitempty"`

	// StaleAfter is an RFC3339 timestamp past which the entry is no longer
	// treated as current guidance.
	StaleAfter string `yaml:"stale_after" json:"stale_after,omitempty"`

	// Raw holds the full parsed frontmatter as a map for forward-compatibility
	// with future frontmatter fields.
	Raw map[string]interface{} `json:"-"`
}

// EffectiveStatus returns the entry's status, substituting DefaultStatus when
// the field is absent.
func (b *FrontmatterBlock) EffectiveStatus() string {
	if b == nil || strings.TrimSpace(b.Status) == "" {
		return DefaultStatus
	}
	return b.Status
}

// ErrStatusSuperseded is returned when an entry still carries the deleted
// `superseded` status. Callers wrap it with the offending file path.
var ErrStatusSuperseded = errors.New("frontmatter: status \"superseded\" was replaced by \"deprecated\" with superseded_by")

// ParseFrontmatter extracts and parses YAML frontmatter from markdown content.
// Frontmatter must be delimited by --- on its own line at the start of the file.
// Returns nil with no error when no frontmatter is present.
// Returns an error when frontmatter is present but the YAML is malformed.
func ParseFrontmatter(content string) (*FrontmatterBlock, error) {
	content = strings.TrimLeft(content, "\r\n")

	// Frontmatter must start with --- at the very beginning of the file.
	if !strings.HasPrefix(content, "---") {
		return nil, nil
	}

	// Find the closing --- sentinel. Skip the opening --- line.
	rest := content[3:]
	// Allow optional carriage return
	rest = strings.TrimLeft(rest, "\r\n")

	// The closing --- must appear as a line by itself.
	// We search for \n--- or ---\n to find the terminator.
	closeIdx := findClosingSentinel(rest)
	if closeIdx < 0 {
		return nil, fmt.Errorf("frontmatter: missing closing '---' sentinel")
	}

	yamlContent := rest[:closeIdx]

	// Empty frontmatter block (--- \n ---) is valid — no repos declared.
	if strings.TrimSpace(yamlContent) == "" {
		return &FrontmatterBlock{}, nil
	}

	// Parse YAML into raw map for forward compatibility.
	var raw map[string]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &raw); err != nil {
		return nil, fmt.Errorf("frontmatter: malformed YAML: %w", err)
	}

	block := &FrontmatterBlock{Raw: raw}

	// Extract repos field specifically.
	if reposRaw, ok := raw["repos"]; ok && reposRaw != nil {
		switch v := reposRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: repos[%d] must be a string, got %T", i, item)
				}
				block.Repos = append(block.Repos, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: repos must be a list of strings, got %T", reposRaw)
		}
	}

	// Extract tags field.
	if tagsRaw, ok := raw["tags"]; ok && tagsRaw != nil {
		switch v := tagsRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: tags[%d] must be a string, got %T", i, item)
				}
				block.Tags = append(block.Tags, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: tags must be a list of strings, got %T", tagsRaw)
		}
	}

	// Extract related field (issue/PR references like "#12").
	if relatedRaw, ok := raw["related"]; ok && relatedRaw != nil {
		switch v := relatedRaw.(type) {
		case []interface{}:
			for i, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: related[%d] must be a string, got %T", i, item)
				}
				block.Related = append(block.Related, s)
			}
		default:
			return nil, fmt.Errorf("frontmatter: related must be a list of strings, got %T", relatedRaw)
		}
	}

	// Extract status field. Unknown values are accepted for forward
	// compatibility, but the deleted `superseded` value is rejected outright so
	// an unmigrated entry surfaces instead of silently ranking as unknown.
	if statusRaw, ok := raw["status"]; ok && statusRaw != nil {
		s, ok := statusRaw.(string)
		if !ok {
			return nil, fmt.Errorf("frontmatter: status must be a string, got %T", statusRaw)
		}
		if s == "superseded" {
			return nil, ErrStatusSuperseded
		}
		block.Status = s
	}

	// Extract superseded_by field.
	if sbRaw, ok := raw["superseded_by"]; ok && sbRaw != nil {
		s, ok := sbRaw.(string)
		if !ok {
			return nil, fmt.Errorf("frontmatter: superseded_by must be a string, got %T", sbRaw)
		}
		block.SupersededBy = s
	}

	// Scalar string fields of the OKF contract.
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"type", &block.Type},
		{"title", &block.Title},
		{"description", &block.Description},
		{"stale_after", &block.StaleAfter},
	} {
		v, ok := raw[f.key]
		if !ok || v == nil {
			continue
		}
		str, err := scalarString(v, f.key)
		if err != nil {
			return nil, err
		}
		*f.dst = str
	}

	// generated: {by, at}
	if v, ok := raw["generated"]; ok && v != nil {
		p, err := parseProvenance(v, "generated")
		if err != nil {
			return nil, err
		}
		block.Generated = &p
	}

	// verified: [{by, at}]
	if v, ok := raw["verified"]; ok && v != nil {
		items, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("frontmatter: verified must be a list, got %T", v)
		}
		for i, item := range items {
			p, err := parseProvenance(item, fmt.Sprintf("verified[%d]", i))
			if err != nil {
				return nil, err
			}
			block.Verified = append(block.Verified, p)
		}
	}

	// sources: [{resource, title?}]
	if v, ok := raw["sources"]; ok && v != nil {
		items, ok := v.([]interface{})
		if !ok {
			return nil, fmt.Errorf("frontmatter: sources must be a list, got %T", v)
		}
		for i, item := range items {
			m, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("frontmatter: sources[%d] must be a mapping, got %T", i, item)
			}
			var src Source
			if r, ok := m["resource"]; ok && r != nil {
				str, ok := r.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: sources[%d].resource must be a string, got %T", i, r)
				}
				src.Resource = str
			}
			if t, ok := m["title"]; ok && t != nil {
				str, ok := t.(string)
				if !ok {
					return nil, fmt.Errorf("frontmatter: sources[%d].title must be a string, got %T", i, t)
				}
				src.Title = str
			}
			src.Extra = extraKeys(m, "resource", "title")
			block.Sources = append(block.Sources, src)
		}
	}

	return block, nil
}

// scalarString reads a YAML scalar as a string. An unquoted RFC3339 timestamp
// is decoded by yaml.v3 as a time.Time, so a hand-written `stale_after:
// 2027-01-01T00:00:00Z` must not be rejected as the wrong type.
func scalarString(v interface{}, field string) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case time.Time:
		return t.UTC().Format(time.RFC3339), nil
	default:
		return "", fmt.Errorf("frontmatter: %s must be a string, got %T", field, v)
	}
}

// parseProvenance converts a `{by, at}` mapping into a Provenance. The actor is
// not validated here: parsing tolerates what a foreign producer wrote, while
// every value this binary writes goes through ValidateActor first.
func parseProvenance(v interface{}, field string) (Provenance, error) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return Provenance{}, fmt.Errorf("frontmatter: %s must be a mapping with by/at, got %T", field, v)
	}
	var p Provenance
	if by, ok := m["by"]; ok && by != nil {
		s, ok := by.(string)
		if !ok {
			return Provenance{}, fmt.Errorf("frontmatter: %s.by must be a string, got %T", field, by)
		}
		p.By = s
	}
	if at, ok := m["at"]; ok && at != nil {
		// yaml.v3 auto-converts unquoted RFC3339 scalars to time.Time.
		s, err := scalarString(at, field+".at")
		if err != nil {
			return Provenance{}, err
		}
		p.At = s
	}
	p.Extra = extraKeys(m, "by", "at")
	return p, nil
}

// extraKeys returns the entries of m whose keys are not part of the contract,
// so a stamp can render them back instead of deleting a foreign producer's
// metadata. Returns nil when there are none.
func extraKeys(m map[string]interface{}, known ...string) map[string]interface{} {
	var extra map[string]interface{}
	for k, v := range m {
		if slices.Contains(known, k) {
			continue
		}
		if extra == nil {
			extra = map[string]interface{}{}
		}
		extra[k] = v
	}
	return extra
}

// nestedNode encodes a struct plus its preserved unknown keys as one mapping
// node, contract fields first in declaration order, extras after in sorted
// order.
func nestedNode(known []MapItem, extra map[string]interface{}) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode}
	add := func(k string, v interface{}) error {
		val, err := encodeValue(v)
		if err != nil {
			return err
		}
		node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: k}, val)
		return nil
	}
	for _, item := range known {
		if err := add(item.Key, item.Value); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := add(k, extra[k]); err != nil {
			return nil, err
		}
	}
	return node, nil
}

// encodeValue encodes v as a YAML node, routing Provenance and Source through
// their own encoders so nested unknown keys survive the round trip.
func encodeValue(v interface{}) (*yaml.Node, error) {
	switch t := v.(type) {
	case *Provenance:
		return t.yamlNode()
	case Provenance:
		return t.yamlNode()
	case []Provenance:
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, p := range t {
			n, err := p.yamlNode()
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, n)
		}
		return seq, nil
	case []Source:
		seq := &yaml.Node{Kind: yaml.SequenceNode}
		for _, src := range t {
			n, err := src.yamlNode()
			if err != nil {
				return nil, err
			}
			seq.Content = append(seq.Content, n)
		}
		return seq, nil
	}
	node := &yaml.Node{}
	if err := node.Encode(v); err != nil {
		return nil, err
	}
	return node, nil
}

// MapItem is one ordered key/value pair used when rendering a nested mapping.
type MapItem struct {
	Key   string
	Value interface{}
}

// yamlNode renders a Provenance, preserving any unknown keys it carries.
func (p Provenance) yamlNode() (*yaml.Node, error) {
	known := []MapItem{{Key: "by", Value: p.By}}
	if p.At != "" {
		known = append(known, MapItem{Key: "at", Value: p.At})
	}
	return nestedNode(known, p.Extra)
}

// yamlNode renders a Source, preserving any unknown keys it carries.
func (s Source) yamlNode() (*yaml.Node, error) {
	known := []MapItem{{Key: "resource", Value: s.Resource}}
	if s.Title != "" {
		known = append(known, MapItem{Key: "title", Value: s.Title})
	}
	return nestedNode(known, s.Extra)
}

// ParseFrontmatterFile reads path and parses its frontmatter, wrapping any
// parse failure with the file path so the operator knows which entry to fix.
func ParseFrontmatterFile(path string) (*FrontmatterBlock, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, err := ParseFrontmatter(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return block, nil
}

// SplitFrontmatter separates a document into its raw frontmatter YAML (without
// the --- sentinels) and the body that follows. When the document carries no
// frontmatter, yamlText is empty and body is the whole document.
func SplitFrontmatter(content string) (yamlText, body string) {
	trimmed := strings.TrimLeft(content, "\r\n")
	if !strings.HasPrefix(trimmed, "---") {
		return "", content
	}
	rest := strings.TrimLeft(trimmed[3:], "\r\n")
	closeIdx := findClosingSentinel(rest)
	if closeIdx < 0 {
		return "", content
	}
	yamlText = rest[:closeIdx]
	after := rest[closeIdx:]
	// Consume the closing sentinel line itself.
	if nl := strings.IndexByte(after, '\n'); nl >= 0 {
		body = after[nl+1:]
	} else {
		body = ""
	}
	// A single blank line separates the block from the body; drop it so
	// render/split round-trips byte-for-byte.
	body = strings.TrimPrefix(body, "\n")
	return yamlText, body
}

// RenderFrontmatter serialises a block back to a `---`-delimited YAML document,
// emitting the contract's fields in a stable order and preserving any unknown
// keys the block was parsed with. The result ends with a blank line so it can
// be concatenated directly with a body.
func RenderFrontmatter(b *FrontmatterBlock) (string, error) {
	if b == nil {
		return "", nil
	}
	root := &yaml.Node{Kind: yaml.MappingNode}
	set := func(k string, v interface{}) error {
		val, err := encodeValue(v)
		if err != nil {
			return fmt.Errorf("render frontmatter: encode %s: %w", k, err)
		}
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: k},
			val,
		)
		return nil
	}

	type field struct {
		key  string
		val  interface{}
		emit bool
	}
	fields := []field{
		{"type", b.Type, b.Type != ""},
		{"title", b.Title, b.Title != ""},
		{"description", b.Description, b.Description != ""},
		{"tags", b.Tags, len(b.Tags) > 0},
		{"related", b.Related, len(b.Related) > 0},
		{"repos", b.Repos, len(b.Repos) > 0},
		{"status", b.Status, b.Status != ""},
		{"superseded_by", b.SupersededBy, b.SupersededBy != ""},
		{"generated", b.Generated, b.Generated != nil},
		{"verified", b.Verified, len(b.Verified) > 0},
		{"sources", b.Sources, len(b.Sources) > 0},
		{"stale_after", b.StaleAfter, b.StaleAfter != ""},
	}
	for _, f := range fields {
		if !f.emit {
			continue
		}
		if err := set(f.key, f.val); err != nil {
			return "", err
		}
	}

	// Preserve keys outside the contract so a foreign producer's metadata
	// survives a stamp. Ordering is deterministic.
	known := map[string]bool{
		"type": true, "title": true, "description": true, "tags": true,
		"related": true, "repos": true, "status": true, "superseded_by": true,
		"generated": true, "verified": true, "sources": true, "stale_after": true,
	}
	var extras []string
	for k := range b.Raw {
		if !known[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		if err := set(k, b.Raw[k]); err != nil {
			return "", err
		}
	}

	var buf strings.Builder
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", fmt.Errorf("render frontmatter: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render frontmatter: %w", err)
	}
	data := buf.String()
	return "---\n" + data + "---\n", nil
}

// WithFrontmatter returns body prefixed with the rendered block and one blank
// separator line.
func WithFrontmatter(b *FrontmatterBlock, body string) (string, error) {
	fm, err := RenderFrontmatter(b)
	if err != nil {
		return "", err
	}
	if fm == "" {
		return body, nil
	}
	return fm + "\n" + body, nil
}

// findClosingSentinel locates the position of the closing --- sentinel in the
// YAML body (the text after the opening --- line has been consumed).
// Returns the index of the start of the --- line, or -1 if not found.
func findClosingSentinel(body string) int {
	lines := strings.Split(body, "\n")
	pos := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "---" {
			return pos
		}
		pos += len(line) + 1 // +1 for the \n
	}
	return -1
}
