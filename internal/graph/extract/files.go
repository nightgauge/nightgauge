package extract

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/graph"
)

// Files extracts one file node per tracked path, and a part-of edge to the
// package (top-level directory) that contains it.
//
// The source is `git ls-files`, not a filesystem walk, and that is deliberate:
// the graph should describe the repository, not the working tree. A walk picks
// up build output, node_modules, and every gitignored artifact, which would
// bury the real tree in noise and make the Phase 2 noise budget unmeetable
// before it is even written.
//
// SourceLine is 0 throughout: git's file list has no line granularity, and
// inventing one would be a fabricated provenance — worse than an honest zero.
func Files(root string) Result {
	res := Result{Extractor: "files", Graph: graph.New()}

	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		res.Skipped = "git ls-files: " + err.Error()
		return res
	}

	prov := graph.Provenance{Extractor: res.Extractor, Source: "git ls-files"}
	seenPkg := map[string]graph.NodeID{}

	for _, raw := range strings.Split(string(out), "\x00") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		p = filepath.ToSlash(p)
		id, err := addNode(res.Graph, graph.NodeFile, p, prov, "", nil)
		if err != nil {
			res.Skipped = err.Error()
			return res
		}

		pkg, _, nested := strings.Cut(p, "/")
		if !nested {
			continue // a root-level file belongs to no package
		}
		pkgID, ok := seenPkg[pkg]
		if !ok {
			pkgID, err = addNode(res.Graph, graph.NodePackage, pkg, prov, "", nil)
			if err != nil {
				res.Skipped = err.Error()
				return res
			}
			seenPkg[pkg] = pkgID
		}
		if err := addEdge(res.Graph, graph.EdgePartOf, id, pkgID, prov, nil); err != nil {
			res.Skipped = err.Error()
			return res
		}
	}
	return res
}
