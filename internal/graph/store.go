package graph

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// SchemaVersion is bumped when the on-disk format changes incompatibly.
	// Load treats a mismatch as "no graph" so the caller rebuilds from source —
	// the pattern internal/knowledge/index.go already uses. That is safe here
	// precisely because the graph is derived (ADR-005 Decision 1): discarding it
	// costs a rebuild, never data, so there is no migration path to maintain and
	// none should be added.
	SchemaVersion = 1

	// StoreDir is the workspace-relative graph directory. Gitignored: it is a
	// derived artifact, and committing it would create the second authored copy
	// the whole program exists to abolish.
	StoreDir = ".nightgauge/graph"

	nodesFile = "nodes.jsonl"
	edgesFile = "edges.jsonl"
	metaFile  = "meta.json"
)

// Meta is the store's metadata index, written alongside the JSONL files.
type Meta struct {
	SchemaVersion int    `json:"schema_version"`
	BuiltAt       string `json:"built_at"` // RFC3339, UTC
	Nodes         int    `json:"nodes"`
	Edges         int    `json:"edges"`
	Dangling      int    `json:"dangling"`
	// Extractors lists the extractors that contributed, in sorted order. It is
	// the cheap answer to "was this graph built before extractor X existed?",
	// which a schema version alone cannot express.
	Extractors []string `json:"extractors"`
}

// Save writes the graph to <root>/.nightgauge/graph atomically.
//
// Each file is written to a UNIQUE temp path and renamed. The uniqueness is the
// point: a fixed "<final>.tmp" path is atomic for a single writer and races
// only under concurrency, where the loser's rename fails with ENOENT — the
// exact shape #777 fixed in TelemetryStore.writeIndex and #786 exists to sweep
// for. It fails toward silence, which is why every test with one writer passes
// over it.
//
// A failed write cleans up its OWN temp file and no one else's.
func Save(root string, g *Graph) (Meta, error) {
	if root == "" {
		return Meta{}, fmt.Errorf("graph save: root is required")
	}
	if g == nil {
		return Meta{}, fmt.Errorf("graph save: graph is nil")
	}
	dir := filepath.Join(root, StoreDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Meta{}, fmt.Errorf("graph save: create dir: %w", err)
	}

	nodes := g.Nodes()
	edges := g.Edges()

	nodeLines := make([][]byte, 0, len(nodes))
	for _, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			return Meta{}, fmt.Errorf("graph save: marshal node %s: %w", n.ID, err)
		}
		nodeLines = append(nodeLines, b)
	}
	edgeLines := make([][]byte, 0, len(edges))
	for _, e := range edges {
		b, err := json.Marshal(e)
		if err != nil {
			return Meta{}, fmt.Errorf("graph save: marshal edge %s %s->%s: %w", e.Kind, e.From, e.To, err)
		}
		edgeLines = append(edgeLines, b)
	}

	meta := Meta{
		SchemaVersion: SchemaVersion,
		BuiltAt:       time.Now().UTC().Format(time.RFC3339),
		Nodes:         len(nodes),
		Edges:         len(edges),
		Dangling:      len(g.Dangling()),
		Extractors:    extractorsOf(nodes, edges),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return Meta{}, fmt.Errorf("graph save: marshal meta: %w", err)
	}

	// Meta last: a reader that sees a current meta.json can rely on the JSONL
	// beside it already being complete. A version mismatch or a missing meta
	// means "no graph", which is the safe reading of a half-written store.
	if err := writeFileAtomic(filepath.Join(dir, nodesFile), joinLines(nodeLines)); err != nil {
		return Meta{}, fmt.Errorf("graph save: nodes: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, edgesFile), joinLines(edgeLines)); err != nil {
		return Meta{}, fmt.Errorf("graph save: edges: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, metaFile), metaBytes); err != nil {
		return Meta{}, fmt.Errorf("graph save: meta: %w", err)
	}
	return meta, nil
}

// Load reads the graph from <root>/.nightgauge/graph.
//
// Returns (nil, nil, nil) — "no graph, rebuild" — when the store is absent, its
// meta is unreadable, or its schema version does not match. A corrupt JSONL
// line is NOT swallowed that way: it returns an error, because silently
// rebuilding past corruption is how a store that is wrong every time looks like
// a store that is merely cold.
func Load(root string) (*Graph, *Meta, error) {
	if root == "" {
		return nil, nil, fmt.Errorf("graph load: root is required")
	}
	dir := filepath.Join(root, StoreDir)

	metaBytes, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("graph load: read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, nil, nil // corrupt meta — treat as no graph
	}
	if meta.SchemaVersion != SchemaVersion {
		return nil, nil, nil // stale schema — treat as no graph
	}

	g := New()
	if err := readJSONL(filepath.Join(dir, nodesFile), func(line []byte, n int) error {
		var node Node
		if err := json.Unmarshal(line, &node); err != nil {
			return fmt.Errorf("%s:%d: %w", nodesFile, n, err)
		}
		return g.AddNode(node)
	}); err != nil {
		return nil, nil, fmt.Errorf("graph load: %w", err)
	}
	if err := readJSONL(filepath.Join(dir, edgesFile), func(line []byte, n int) error {
		var edge Edge
		if err := json.Unmarshal(line, &edge); err != nil {
			return fmt.Errorf("%s:%d: %w", edgesFile, n, err)
		}
		return g.AddEdge(edge)
	}); err != nil {
		return nil, nil, fmt.Errorf("graph load: %w", err)
	}
	return g, &meta, nil
}

// writeFileAtomic writes data to path via a unique temp file and a rename.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := uniqueTempPath(path)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	// From here on every failure removes THIS writer's temp file, never a
	// path another writer may own.
	cleanup := func() { _ = f.Close(); _ = os.Remove(tmp) }
	if _, err := f.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write temp: %w", err)
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename temp: %w", err)
	}
	return nil
}

// uniqueTempPath returns a temp path in the destination's own directory —
// same directory so the rename stays atomic, unique suffix so two concurrent
// writers cannot collide.
func uniqueTempPath(path string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("temp suffix: %w", err)
	}
	return fmt.Sprintf("%s.tmp-%d-%s", path, os.Getpid(), hex.EncodeToString(b[:])), nil
}

func readJSONL(path string, fn func(line []byte, n int) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // an empty graph writes no lines; absence is not corruption
		}
		return err
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	// JSONL lines here are single-record JSON; 4 MiB is generous for one node
	// or edge and still bounded.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	n := 0
	for sc.Scan() {
		n++
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		buf := make([]byte, len(line))
		copy(buf, line)
		if err := fn(buf, n); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil && err != io.EOF {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}

func joinLines(lines [][]byte) []byte {
	if len(lines) == 0 {
		return nil
	}
	var out []byte
	for _, l := range lines {
		out = append(out, l...)
		out = append(out, '\n')
	}
	return out
}

func extractorsOf(nodes []Node, edges []Edge) []string {
	seen := map[string]bool{}
	for _, n := range nodes {
		seen[n.Prov.Extractor] = true
	}
	for _, e := range edges {
		seen[e.Prov.Extractor] = true
	}
	return sortedKeys(seen)
}
