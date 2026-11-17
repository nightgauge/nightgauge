package main

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/nightgauge/nightgauge/internal/shippedfiles"
)

// goreleaserArchiveFile is one archives[].files entry: a bare glob, or the
// object form with a destination.
type goreleaserArchiveFile struct {
	Src         string `yaml:"src"`
	Dst         string `yaml:"dst"`
	StripParent bool   `yaml:"strip_parent"`
	Info        struct {
		Mode os.FileMode `yaml:"mode"`
	} `yaml:"info"`
}

func (f *goreleaserArchiveFile) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		f.Src = n.Value
		return nil
	}
	type plain goreleaserArchiveFile
	return n.Decode((*plain)(f))
}

// archiveLayout maps each archive path the .goreleaser.yml archives would
// contain to the archive entry that put it there, by expanding each entry's
// glob against the checkout the way GoReleaser does.
func archiveLayout(t *testing.T) map[string]goreleaserArchiveFile {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yml"))
	if err != nil {
		t.Fatalf("HARNESS ERROR: %v", err)
	}
	var cfg struct {
		Archives []struct {
			Files []goreleaserArchiveFile `yaml:"files"`
		} `yaml:"archives"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("HARNESS ERROR: parse .goreleaser.yml: %v", err)
	}
	if len(cfg.Archives) == 0 {
		t.Fatal("HARNESS ERROR: .goreleaser.yml declares no archives; the layout check would check nothing")
	}
	layout := map[string]goreleaserArchiveFile{}
	for i, a := range cfg.Archives {
		if len(a.Files) == 0 {
			t.Errorf("archives[%d] lists no files: the shipped payloads are missing from it", i)
		}
		for _, f := range a.Files {
			matches, err := filepath.Glob(filepath.Join(repoRoot, filepath.FromSlash(f.Src)))
			if err != nil {
				t.Fatalf("archives[%d] glob %q: %v", i, f.Src, err)
			}
			for _, m := range matches {
				rel, err := filepath.Rel(repoRoot, m)
				if err != nil {
					t.Fatal(err)
				}
				dst := filepath.ToSlash(rel)
				if f.StripParent {
					dst = filepath.Base(m)
				}
				if f.Dst != "" {
					dst = path.Join(f.Dst, dst)
				}
				// strip_parent flattens to basenames, so two sources can
				// land on one archive path and the later silently wins.
				if prev, ok := layout[dst]; ok && prev.Src != f.Src {
					t.Errorf("archive path %s is written by both %s and %s", dst, prev.Src, f.Src)
				}
				layout[dst] = f
			}
		}
	}
	return layout
}

// TestReleaseArchiveShipsTheScriptPayloads (#3383): every release archive
// carries the whole OpenCode plugin tree and the pre-push hook at the paths
// internal/shippedfiles looks for beside the binary. strip_parent flattens a
// glob to basenames, so a plugin file in a directory no entry covers would
// silently go missing from the archive; this test walks the source tree.
func TestReleaseArchiveShipsTheScriptPayloads(t *testing.T) {
	layout := archiveLayout(t)

	src := filepath.Join(repoRoot, filepath.FromSlash(shippedfiles.OpenCodePluginSource))
	var want []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		want = append(want, path.Join(shippedfiles.OpenCodePlugin, filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		t.Fatalf("HARNESS ERROR: walk %s: %v", src, err)
	}
	if len(want) == 0 {
		t.Fatalf("HARNESS ERROR: no files under %s; the check would check nothing", src)
	}
	sort.Strings(want)
	for _, w := range want {
		if _, ok := layout[w]; !ok {
			t.Errorf("the release archive does not ship %s: add a .goreleaser.yml archives.files entry for it", w)
		}
	}
	// The binary refuses a shipped tree whose digest is not the source's, so
	// an extra file under opencode-plugin/ breaks every OpenCode stage too.
	wantSet := map[string]bool{}
	for _, w := range want {
		wantSet[w] = true
	}
	for dst := range layout {
		if strings.HasPrefix(dst, shippedfiles.OpenCodePlugin+"/") && !wantSet[dst] {
			t.Errorf("the release archive ships %s, which is not in %s", dst, shippedfiles.OpenCodePluginSource)
		}
	}

	hook, ok := layout[shippedfiles.PrePushHook]
	switch {
	case !ok:
		t.Errorf("the release archive does not ship %s", shippedfiles.PrePushHook)
	case hook.Src != shippedfiles.PrePushHookSource:
		t.Errorf("%s comes from %s, want %s", shippedfiles.PrePushHook, hook.Src, shippedfiles.PrePushHookSource)
	case hook.Info.Mode != 0o755:
		t.Errorf("%s ships with mode %o, want 755: the installed hook must be executable", shippedfiles.PrePushHook, hook.Info.Mode)
	}
}
