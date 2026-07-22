package teams

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DependencyConfig configures the dependency detector.
type DependencyConfig struct {
	FilePatternRegex   *regexp.Regexp
	SequentialKeywords []string
}

// targetFileExtensions is the allowlist of source-file extensions the file
// extractor recognises as predicted target files. dart is included so Flutter
// pages like journal_entry_page.dart (the #143/#144 collision class) are
// detected by the authoring-side wave planner.
const targetFileExtensions = `ts|tsx|js|jsx|mjs|cjs|py|rs|go|java|kt|swift|dart|c|cc|cpp|h|hpp|cs|rb|php|sql|sh|yaml|yml|json|toml|md|css|scss|html|vue|svelte`

// DefaultDependencyConfig returns sensible defaults.
func DefaultDependencyConfig() DependencyConfig {
	return DependencyConfig{
		// Matches both directory-qualified paths (lib/pages/journal_entry_page.dart)
		// and bare repeated filenames (journal_entry_page.dart) so Flutter and
		// other single-file target references are captured deterministically.
		FilePatternRegex: regexp.MustCompile(
			`(?:^|[\s` + "`" + `("'(])((?:[\w-]+/)*[\w.-]+\.(?:` + targetFileExtensions + `))\b`,
		),
		SequentialKeywords: []string{
			"after", "depends on", "requires", "blocked by",
			"prerequisite", "must complete first",
		},
	}
}

// markdownLinkRe matches a complete markdown link or image — `[text](dest)` /
// `![alt](dest)`. A linked file is a CITATION (evidence, prior art, a spike
// doc), not a declaration of intent to edit; counting link destinations as
// change targets is what falsely serialized epic #71's sub-issues behind one
// shared spike-doc reference (#79). The whole link is removed before
// inference — the link text is the citation's label and goes with it.
var markdownLinkRe = regexp.MustCompile(`!?\[[^\]]*\]\([^)]*\)`)

// depMetadataRe captures the YAML inside the agent-teams dependency-metadata
// comment block (issue-create Phase 2.5) so an explicit `file_ownership`
// declaration can replace prose inference entirely (#79).
var depMetadataRe = regexp.MustCompile(`(?s)<!--\s*nightgauge:dependency-metadata\s*(.*?)-->`)

// declaredTargetFiles returns the `file_ownership` list from the body's
// dependency-metadata block — trimmed, de-duplicated, first-seen order — or
// nil when the block is absent, unparseable, or declares no files.
func declaredTargetFiles(body string) []string {
	m := depMetadataRe.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var meta struct {
		FileOwnership []string `yaml:"file_ownership"`
	}
	if err := yaml.Unmarshal([]byte(m[1]), &meta); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var files []string
	for _, f := range meta.FileOwnership {
		f = strings.TrimSpace(f)
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// ExtractTargetFiles extracts predicted target-file references from issue body
// text. It is the single source of truth for file extraction shared by the
// authoring-side wave planner (PlanWavesFromIssues), the runtime wave
// orchestrator, and — via `nightgauge issue extract-targets` — the
// issue-create/issue-audit oversized-scope gates (#79).
//
// Resolution order (#79):
//  1. An explicit `file_ownership` declaration in the dependency-metadata
//     block is the author's statement of the change surface — when present it
//     wins outright and prose is never scanned, so citations cannot re-widen
//     a declared scope.
//  2. Otherwise targets are inferred from prose with markdown links stripped
//     first — a linked file is a citation, not a change target.
//
// The regex capture group strips surrounding markdown delimiters (backticks,
// quotes, parens); duplicates de-duplicate in first-seen order.
func ExtractTargetFiles(body string) []string {
	files, _ := ExtractTargetFilesDetailed(body)
	return files
}

// ExtractTargetFilesDetailed is ExtractTargetFiles plus the resolution source:
// "declared" (explicit file_ownership block) or "inferred" (prose scan with
// citations stripped). The `nightgauge issue extract-targets` command surfaces
// the source so scope-gate output can tell operators which path counted (#79).
func ExtractTargetFilesDetailed(body string) (files []string, source string) {
	if declared := declaredTargetFiles(body); len(declared) > 0 {
		return declared, "declared"
	}
	withoutCitations := markdownLinkRe.ReplaceAllString(body, " ")
	return extractFilesWith(DefaultDependencyConfig().FilePatternRegex, withoutCitations), "inferred"
}

// extractFilesWith extracts file references from body using re, returning the
// capture group (group 1) so surrounding delimiters are stripped, de-duplicated
// in first-seen order.
func extractFilesWith(re *regexp.Regexp, body string) []string {
	if re == nil {
		return nil
	}
	matches := re.FindAllStringSubmatch(body, -1)
	seen := make(map[string]bool)
	var files []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		f := strings.TrimSpace(m[1])
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		files = append(files, f)
	}
	return files
}

// DetectDependencies analyzes issues for inter-issue dependencies.
// Returns map of issue index → sorted dependency indices.
func DetectDependencies(issues []SubIssue, sources []string, config DependencyConfig) map[int][]int {
	n := len(issues)
	edges := make(map[string]bool) // "dependent:dependency" dedup

	// Heuristic 1: Shared file paths
	for j := 0; j < n; j++ {
		for i := 0; i < j; i++ {
			if sharesFiles(issues[i].Files, issues[j].Files) {
				key := depKey(j, i) // later issue depends on earlier
				edges[key] = true
			}
		}
	}

	// Heuristic 2: Import chains (source text mentions other issue's files)
	if len(sources) == n && config.FilePatternRegex != nil {
		for j := 0; j < n; j++ {
			if sources[j] == "" {
				continue
			}
			mentionedFiles := extractFilesWith(config.FilePatternRegex, sources[j])
			for i := 0; i < n; i++ {
				if i == j {
					continue
				}
				for _, mf := range mentionedFiles {
					for _, owned := range issues[i].Files {
						if mf == owned || strings.HasSuffix(mf, owned) {
							edges[depKey(j, i)] = true
						}
					}
				}
			}
		}
	}

	// Heuristic 3: Sequential keywords referencing sibling titles
	if len(sources) == n {
		for j := 0; j < n; j++ {
			lower := strings.ToLower(sources[j])
			for _, kw := range config.SequentialKeywords {
				if !strings.Contains(lower, kw) {
					continue
				}
				// Check if any sibling title is mentioned
				for i := 0; i < n; i++ {
					if i == j {
						continue
					}
					titleLower := strings.ToLower(issues[i].Title)
					if titleLower != "" && strings.Contains(lower, titleLower) {
						edges[depKey(j, i)] = true
					}
				}
			}
		}
	}

	// Convert edge set to adjacency map
	result := make(map[int][]int)
	for edge := range edges {
		parts := strings.SplitN(edge, ":", 2)
		if len(parts) != 2 {
			continue
		}
		dep := atoi(parts[0])
		on := atoi(parts[1])
		if dep >= 0 && on >= 0 {
			result[dep] = append(result[dep], on)
		}
	}

	// Sort each dependency list
	for k := range result {
		sort.Ints(result[k])
	}

	return result
}

func sharesFiles(a, b []string) bool {
	set := make(map[string]bool, len(a))
	for _, f := range a {
		set[f] = true
	}
	for _, f := range b {
		if set[f] {
			return true
		}
	}
	return false
}

func depKey(dependent, dependency int) string {
	return strings.Join([]string{itoa(dependent), itoa(dependency)}, ":")
}

func itoa(n int) string {
	s := ""
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	if neg {
		s = "-" + s
	}
	return s
}

func atoi(s string) int {
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}
