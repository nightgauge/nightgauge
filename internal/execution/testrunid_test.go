package execution

import "github.com/nightgauge/nightgauge/internal/runstate"

// testRunID mints a REAL run identity for tests — the same runstate.NewRunID()
// production mints, so every fixture carries a canonical lowercase UUIDv7 that
// the snapshot composer, the discovery regex and the IPC wire validation all
// accept (ADR-017 Decision 1). Hand-written ids are deliberately avoided: an
// identity that only the test believes in proves nothing about the filename
// the run will actually get.
func testRunID() string {
	id, err := runstate.NewRunID()
	if err != nil {
		panic("test run identity: " + err.Error())
	}
	return id
}
