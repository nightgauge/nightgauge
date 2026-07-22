package depgraph

import "sort"

// ComputeWaves performs Kahn's algorithm topological sort on the DAG.
// Returns waves (groups of issues that can execute in parallel) and any
// detected cycles. Edges point from dependent (From) to dependency (To):
// "From is blocked by To", meaning To must complete before From.
//
// The dependency direction is: From depends on To, so To must be scheduled
// first. Adjacency is From→To, meaning each node's successors (nodes it
// blocks) are found via the reverse adjacency.
func ComputeWaves(g *Graph) (waves [][]NodeID, cycles [][]NodeID) {
	if len(g.Nodes) == 0 {
		return nil, nil
	}

	// Build in-degree: for each node, count how many other nodes it depends on
	// (i.e. how many edges have this node as From).
	inDegree := make(map[string]int)
	for key := range g.Nodes {
		inDegree[key] = 0
	}

	// adj: dependency → dependents (To → []From)
	// An edge From→To means To blocks From, so after To completes, From's
	// in-degree decreases.
	adj := make(map[string][]string) // To → []From (who does To unblock)
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		toKey := g.NodeKey(e.To)
		// Only count edges where both endpoints exist in the graph
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		if _, ok := g.Nodes[toKey]; !ok {
			continue
		}
		inDegree[fromKey]++
		adj[toKey] = append(adj[toKey], fromKey)
	}

	assigned := make(map[string]bool)
	totalNodes := len(g.Nodes)
	totalAssigned := 0

	for totalAssigned < totalNodes {
		// Collect all nodes with in-degree 0 that haven't been assigned
		var frontier []string
		for key := range g.Nodes {
			if !assigned[key] && inDegree[key] == 0 {
				frontier = append(frontier, key)
			}
		}

		if len(frontier) == 0 {
			// Cycle detected — collect all unassigned nodes as a cycle
			var cycleNodes []NodeID
			for key := range g.Nodes {
				if !assigned[key] {
					cycleNodes = append(cycleNodes, g.Nodes[key].ID())
				}
			}
			sort.Slice(cycleNodes, func(i, j int) bool {
				return cycleNodes[i].String() < cycleNodes[j].String()
			})
			cycles = append(cycles, cycleNodes)

			// Force-break: pick the node with highest in-degree and reset it
			maxDeg := -1
			var maxKey string
			for key := range g.Nodes {
				if !assigned[key] && inDegree[key] > maxDeg {
					maxDeg = inDegree[key]
					maxKey = key
				}
			}
			if maxKey != "" {
				inDegree[maxKey] = 0
			}
			continue // re-enter the loop to pick up the freed node
		}

		// Sort frontier for deterministic output
		sort.Strings(frontier)

		// Record this wave
		var wave []NodeID
		for _, key := range frontier {
			wave = append(wave, g.Nodes[key].ID())
			assigned[key] = true
			totalAssigned++

			// Decrease in-degree for dependents
			for _, depKey := range adj[key] {
				inDegree[depKey]--
			}
		}

		waves = append(waves, wave)
	}

	return waves, cycles
}

// ComputeCriticalPath finds the longest weighted path in the DAG using
// dynamic programming on the topological order. Weight = issue size
// (XS=1, S=2, M=3, L=5, XL=8).
//
// The critical path represents the sequence of issues that determines
// the minimum total execution time (assuming unlimited parallelism).
func ComputeCriticalPath(g *Graph) []NodeID {
	if len(g.Nodes) == 0 {
		return nil
	}

	// Build adjacency: From depends on To, so the "execution order" DAG
	// has edges from To → From (dependency completes, then dependent starts).
	// For longest path we traverse in execution order.
	//
	// Forward adj: To → []From (what does completing To unlock?)
	fwd := make(map[string][]string)
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		toKey := g.NodeKey(e.To)
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		if _, ok := g.Nodes[toKey]; !ok {
			continue
		}
		fwd[toKey] = append(fwd[toKey], fromKey)
	}

	// In-degree for topological sort (in execution order: edges are To→From)
	inDeg := make(map[string]int)
	for key := range g.Nodes {
		inDeg[key] = 0
	}
	for _, e := range g.Edges {
		fromKey := g.NodeKey(e.From)
		toKey := g.NodeKey(e.To)
		if _, ok := g.Nodes[fromKey]; !ok {
			continue
		}
		if _, ok := g.Nodes[toKey]; !ok {
			continue
		}
		// In execution order, From has an incoming edge from To
		inDeg[fromKey]++
	}

	// Topological order via Kahn's
	var topoOrder []string
	queue := make([]string, 0)
	for key := range g.Nodes {
		if inDeg[key] == 0 {
			queue = append(queue, key)
		}
	}
	sort.Strings(queue) // deterministic

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		topoOrder = append(topoOrder, node)

		succs := fwd[node]
		sort.Strings(succs)
		for _, s := range succs {
			inDeg[s]--
			if inDeg[s] == 0 {
				queue = append(queue, s)
			}
		}
	}

	// If there are cycles, topoOrder may not cover all nodes — that's OK,
	// we just compute the critical path over the acyclic portion.

	// DP: dist[node] = longest path weight ending at node
	dist := make(map[string]int)
	pred := make(map[string]string) // predecessor on the longest path

	for _, key := range topoOrder {
		w := g.Nodes[key].Weight
		if w == 0 {
			w = SizeWeight(g.Nodes[key].Size)
		}
		dist[key] = w // at minimum, the node's own weight
	}

	for _, key := range topoOrder {
		w := g.Nodes[key].Weight
		if w == 0 {
			w = SizeWeight(g.Nodes[key].Size)
		}

		for _, succ := range fwd[key] {
			succW := g.Nodes[succ].Weight
			if succW == 0 {
				succW = SizeWeight(g.Nodes[succ].Size)
			}
			candidate := dist[key] + succW
			if candidate > dist[succ] {
				dist[succ] = candidate
				pred[succ] = key
			}
		}
	}

	// Find the node with the maximum distance
	maxDist := 0
	var maxKey string
	// Sort keys for determinism
	sortedKeys := make([]string, 0, len(dist))
	for k := range dist {
		sortedKeys = append(sortedKeys, k)
	}
	sort.Strings(sortedKeys)

	for _, key := range sortedKeys {
		if dist[key] > maxDist {
			maxDist = dist[key]
			maxKey = key
		}
	}

	if maxKey == "" {
		return nil
	}

	// Trace back from maxKey
	var path []NodeID
	for cur := maxKey; cur != ""; cur = pred[cur] {
		path = append(path, g.Nodes[cur].ID())
	}

	// Reverse to get start → end order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}

	return path
}
