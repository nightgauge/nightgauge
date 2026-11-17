// Package shippedfiles finds the files a release ships beside the
// nightgauge binary instead of inside it (#3383): the OpenCode plugin tree
// and the git pre-push hook. Keeping script payloads out of the binary's
// bytes keeps them out of the content a malware scanner's script model reads.
//
// Locate tries, for an entry <name> with checkout-relative source <devRel>:
//
//  1. $NIGHTGAUGE_SHIPPED_FILES_DIR/<name>, then
//     $NIGHTGAUGE_SHIPPED_FILES_DIR/<devRel> (the variable may name a
//     shipped-files directory or a source checkout; `go install` users and
//     tests set it);
//  2. <exe dir>/<name>: a release archive or the Homebrew cask;
//  3. <exe dir>/../<name>: the VS Code extension (dist/bin/nightgauge);
//  4. <exe dir>/../<devRel>: a development bin/nightgauge in a checkout.
//
// The executable path is resolved through symlinks first, so a Homebrew
// symlink finds the files beside the real binary. A miss names every path
// tried, in order.
package shippedfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// EnvDir overrides the lookup; see the package doc.
const EnvDir = "NIGHTGAUGE_SHIPPED_FILES_DIR"

// Entry names, relative to the shipped-files root, and their sources.
const (
	OpenCodePlugin       = "opencode-plugin"
	OpenCodePluginSource = "internal/execution/opencodeplugin/plugin"
	PrePushHook          = "git-hooks/pre-push"
	PrePushHookSource    = "scripts/git-hooks/pre-push"
)

// executable is os.Executable, replaceable by tests.
var executable = os.Executable

// Candidates returns the paths Locate tries, in order.
func Candidates(name, devRel string) []string {
	name, devRel = filepath.FromSlash(name), filepath.FromSlash(devRel)
	var out []string
	if dir := os.Getenv(EnvDir); dir != "" {
		out = append(out, filepath.Join(dir, name), filepath.Join(dir, devRel))
	}
	if exe, err := executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir := filepath.Dir(exe)
		out = append(out,
			filepath.Join(dir, name),
			filepath.Join(dir, "..", name),
			filepath.Join(dir, "..", devRel))
	}
	return out
}

// fileKey is the Payload.Files key of a located entry that is a file, not
// a directory. No path under a directory is ".", so the two cannot collide.
const fileKey = "."

// Payload is a shipped entry read into memory once. Files maps the slash
// path of every regular file under a located directory to its bytes, or
// fileKey to a located file's bytes. LoadVerified checks the digest of these
// bytes, so a caller that uses them uses exactly what was verified: nothing
// is read from disk again after the check.
type Payload struct {
	// Path is where the entry was found.
	Path  string
	Files map[string][]byte
}

// File returns a located file's bytes.
func (p Payload) File() []byte { return p.Files[fileKey] }

// read loads path: one fileKey entry for a file, every regular file under
// it for a directory.
func read(path string) (map[string][]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return map[string][]byte{fileKey: data}, nil
	}
	tree := os.DirFS(path)
	files := map[string][]byte{}
	err = fs.WalkDir(tree, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(tree, p)
		if err != nil {
			return err
		}
		files[p] = data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

// digestOf is Digest over bytes already read.
func digestOf(files map[string][]byte) string {
	if data, ok := files[fileKey]; ok && len(files) == 1 {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	lines := make([]string, 0, len(files))
	for p, data := range files {
		sum := sha256.Sum256(data)
		lines = append(lines, p+"\x00"+hex.EncodeToString(sum[:])+"\n")
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Digest is the sha256 of a file, or of a directory tree as the sorted
// sequence "<slash path>\x00<sha256 of content>\n" over its regular files.
func Digest(path string) (string, error) {
	files, err := read(path)
	if err != nil {
		return "", err
	}
	return digestOf(files), nil
}

// LoadVerified locates an entry, reads it once and refuses it unless the
// Digest of the bytes read is want: the plugin is the OpenCode tool gate and
// the hook is written into .git/hooks, so a file found on a path is trusted
// only by its content, and only the bytes that were checked are returned.
func LoadVerified(name, devRel, want string) (Payload, error) {
	p, err := Locate(name, devRel)
	if err != nil {
		return Payload{}, err
	}
	files, err := read(p)
	if err != nil {
		return Payload{}, fmt.Errorf("shipped file %s: %w", p, err)
	}
	if got := digestOf(files); got != want {
		return Payload{}, fmt.Errorf("shipped file %s has sha256 %s, not the %s this binary pins; "+
			"reinstall nightgauge so the binary and its shipped files match", p, got, want)
	}
	return Payload{Path: p, Files: files}, nil
}

// Locate returns the first candidate that exists.
func Locate(name, devRel string) (string, error) {
	tried := Candidates(name, devRel)
	for _, p := range tried {
		_, err := os.Stat(p)
		if err == nil {
			return p, nil
		}
		// Only a missing candidate falls through (ENOTDIR: a path component
		// is a file): one that exists but cannot be read is reported as
		// itself, not as "not found".
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", fmt.Errorf("shipped file %s: %w", p, err)
		}
	}
	return "", fmt.Errorf("shipped file %s not found beside the nightgauge binary; tried %s "+
		"(set %s to a directory holding it, or to a nightgauge source checkout)",
		name, strings.Join(tried, ", "), EnvDir)
}
