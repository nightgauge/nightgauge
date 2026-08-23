package extract

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/capabilities"
	"github.com/nightgauge/nightgauge/internal/graph"
)

const capabilitiesFile = "capabilities.yaml"

// Capabilities extracts the capability registry: one node per capability, an
// owns-file edge per `owns` glob, a documents edge per `docs` entry, and a
// part-of edge per `depends_on` ID.
//
// This is the one AUTHORED layer in the whole graph (ADR-005 Decision 2), which
// makes it the extractor whose provenance is easiest to get right and most
// important to get right: every other extractor derives, so a wrong edge is a
// parsing bug, while here a wrong edge means the registry itself is wrong and a
// human has to fix it. SourceLine therefore points at the capability's own `id:`
// line so `graph explain` lands a reader on the entry to edit.
func Capabilities(root string) Result {
	res := Result{Extractor: "capabilities", Graph: graph.New()}
	path := filepath.Join(root, capabilitiesFile)

	if _, err := os.Stat(path); err != nil {
		res.Skipped = fmt.Sprintf("no %s in %s", capabilitiesFile, root)
		return res
	}
	reg, err := capabilities.Load(path)
	if err != nil {
		res.Skipped = fmt.Sprintf("%s: %v", capabilitiesFile, err)
		return res
	}

	tracked, err := trackedFiles(root)
	if err != nil {
		res.Skipped = "git ls-files: " + err.Error()
		return res
	}

	lines := idLines(path, "id:")
	for _, c := range reg.Capabilities {
		prov := graph.Provenance{
			Extractor:  res.Extractor,
			Source:     capabilitiesFile,
			SourceLine: lines[c.ID],
		}
		surfaces := make([]string, 0, len(c.Surfaces))
		for _, s := range c.Surfaces {
			surfaces = append(surfaces, string(s))
		}
		id, err := addNode(res.Graph, graph.NodeCapability, c.ID, prov, c.Title, graph.Attrs{
			"status":      string(c.Status),
			"disposition": string(c.Disposition),
			"surfaces":    strings.Join(surfaces, ","),
		})
		if err != nil {
			res.Skipped = err.Error()
			return res
		}

		// A doc that no longer exists must still produce an edge: the dangling
		// report is the whole point (Decision 1's third corollary).
		for _, d := range c.Docs {
			if err := addEdge(res.Graph, graph.EdgeDocuments,
				graph.MakeNodeID(graph.NodeDoc, filepath.ToSlash(d)), id, prov, nil); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
		for _, glob := range c.Owns {
			// The glob is EXPANDED against the tracked file list, so the edge
			// lands on the same file nodes the Files extractor emits.
			//
			// An earlier cut pointed this edge at the glob string itself and
			// produced 100+ dangling findings on the first real run — every
			// one of them an extractor bug rather than a defect in the tree.
			// That is exactly the failure mode Phase 1.5 has to distinguish,
			// and shipping it would have poisoned the falsifier's sample with
			// noise this extractor manufactured.
			matches := matchGlob(tracked, glob)
			if len(matches) == 0 {
				// A glob owning nothing IS a real finding — the registry's own
				// tree validation fails on it too — so it stays as one
				// deliberately unresolvable edge rather than silence.
				if err := addEdge(res.Graph, graph.EdgeOwnsFile,
					id, graph.MakeNodeID(graph.NodeFile, filepath.ToSlash(glob)), prov,
					graph.Attrs{"glob": glob, "matched": "0"}); err != nil {
					res.Skipped = err.Error()
					return res
				}
				continue
			}
			for _, m := range matches {
				if err := addEdge(res.Graph, graph.EdgeOwnsFile,
					id, graph.MakeNodeID(graph.NodeFile, m), prov,
					graph.Attrs{"glob": glob}); err != nil {
					res.Skipped = err.Error()
					return res
				}
			}
		}
		for _, dep := range c.DependsOn {
			if err := addEdge(res.Graph, graph.EdgeConsumes,
				id, graph.MakeNodeID(graph.NodeCapability, dep), prov, nil); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}
	return res
}

// idLines maps a YAML list entry's value to the 1-based line it appears on, for
// keys of the form "- id: value" or "id: value".
//
// This is a line INDEX, not a YAML parse: the registry is parsed properly by
// capabilities.Load and this only recovers the position that a struct
// unmarshal throws away. Keeping the two separate means a malformed registry
// fails in the real parser with a real error, not here with a wrong line.
func idLines(path, key string) map[string]int {
	out := map[string]int{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	n := 0
	for sc.Scan() {
		n++
		t := strings.TrimSpace(sc.Text())
		t = strings.TrimPrefix(t, "- ")
		if !strings.HasPrefix(t, key) {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(t, key))
		v = strings.Trim(v, `"'`)
		if v == "" {
			continue
		}
		if _, seen := out[v]; !seen {
			out[v] = n
		}
	}
	return out
}
