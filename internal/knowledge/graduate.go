package knowledge

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GraduateInput describes a single graduation request.
//
// DecisionsPath is the absolute or workspace-relative path to a
// per-issue decisions.md file. ADRAnchor identifies the ADR block
// inside that file (e.g. "adr-001" or "ADR-001" — matched case-
// insensitively against the `## ADR-NNN: ...` heading). DocsSection
// is the destination location in docs/ that the decision graduated
// to, expressed as `<path>#<anchor>` (e.g.
// `docs/ARCHITECTURE.md#sse-pipeline-events`).
type GraduateInput struct {
	DecisionsPath string
	ADRAnchor     string
	DocsSection   string
}

// adrHeadingRe matches `## ADR-NNN: ...` headings (case-insensitive).
var adrHeadingRe = regexp.MustCompile(`(?i)^##\s+ADR-(\d+)\s*:`)

// WriteBacklink appends a `<!-- graduated-to: <docs-section> -->`
// HTML comment immediately after the ADR heading inside the file at
// in.DecisionsPath. It is idempotent — if the same backlink is
// already present inside the same ADR block, the file is left
// unchanged and no error is returned.
//
// Returns an error when the file is missing, the ADR anchor is not
// found, or the file cannot be written.
func WriteBacklink(in GraduateInput) error {
	if strings.TrimSpace(in.DecisionsPath) == "" {
		return fmt.Errorf("decisions path is required")
	}
	if strings.TrimSpace(in.ADRAnchor) == "" {
		return fmt.Errorf("ADR anchor is required")
	}
	if strings.TrimSpace(in.DocsSection) == "" {
		return fmt.Errorf("docs section is required")
	}

	raw, err := os.ReadFile(in.DecisionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("decisions file not found: %s", in.DecisionsPath)
		}
		return fmt.Errorf("read decisions file: %w", err)
	}

	content := string(raw)
	headingLine, blockStart, blockEnd, err := findADRBlock(content, in.ADRAnchor)
	if err != nil {
		return err
	}

	backlink := FormatGraduatedToComment(in.DocsSection)
	block := content[blockStart:blockEnd]
	if strings.Contains(block, backlink) {
		return nil
	}

	// Insert the backlink on the line immediately after the heading.
	headingEnd := blockStart + len(headingLine)
	// headingLine includes the trailing newline if present.
	var sb strings.Builder
	sb.Grow(len(content) + len(backlink) + 1)
	sb.WriteString(content[:headingEnd])
	sb.WriteString(backlink)
	sb.WriteString("\n")
	sb.WriteString(content[headingEnd:])

	if err := os.WriteFile(in.DecisionsPath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write decisions file: %w", err)
	}
	return nil
}

// FormatGraduatedToComment returns the HTML comment that links a
// decisions.md ADR block to its graduated docs/ destination.
func FormatGraduatedToComment(docsSection string) string {
	return fmt.Sprintf("<!-- graduated-to: %s -->", strings.TrimSpace(docsSection))
}

// FormatGraduatedFromComment returns the HTML comment that the human
// pastes into the destination docs/ file to record where the
// distilled content came from.
func FormatGraduatedFromComment(decisionsPath, adrAnchor string) string {
	anchor := normalizeADRAnchor(adrAnchor)
	return fmt.Sprintf("<!-- graduated-from: %s#%s -->", strings.TrimSpace(decisionsPath), anchor)
}

// ADRBlock is one parsed ADR section from a decisions.md file. It carries
// both the raw block body and the parsed field values used by graduation
// scoring. Field bodies preserve internal whitespace but trim leading and
// trailing whitespace so callers can length-check without normalizing again.
type ADRBlock struct {
	Index        int    // parsed from "## ADR-NNN:"
	Title        string // text after "## ADR-NNN: "
	Body         string // full block text starting at the heading line
	Status       string // value of "**Status**:" line
	Context      string // value of "**Context**:" body
	Decision     string // value of "**Decision**:" body
	Consequences string // value of "**Consequences**:" body
	Graduated    bool   // <!-- graduated-to: ... --> present in body
	Tags         string // value of optional "**Tags**:" line (soft extension)
}

// EnumerateADRBlocks parses every ADR block in the file at decisionsPath. The
// walker mirrors findADRBlock but returns all blocks rather than a single
// anchor. Malformed headings are skipped. Returns an empty slice (not nil) when
// the file exists but contains no ADR headings.
func EnumerateADRBlocks(decisionsPath string) ([]ADRBlock, error) {
	raw, err := os.ReadFile(decisionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("decisions file not found: %s", decisionsPath)
		}
		return nil, fmt.Errorf("read decisions file: %w", err)
	}
	return parseADRBlocks(string(raw)), nil
}

// parseADRBlocks performs a single pass over the decisions.md content and
// returns one ADRBlock per `## ADR-NNN: Title` heading.
func parseADRBlocks(content string) []ADRBlock {
	blocks := []ADRBlock{}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	type span struct {
		index int
		title string
		start int
		end   int
	}
	var spans []span
	current := -1
	pos := 0
	for scanner.Scan() {
		line := scanner.Text()
		lineWithNL := line + "\n"
		if m := adrHeadingRe.FindStringSubmatch(line); m != nil {
			num, err := parseADRNumber(m[1])
			if err == nil {
				if current >= 0 {
					spans[current].end = pos
				}
				title := strings.TrimSpace(stripADRHeadingPrefix(line))
				spans = append(spans, span{index: num, title: title, start: pos, end: -1})
				current = len(spans) - 1
			}
		} else if current >= 0 && strings.HasPrefix(line, "## ") {
			spans[current].end = pos
			current = -1
		}
		pos += len(lineWithNL)
	}
	if current >= 0 {
		spans[current].end = pos
	}

	for _, s := range spans {
		end := s.end
		if end < 0 || end > len(content) {
			end = len(content)
		}
		body := content[s.start:end]
		block := ADRBlock{
			Index:     s.index,
			Title:     s.title,
			Body:      body,
			Graduated: strings.Contains(body, "<!-- graduated-to:"),
		}
		extractADRFields(body, &block)
		blocks = append(blocks, block)
	}
	return blocks
}

// adrFieldRe matches the leading `**FieldName**:` (or `**FieldName:**`) marker
// at the start of a line. Captures the field name (case-folded by the caller)
// and the inline value following the colon on the same line.
var adrFieldRe = regexp.MustCompile(`(?m)^\*\*([A-Za-z][A-Za-z ]*)\*\*\s*:\s*(.*)$`)

// extractADRFields walks the ADR body and populates the parsed field values
// on block. A field's body runs from its marker line to the next field
// marker, the next `## ` heading, or end of block.
func extractADRFields(body string, block *ADRBlock) {
	indices := adrFieldRe.FindAllStringSubmatchIndex(body, -1)
	if len(indices) == 0 {
		return
	}
	type match struct {
		name  string
		inline string
		start int
		end   int
	}
	matches := make([]match, 0, len(indices))
	for i, idx := range indices {
		name := strings.ToLower(strings.TrimSpace(body[idx[2]:idx[3]]))
		inline := strings.TrimSpace(body[idx[4]:idx[5]])
		end := len(body)
		if i+1 < len(indices) {
			end = indices[i+1][0]
		}
		matches = append(matches, match{name: name, inline: inline, start: idx[1], end: end})
	}
	for _, m := range matches {
		value := strings.TrimSpace(m.inline)
		remainder := strings.TrimSpace(body[m.start:m.end])
		if remainder != "" {
			if value != "" {
				value = value + "\n" + remainder
			} else {
				value = remainder
			}
		}
		value = strings.TrimSpace(value)
		switch m.name {
		case "status":
			block.Status = value
		case "context":
			block.Context = value
		case "decision":
			block.Decision = value
		case "consequences":
			block.Consequences = value
		case "tags":
			block.Tags = value
		}
	}
}

// stripADRHeadingPrefix removes the leading `## ADR-NNN:` prefix from a
// heading line and returns the trailing title text.
func stripADRHeadingPrefix(line string) string {
	loc := adrHeadingRe.FindStringIndex(line)
	if loc == nil {
		return strings.TrimSpace(line)
	}
	return strings.TrimSpace(line[loc[1]:])
}

// ReadADRBlock returns the raw markdown of the ADR block identified
// by anchor inside the file at decisionsPath. The block runs from
// the matching `## ADR-NNN:` heading up to (but not including) the
// next `## ` heading or end of file.
func ReadADRBlock(decisionsPath, anchor string) (string, error) {
	raw, err := os.ReadFile(decisionsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("decisions file not found: %s", decisionsPath)
		}
		return "", fmt.Errorf("read decisions file: %w", err)
	}
	_, start, end, err := findADRBlock(string(raw), anchor)
	if err != nil {
		return "", err
	}
	return string(raw[start:end]), nil
}

// FindDecisionsPath resolves the decisions.md path for an issue
// number by scanning .nightgauge/knowledge/{epics,features}/
// for a directory whose name starts with `{issueNumber}-`. Returns
// the workspace-relative path on success.
func FindDecisionsPath(workspaceRoot string, issueNumber int) (string, error) {
	if issueNumber <= 0 {
		return "", fmt.Errorf("issue number must be positive")
	}
	root := filepath.Join(workspaceRoot, ".nightgauge", "knowledge")
	prefix := fmt.Sprintf("%d-", issueNumber)
	for _, category := range []string{"features", "epics"} {
		catDir := filepath.Join(root, category)
		entries, err := os.ReadDir(catDir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", catDir, err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			candidate := filepath.Join(catDir, e.Name(), "decisions.md")
			if _, err := os.Stat(candidate); err == nil {
				rel, relErr := filepath.Rel(workspaceRoot, candidate)
				if relErr != nil {
					return candidate, nil
				}
				return rel, nil
			}
		}
	}
	return "", fmt.Errorf("no decisions.md found for issue #%d under %s", issueNumber, filepath.Join(".nightgauge", "knowledge"))
}

// findADRBlock locates the ADR heading line whose number matches the
// given anchor. Returns the heading line text (with trailing
// newline), the byte offset where the heading starts, and the byte
// offset where the block ends (exclusive — either the start of the
// next `## ` heading or len(content)).
func findADRBlock(content, anchor string) (string, int, int, error) {
	wantNum, err := parseADRNumber(anchor)
	if err != nil {
		return "", 0, 0, err
	}

	scanner := bufio.NewScanner(strings.NewReader(content))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	pos := 0
	headingStart := -1
	var headingLine string
	for scanner.Scan() {
		line := scanner.Text()
		lineWithNL := line + "\n"
		if headingStart >= 0 {
			if strings.HasPrefix(line, "## ") {
				return headingLine, headingStart, pos, nil
			}
		} else if m := adrHeadingRe.FindStringSubmatch(line); m != nil {
			if num, _ := parseADRNumber(m[1]); num == wantNum {
				headingStart = pos
				headingLine = lineWithNL
			}
		}
		pos += len(lineWithNL)
	}
	if err := scanner.Err(); err != nil {
		return "", 0, 0, fmt.Errorf("scan decisions file: %w", err)
	}
	if headingStart < 0 {
		return "", 0, 0, fmt.Errorf("ADR anchor %q not found", anchor)
	}
	return headingLine, headingStart, len(content), nil
}

// parseADRNumber accepts forms like "1", "001", "ADR-001",
// "adr-1" and returns the integer ADR number.
func parseADRNumber(anchor string) (int, error) {
	s := strings.TrimSpace(anchor)
	s = strings.TrimPrefix(strings.ToLower(s), "adr-")
	s = strings.TrimLeft(s, "0")
	if s == "" {
		s = "0"
	}
	n := 0
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid ADR anchor %q: expected ADR-NNN or NNN", anchor)
	}
	return n, nil
}

// normalizeADRAnchor returns a lowercase `adr-NNN` form used in
// graduated-from comments, regardless of input casing.
func normalizeADRAnchor(anchor string) string {
	n, err := parseADRNumber(anchor)
	if err != nil {
		return strings.TrimSpace(anchor)
	}
	return fmt.Sprintf("adr-%03d", n)
}
