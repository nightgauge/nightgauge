package capabilities

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCapabilitiesMapIsCurrent regenerates docs/CAPABILITIES_MAP.md from
// capabilities.yaml and byte-compares it with what is committed.
//
// Set NIGHTGAUGE_WRITE_CAPABILITIES_MAP=1 to rewrite the file instead of
// failing -- that is how the generated doc is updated.
func TestCapabilitiesMapIsCurrent(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(filepath.Join(root, "capabilities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	want := reg.RenderMarkdown()
	path := filepath.Join(root, "docs", "CAPABILITIES_MAP.md")

	if os.Getenv("NIGHTGAUGE_WRITE_CAPABILITIES_MAP") == "1" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("wrote", path)
		return
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Errorf("docs/CAPABILITIES_MAP.md is stale.\n"+
			"Regenerate with:\n"+
			"  NIGHTGAUGE_WRITE_CAPABILITIES_MAP=1 go test ./internal/capabilities/ -run TestCapabilitiesMapIsCurrent\n"+
			"got %d bytes, want %d bytes", len(got), len(want))
	}
}

// Every cell must carry an explicit value -- never blank. A blank cell is
// unexpressed data posing as a negative (#578's lesson from the registry's
// transports map).
func TestMatrixHasNoBlankCells(t *testing.T) {
	root, _ := filepath.Abs("../..")
	reg, err := Load(filepath.Join(root, "capabilities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m := reg.BuildMatrix()
	if len(m.Rows) == 0 {
		t.Fatal("no rows")
	}
	for _, row := range m.Rows {
		if len(row.Cells) != len(AllSurfaces) {
			t.Errorf("%s: %d cells, want %d", row.ID, len(row.Cells), len(AllSurfaces))
		}
		for _, s := range AllSurfaces {
			if row.Cells[string(s)] == "" {
				t.Errorf("%s: blank cell for surface %q", row.ID, s)
			}
		}
	}
}

func TestCellFor(t *testing.T) {
	c := &Capability{Surfaces: []Surface{SurfaceCLI}, Status: StatusGA}
	if got := cellFor(c, SurfaceCLI); got != "✓" {
		t.Errorf("ga+surface: got %q want ✓", got)
	}
	if got := cellFor(c, SurfaceVSCode); got != "—" {
		t.Errorf("absent surface: got %q want —", got)
	}
	c.Status = StatusBeta
	if got := cellFor(c, SurfaceCLI); got != "beta" {
		t.Errorf("beta: got %q want beta", got)
	}
}

func TestRenderJSON(t *testing.T) {
	root, _ := filepath.Abs("../..")
	reg, err := Load(filepath.Join(root, "capabilities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := reg.RenderJSON()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Error("JSON output must be non-empty and newline-terminated")
	}
}

// Prettier pads every cell to its column width; a generator whose output
// Prettier would rewrite can never be byte-compared for currency.
func TestRenderTable_PadsToColumnWidth(t *testing.T) {
	// "A" is padded to three, not two: Prettier's minimum column width. The
	// generator emitted two-dash delimiters for the "ci" column until this was
	// pinned, and Prettier rewrote them on every run.
	got := renderTable([]string{"A", "Long"}, [][]string{{"xx", "y"}})
	want := "| A   | Long |\n| --- | ---- |\n| xx  | y    |\n"
	if got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}
