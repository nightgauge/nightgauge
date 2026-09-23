//go:build unix

package opencodeplugin

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestReadRunEventsNeverBlocksOnAFIFO: an events path that is a FIFO with no
// writer is refused promptly. A blocking open or read would hang the
// scheduler's stage completion forever (#1653).
func TestReadRunEventsNeverBlocksOnAFIFO(t *testing.T) {
	path := filepath.Join(t.TempDir(), EventsFileName("run-1"))
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	type outcome struct {
		n   int
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		events, err := ReadRunEvents(path)
		done <- outcome{len(events), err}
	}()
	select {
	case got := <-done:
		if got.err == nil || !strings.Contains(got.err.Error(), "not a regular file") {
			t.Errorf("ReadRunEvents(FIFO) = %d events, err %v; want a not-a-regular-file refusal", got.n, got.err)
		}
	case <-time.After(5 * time.Second):
		// Unblock the stuck reader so the test binary can exit.
		if f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
			f.Close()
		}
		t.Fatal("ReadRunEvents blocked on a FIFO for 5s")
	}
}

// TestEventsOpenFlagsRefuseASymlink: the open that follows the containment
// check refuses a symlink, so one swapped in between the check and the open
// is never followed (#1653).
func TestEventsOpenFlagsRefuseASymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.jsonl")
	if err := os.WriteFile(target, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(link, eventsOpenFlags, 0)
	if err == nil {
		f.Close()
		t.Fatal("opening a symlink with eventsOpenFlags succeeded; want it refused")
	}
}
