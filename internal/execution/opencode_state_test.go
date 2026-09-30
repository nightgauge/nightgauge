package execution

import "path/filepath"

// testStateHome is the machine-state root a test pairs with its fake home:
// the macOS default layout, <home>/.nightgauge/state. isolateOpenCodeHome
// points NIGHTGAUGE_STATE_HOME at it.
func testStateHome(home string) string {
	return filepath.Join(home, ".nightgauge", "state")
}
