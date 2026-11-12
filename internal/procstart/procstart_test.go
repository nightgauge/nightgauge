package procstart

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// A live process has a stable, non-zero start time; a dead one has none.
func TestStart(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		if _, err := Start(os.Getpid()); err == nil {
			t.Fatal("Start succeeded on a platform without support")
		}
		return
	}
	a, err := Start(os.Getpid())
	if err != nil || a == 0 {
		t.Fatalf("Start(self) = %d, %v; want non-zero", a, err)
	}
	b, err := Start(os.Getpid())
	if err != nil || b != a {
		t.Fatalf("Start(self) changed: %d then %d (%v)", a, b, err)
	}
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatal(err)
	}
	if s, err := Start(dead.Process.Pid); err == nil {
		t.Fatalf("Start(dead pid %d) = %d, want an error", dead.Process.Pid, s)
	}
}
