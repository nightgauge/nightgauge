package docs

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// sha256Hex returns the hex sha256 of a string — matches fetchHash implementation.
func sha256Hex(s string) string {
	h := sha256.New()
	h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// writeSnapshotFile writes a minimal snapshot JSON to path. pages maps URL → hash.
func writeSnapshotFile(t *testing.T, path string, pages map[string]string) {
	t.Helper()
	type page struct {
		Hash string `json:"hash"`
	}
	type snap struct {
		Pages map[string]page `json:"pages"`
	}
	s := snap{Pages: make(map[string]page, len(pages))}
	for u, h := range pages {
		s.Pages[u] = page{Hash: h}
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
}

// writeURLsFile writes a URLs file (one URL per line) to path.
func writeURLsFile(t *testing.T, path string, urls []string) {
	t.Helper()
	var data string
	for _, u := range urls {
		data += u + "\n"
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write urls: %v", err)
	}
}

// newBodyServer starts an httptest.Server that serves each URL path with the
// provided body. Paths not in the map return 404.
func newBodyServer(t *testing.T, bodies map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
}

func TestSnapshotDiff_NewPages(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	srv := newBodyServer(t, map[string]string{"/page1": "hello"})
	defer srv.Close()

	writeSnapshotFile(t, snapPath, map[string]string{}) // empty snapshot
	writeURLsFile(t, urlsPath, []string{srv.URL + "/page1"})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if res.V != 1 {
		t.Errorf("V = %d, want 1", res.V)
	}
	if len(res.New) != 1 {
		t.Fatalf("New = %d, want 1", len(res.New))
	}
	if res.New[0].URL != srv.URL+"/page1" {
		t.Errorf("New[0].URL = %q", res.New[0].URL)
	}
	if res.New[0].Hash == "" {
		t.Error("New[0].Hash is empty")
	}
	if len(res.Changed) != 0 {
		t.Errorf("Changed = %d, want 0", len(res.Changed))
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %d, want 0", len(res.Removed))
	}
}

func TestSnapshotDiff_ChangedPages(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	srv := newBodyServer(t, map[string]string{"/page1": "new content"})
	defer srv.Close()

	url := srv.URL + "/page1"
	writeSnapshotFile(t, snapPath, map[string]string{url: "oldhash"})
	writeURLsFile(t, urlsPath, []string{url})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(res.Changed) != 1 {
		t.Fatalf("Changed = %d, want 1", len(res.Changed))
	}
	if res.Changed[0].OldHash != "oldhash" {
		t.Errorf("OldHash = %q, want oldhash", res.Changed[0].OldHash)
	}
	if res.Changed[0].Hash == "" {
		t.Error("Hash is empty")
	}
	if len(res.New) != 0 {
		t.Errorf("New = %d, want 0", len(res.New))
	}
	if len(res.Removed) != 0 {
		t.Errorf("Removed = %d, want 0", len(res.Removed))
	}
}

func TestSnapshotDiff_RemovedPages(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	writeSnapshotFile(t, snapPath, map[string]string{"https://example.com/old": "abc"})
	writeURLsFile(t, urlsPath, []string{}) // empty — all pages removed

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(res.Removed) != 1 {
		t.Fatalf("Removed = %d, want 1", len(res.Removed))
	}
	if res.Removed[0].URL != "https://example.com/old" {
		t.Errorf("Removed[0].URL = %q", res.Removed[0].URL)
	}
	if len(res.New) != 0 {
		t.Errorf("New = %d, want 0", len(res.New))
	}
	if len(res.Changed) != 0 {
		t.Errorf("Changed = %d, want 0", len(res.Changed))
	}
}

func TestSnapshotDiff_MixedChanges(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	srv := newBodyServer(t, map[string]string{
		"/new":     "brand new",
		"/changed": "updated content",
		"/same":    "unchanged",
	})
	defer srv.Close()

	unchangedURL := srv.URL + "/same"
	unchangedHash := sha256Hex("unchanged")

	writeSnapshotFile(t, snapPath, map[string]string{
		srv.URL + "/changed": "oldhash",
		srv.URL + "/removed": "gone",
		unchangedURL:         unchangedHash,
	})
	writeURLsFile(t, urlsPath, []string{
		srv.URL + "/new",
		srv.URL + "/changed",
		unchangedURL,
	})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(res.New) != 1 {
		t.Errorf("New = %d, want 1", len(res.New))
	}
	if len(res.Changed) != 1 {
		t.Errorf("Changed = %d, want 1", len(res.Changed))
	}
	if len(res.Removed) != 1 {
		t.Errorf("Removed = %d, want 1", len(res.Removed))
	}
	if res.Removed[0].URL != srv.URL+"/removed" {
		t.Errorf("Removed[0].URL = %q", res.Removed[0].URL)
	}
}

func TestSnapshotDiff_EmptySnapshotAndEmptyURLs(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	writeSnapshotFile(t, snapPath, map[string]string{})
	writeURLsFile(t, urlsPath, []string{})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(res.New) != 0 || len(res.Changed) != 0 || len(res.Removed) != 0 {
		t.Errorf("expected all empty arrays, got new=%d changed=%d removed=%d",
			len(res.New), len(res.Changed), len(res.Removed))
	}
}

func TestSnapshotDiff_MalformedSnapshotJSON(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	if err := os.WriteFile(snapPath, []byte("not json {{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeURLsFile(t, urlsPath, []string{})

	_, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
	})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestSnapshotDiff_MissingSnapshotFile(t *testing.T) {
	dir := t.TempDir()
	urlsPath := filepath.Join(dir, "urls.txt")
	writeURLsFile(t, urlsPath, []string{})

	_, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: filepath.Join(dir, "does-not-exist.json"),
		URLsFile:     urlsPath,
	})
	if err == nil {
		t.Fatal("expected error for missing snapshot file, got nil")
	}
}

func TestSnapshotDiff_MissingURLsFile(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	writeSnapshotFile(t, snapPath, map[string]string{})

	_, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     filepath.Join(dir, "does-not-exist.txt"),
	})
	if err == nil {
		t.Fatal("expected error for missing URLs file, got nil")
	}
}

func TestSnapshotDiff_FetchFailureIsNonFatal(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	srv := newBodyServer(t, map[string]string{}) // no paths → 404 for all
	defer srv.Close()

	writeSnapshotFile(t, snapPath, map[string]string{})
	writeURLsFile(t, urlsPath, []string{srv.URL + "/missing"})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v (should be non-fatal)", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for the failed fetch, got none")
	}
	if len(res.New) != 0 {
		t.Errorf("New = %d, want 0", len(res.New))
	}
}

func TestSnapshotDiff_UnchangedPageNotReported(t *testing.T) {
	dir := t.TempDir()
	snapPath := filepath.Join(dir, "snapshot.json")
	urlsPath := filepath.Join(dir, "urls.txt")

	body := "stable content"
	srv := newBodyServer(t, map[string]string{"/page": body})
	defer srv.Close()

	url := srv.URL + "/page"
	hash := sha256Hex(body)

	writeSnapshotFile(t, snapPath, map[string]string{url: hash})
	writeURLsFile(t, urlsPath, []string{url})

	res, err := SnapshotDiff(SnapshotDiffOptions{
		SnapshotFile: snapPath,
		URLsFile:     urlsPath,
		HTTPClient:   srv.Client(),
	})
	if err != nil {
		t.Fatalf("SnapshotDiff: %v", err)
	}
	if len(res.New) != 0 || len(res.Changed) != 0 || len(res.Removed) != 0 {
		t.Errorf("unchanged page should not appear in any list: new=%d changed=%d removed=%d",
			len(res.New), len(res.Changed), len(res.Removed))
	}
}
