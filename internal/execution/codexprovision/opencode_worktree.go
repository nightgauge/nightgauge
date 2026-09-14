package codexprovision

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// maxWorktreeFileBytes is the most of one steering or MCP source file an
// OpenCode stage's provisioning reads. The steering summaries keep at most
// 80 lines of a file, and no source a repository keeps comes near it.
const maxWorktreeFileBytes = 1 << 20

// worktreeReader reads an OpenCode stage's steering and MCP sources from its
// worktree and from nowhere else (ADR-022 § 8, #1626). A path is read only
// when, with its symbolic links resolved, it names a regular file inside the
// worktree, and only its first maxWorktreeFileBytes: a link out of the
// worktree would put a file of the operator's into the model's prompt, and a
// FIFO or a device would never finish reading. The file is opened through the
// worktree's os.Root, without blocking, and checked again once it is open, so
// a link or a FIFO swapped in after the first check is refused too. Each file
// it refuses is named once in its warnings.
type worktreeReader struct {
	root     string // the worktree, symbolic links resolved
	refused  map[string]bool
	warnings []string
}

func newWorktreeReader(root string) *worktreeReader {
	return &worktreeReader{root: root, refused: map[string]bool{}}
}

// read returns the content of path, a path in the worktree, or false when it
// is absent or refused. It is a readFunc, as readFileGracefully is, so the
// steering sources Codex reads take it unchanged.
func (w *worktreeReader) read(path string) (string, bool) {
	name := path
	if rel, err := filepath.Rel(w.root, path); err == nil {
		name = filepath.ToSlash(rel)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", false // absent, as readFileGracefully reads it
	}
	if !within(w.root, resolved) {
		why := "is a symbolic link to a file outside the worktree, so Nightgauge does not read it"
		if name == "AGENTS.md" {
			why += "; OpenCode's own search still loads it until project config is switched off (#1638)"
		}
		w.refuse(name, why)
		return "", false
	}
	rel, err := filepath.Rel(w.root, resolved)
	if err != nil {
		return "", false
	}
	root, err := os.OpenRoot(w.root)
	if err != nil {
		return "", false
	}
	defer root.Close()
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			w.refuse(name, "cannot be opened inside the worktree, so Nightgauge does not read it")
		}
		return "", false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return "", false
	}
	if !fi.Mode().IsRegular() {
		w.refuse(name, "is not a regular file, so Nightgauge does not read it")
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxWorktreeFileBytes))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// refuse records, once, that the file name, relative to the worktree, is not
// read, and why.
func (w *worktreeReader) refuse(name, why string) {
	if w.refused[name] {
		return
	}
	w.refused[name] = true
	w.warnings = append(w.warnings, name+" "+why)
}
