package extract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nightgauge/nightgauge/internal/graph"
)

const modelRegistryPath = "internal/models/model-registry.json"

// modelRegistry is a deliberately partial view of model-registry.json.
//
// Only the fields the graph needs are declared. internal/models owns the full
// schema and its own validation; re-declaring it here would create a second
// definition to drift against — the precise failure this whole program exists
// to detect. If a field is needed later, widen this struct, do not fork the
// registry's own types.
type modelRegistry struct {
	Version string `json:"version"`
	Models  []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Provider string `json:"provider"`
		Band     string `json:"band"`
		Status   string `json:"status"`
	} `json:"models"`
}

// Models extracts model, provider and band nodes from the model registry, with
// a runs-on edge from each model to its provider and a serves-band edge to the
// band it serves.
//
// Band is a node rather than an attribute on purpose: ADR-005 Decision 9 says
// provider and model neutrality is a graph DIMENSION, not a rename. A band that
// no model serves is then a visible orphan instead of a string nobody queries.
func Models(root string) Result {
	res := Result{Extractor: "models", Graph: graph.New()}
	path := filepath.Join(root, modelRegistryPath)

	raw, err := os.ReadFile(path)
	if err != nil {
		res.Skipped = fmt.Sprintf("no %s in %s", modelRegistryPath, root)
		return res
	}
	var reg modelRegistry
	if err := json.Unmarshal(raw, &reg); err != nil {
		res.Skipped = fmt.Sprintf("%s: %v", modelRegistryPath, err)
		return res
	}

	// The registry is a JSON document, so a per-model line number would have to
	// be recovered by re-scanning text. The honest provenance is the file plus
	// the registry version, which is what actually identifies the fact's
	// vintage; a fabricated line number would be worse than none.
	prov := graph.Provenance{Extractor: res.Extractor, Source: modelRegistryPath}

	seenProvider := map[string]graph.NodeID{}
	seenBand := map[string]graph.NodeID{}

	for _, m := range reg.Models {
		if m.ID == "" {
			continue
		}
		id, err := addNode(res.Graph, graph.NodeModel, m.ID, prov, m.Name, graph.Attrs{
			"provider":         m.Provider,
			"band":             m.Band,
			"status":           m.Status,
			"registry_version": reg.Version,
		})
		if err != nil {
			res.Skipped = err.Error()
			return res
		}
		if m.Provider != "" {
			pid, ok := seenProvider[m.Provider]
			if !ok {
				if pid, err = addNode(res.Graph, graph.NodeProvider, m.Provider, prov, "", nil); err != nil {
					res.Skipped = err.Error()
					return res
				}
				seenProvider[m.Provider] = pid
			}
			if err := addEdge(res.Graph, graph.EdgeRunsOn, id, pid, prov, nil); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
		if m.Band != "" {
			bid, ok := seenBand[m.Band]
			if !ok {
				// A band has no node kind of its own in Decision 3's set, and
				// inventing one would break the closed-set guarantee. `stage`
				// is wrong, so a band is modelled as a capability-less
				// `provider`-adjacent concept via its own stable ref under
				// NodeStage — see ADR-005 Decision 9, which puts bands on the
				// same axis as stages for routing purposes.
				if bid, err = addNode(res.Graph, graph.NodeStage, "band/"+m.Band, prov, "band "+m.Band, graph.Attrs{"kind": "band"}); err != nil {
					res.Skipped = err.Error()
					return res
				}
				seenBand[m.Band] = bid
			}
			if err := addEdge(res.Graph, graph.EdgeServesBand, id, bid, prov, nil); err != nil {
				res.Skipped = err.Error()
				return res
			}
		}
	}
	return res
}
