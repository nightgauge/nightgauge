package capabilities

import (
	"path/filepath"
	"testing"
)

// TestRegistryValidatesAgainstRealTree is the registry's own gate: it loads the
// repo-root capabilities.yaml and asserts every doc exists and every owns glob
// matches. This is the test that fails when a capability's implementation is
// deleted or relocated without updating the registry.
func TestRegistryValidatesAgainstRealTree(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(filepath.Join(root, "capabilities.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Logf("loaded %d capabilities", len(reg.Capabilities))

	violations, err := reg.ValidateAgainstTree(root)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	for _, v := range violations {
		t.Errorf("%s", v)
	}
	if s := reg.SurfacesWithoutCapability(); len(s) > 0 {
		t.Logf("surfaces with no capability yet: %v", s)
	}
}
