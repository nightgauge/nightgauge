package forgecmd

import (
	"os"
	"testing"
)

// Tests here pin CI themselves where it matters, as internal/github's TestMain
// does. An extension-hosted stage exports NIGHTGAUGE_HOST_CI, which the token chain
// reads ahead of CI (#2514). A leaked value decided whether these tests ran as
// CI, so a run from inside a stage failed on a clean tree (#2524).
func TestMain(m *testing.M) {
	_ = os.Unsetenv("CI")
	_ = os.Unsetenv("NIGHTGAUGE_HOST_CI")
	os.Exit(m.Run())
}
