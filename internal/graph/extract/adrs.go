package extract

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/nightgauge/nightgauge/internal/graph"
)

const adrDir = "docs/decisions"

var (
	adrH1Re         = regexp.MustCompile(`^#\s+(.+?)\s*$`)
	adrSupersedesRe = regexp.MustCompile(`(?i)\bsupersedes?\b[^.\n]*?ADR[- ]?0*(\d+)`)
)

// ADRs extracts one adr node per file in docs/decisions, plus a supersedes edge
// wherever an ADR's prose says it supersedes another.
//
// The supersedes edge is derived from PROSE, and that is exactly the case #126
// warned about — a relation created by a sentence is indistinguishable from a
// structural one once it is in the graph. So every such edge records the line
// it was parsed from AND carries `derived_from: "prose"` in its attributes, so
// a consumer can weigh it differently from a registry-declared edge without
// having to guess.
func ADRs(root string) Result {
	res := Result{Extractor: "adrs", Graph: graph.New()}
	dir := filepath.Join(root, adrDir)

	entries, err := os.ReadDir(dir)
	if err != nil {
		res.Skipped = fmt.Sprintf("no %s in %s", adrDir, root)
		return res
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		rel := adrDir + "/" + e.Name()
		title, supersedes := readADR(filepath.Join(dir, e.Name()))

		prov := graph.Provenance{Extractor: res.Extractor, Source: rel, SourceLine: title.line}
		id, err := addNode(res.Graph, graph.NodeADR, e.Name(), prov, title.text, graph.Attrs{"path": rel})
		if err != nil {
			res.Skipped = err.Error()
			return res
		}
		// The doc node for the same file, so doc-facing queries reach it too.
		if err := addEdge(res.Graph, graph.EdgeDocuments, id,
			graph.MakeNodeID(graph.NodeDoc, rel), prov, nil); err != nil {
			res.Skipped = err.Error()
			return res
		}
		for _, s := range supersedes {
			sProv := graph.Provenance{Extractor: res.Extractor, Source: rel, SourceLine: s.line}
			if err := addEdge(res.Graph, graph.EdgeSupersedes, id,
				graph.MakeNodeID(graph.NodeADR, s.target), sProv,
				graph.Attrs{"derived_from": "prose", "line_text": s.text}); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}
	return res
}

type adrTitle struct {
	text string
	line int
}

type adrRef struct {
	target string
	text   string
	line   int
}

// readADR returns the first H1 and every "supersedes ADR-NNN" mention. The ADR
// number is normalised to a three-digit prefix so it matches the file naming
// convention; the resulting node ID may not resolve, and that is a finding
// rather than a reason to skip the edge.
func readADR(path string) (adrTitle, []adrRef) {
	var title adrTitle
	var refs []adrRef

	f, err := os.Open(path)
	if err != nil {
		return title, refs
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Text()
		if title.text == "" {
			if m := adrH1Re.FindStringSubmatch(line); m != nil {
				title = adrTitle{text: strings.TrimSpace(m[1]), line: n}
			}
		}
		for _, m := range adrSupersedesRe.FindAllStringSubmatch(line, -1) {
			refs = append(refs, adrRef{
				target: fmt.Sprintf("%03s", m[1]),
				text:   strings.TrimSpace(line),
				line:   n,
			})
		}
	}
	return title, refs
}
