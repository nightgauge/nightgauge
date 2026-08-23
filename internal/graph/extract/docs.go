package extract

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/graph"
)

var (
	docH1Re    = regexp.MustCompile(`^#\s+(.+?)\s*$`)
	wikiLinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|[^\]]*)?\]\]`)
	mdLinkRe   = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
)

// Docs extracts one doc node per tracked markdown file, plus a documents edge
// for every link that points at another file in the tree.
//
// This generalises internal/knowledge/index.go's scope from the knowledge base
// to the whole tree, as ADR-005 Decision 3 asks. It does not CALL that package:
// its index is a persisted artifact with its own schema, lifecycle and 5,000-
// entry cap, built for backlink queries over the KB. Reusing the shape (H1 as
// title, wiki-links as backlinks) while emitting graph nodes is the reuse that
// was actually wanted; sharing the store would couple two artifacts with
// different rebuild triggers.
//
// External links (http, mailto) are skipped rather than emitted as dangling —
// they are outside the graph's universe, so reporting them as unresolvable
// would be noise, not a finding. A link to a tree path that does not exist IS
// emitted, because that is the reference rot the program gates on.
func Docs(root string) Result {
	res := Result{Extractor: "docs", Graph: graph.New()}

	cmd := exec.Command("git", "ls-files", "-z", "*.md")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		res.Skipped = "git ls-files *.md: " + err.Error()
		return res
	}

	// The full tracked list is needed to decide what a link TARGETS. A link to
	// Makefile or .github/workflows/lint.yml is a reference to a FILE, not to a
	// doc; emitting it as a doc node manufactured a dangling finding for every
	// such link on the first real run. Directories are resolved too, so that a
	// navigation link to an existing directory is not reported as rot.
	tracked, err := trackedFiles(root)
	if err != nil {
		res.Skipped = "git ls-files: " + err.Error()
		return res
	}
	trackedSet := make(map[string]bool, len(tracked))
	dirSet := map[string]bool{}
	for _, f := range tracked {
		trackedSet[f] = true
		for d := filepath.ToSlash(filepath.Dir(f)); d != "." && d != "/"; d = filepath.ToSlash(filepath.Dir(d)) {
			dirSet[d] = true
		}
	}

	for _, raw := range strings.Split(string(out), "\x00") {
		rel := strings.TrimSpace(raw)
		if rel == "" {
			continue
		}
		rel = filepath.ToSlash(rel)
		title, links := readDoc(filepath.Join(root, rel))

		prov := graph.Provenance{Extractor: res.Extractor, Source: rel, SourceLine: title.line}
		id, err := addNode(res.Graph, graph.NodeDoc, rel, prov, title.text, nil)
		if err != nil {
			res.Skipped = err.Error()
			return res
		}
		for _, l := range links {
			target := resolveDocLink(rel, l.target)
			if target == "" {
				continue
			}
			// A link to an existing DIRECTORY resolves; it is navigation, not
			// a file reference, and there is no directory node kind to point
			// at. Reporting it would be noise the noise budget cannot afford.
			if dirSet[target] {
				continue
			}
			// Markdown targets become doc nodes; anything else becomes a file
			// node, which is what the Files extractor emits. Getting this wrong
			// is what produced the fake findings.
			targetKind := graph.NodeFile
			if strings.HasSuffix(strings.ToLower(target), ".md") {
				targetKind = graph.NodeDoc
			}
			lProv := graph.Provenance{Extractor: res.Extractor, Source: rel, SourceLine: l.line}
			if err := addEdge(res.Graph, graph.EdgeDocuments, id,
				graph.MakeNodeID(targetKind, target), lProv,
				graph.Attrs{"link_kind": l.kind}); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}
	return res
}

type docLink struct {
	target string
	kind   string
	line   int
}

func readDoc(path string) (adrTitle, []docLink) {
	var title adrTitle
	var links []docLink

	f, err := os.Open(path)
	if err != nil {
		return title, links
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	inFence := false
	for sc.Scan() {
		n++
		line := sc.Text()
		// Links inside a fenced code block are illustrations, not references.
		// Counting them would put every doc that shows an example path into the
		// dangling report forever.
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if title.text == "" {
			if m := docH1Re.FindStringSubmatch(line); m != nil {
				title = adrTitle{text: strings.TrimSpace(m[1]), line: n}
			}
		}
		for _, m := range wikiLinkRe.FindAllStringSubmatch(line, -1) {
			links = append(links, docLink{target: strings.TrimSpace(m[1]), kind: "wiki", line: n})
		}
		for _, m := range mdLinkRe.FindAllStringSubmatch(line, -1) {
			links = append(links, docLink{target: strings.TrimSpace(m[1]), kind: "markdown", line: n})
		}
	}
	return title, links
}

// resolveDocLink turns a link as written into a tree-relative path, or "" when
// the link is not a reference to a file in this tree.
func resolveDocLink(from, target string) string {
	if target == "" || strings.HasPrefix(target, "#") {
		return "" // a same-page anchor is not a reference to another file
	}
	lower := strings.ToLower(target)
	for _, scheme := range []string{"http://", "https://", "mailto:", "tel:", "//"} {
		if strings.HasPrefix(lower, scheme) {
			return ""
		}
	}
	// Drop any fragment or query; the file is the referent.
	if i := strings.IndexAny(target, "#?"); i >= 0 {
		target = target[:i]
	}
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "/") {
		return filepath.ToSlash(filepath.Clean(strings.TrimPrefix(target, "/")))
	}
	joined := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.Dir(from), target)))
	// A path that climbs out of the repository is not a tree reference.
	if strings.HasPrefix(joined, "..") {
		return ""
	}
	return joined
}
