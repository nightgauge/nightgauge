package github

// Persistent conditional-GET store.
//
// GitHub does not count a REST request against the primary rate limit when it
// answers 304 Not Modified to an authorized request carrying If-None-Match.
// That makes a stored ETag worth more than any cache TTL: a read that has not
// changed costs nothing however often it is asked, and a read that has
// changed is always seen. The transport's in-memory ETag layer
// (rateLimitHeaderTransport) already gets this inside one *Client, but it
// lives and dies with that client and refuses bodies over 1 MiB — and a
// Projects REST item page is ~2.5 MB. So the reads that matter most were the
// ones it could never serve, and every daemon restart (every window reload)
// started cold.
//
// This store fixes both. It keeps, per (token identity, URL):
//
//   - the ETag the server last sent,
//   - the absolute URL of the next page (from the Link header), and
//   - the caller's REDUCED payload — the few fields it maps, never the raw
//     body — so a 2.5 MB page is kept as the tens of kilobytes it maps to.
//
// Entries are written to disk as one small JSON file each, so a restarted
// daemon (a new process) revalidates with the same ETags and is answered 304.
//
// The transport's own in-memory layer is backed by the same directory (under
// a separate key namespace, whole bodies up to its 1 MiB cap): a REST read
// that is not a condGet caller — the CI check reads, for instance — also
// revalidates after a restart instead of paying once more.
//
// Keyed by token identity, never by login alone: two tokens can see different
// things (a fine-grained token scoped to fewer repos, a GitHub App
// installation), and a payload one of them was allowed to read must never be
// served to the other on the strength of a matching URL. The identity is a
// truncated SHA-256 of the token; the token itself is never written.
//
// Concurrency: several daemons (one per window) may share the directory.
// Every file is written atomically (temp + rename), so a reader sees a whole
// entry or the previous one; two writers racing on one URL each write a
// complete, self-consistent (ETag, payload) pair, and whichever lands last is
// simply the entry the next request revalidates. A cache may lose that race;
// it may not tear.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nightgauge/nightgauge/internal/atomicfile"
)

// condStoreVersion is the ENTRY layout's version (condEntry). A reducer's
// payload shape is versioned separately, by the schema tag each condGet caller
// passes, so a change to one reducer invalidates only its own entries.
const condStoreVersion = "v1"

// condStoreMaxMemEntries bounds the in-memory mirror. Past it the mirror is
// dropped and refilled from disk on demand; disk entries are not evicted.
const condStoreMaxMemEntries = 8192

// condEntry is one stored conditional-GET answer.
type condEntry struct {
	URL      string          `json:"url"`
	ETag     string          `json:"etag"`
	Next     string          `json:"next,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	StoredAt time.Time       `json:"storedAt"`
}

// ConditionalStore holds ETag + reduced payload per (identity, URL). A store
// with an empty directory is memory-only: correct, just not persistent.
type ConditionalStore struct {
	dir string
	mu  sync.Mutex
	mem map[string]condEntry
}

// NewConditionalStore returns a store persisting under dir ("" = memory only).
// The directory is created 0700, and an existing one is tightened to 0700:
// the entries hold forge answers read with a private token.
func NewConditionalStore(dir string) *ConditionalStore {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			_ = os.Chmod(dir, 0o700)
		}
	}
	return &ConditionalStore{dir: dir, mem: map[string]condEntry{}}
}

// cacheHomeEnv overrides the cache root: the store is then
// $NIGHTGAUGE_CACHE_HOME/github-conditional.
const cacheHomeEnv = "NIGHTGAUGE_CACHE_HOME"

// ConditionalStoreDir is THE one place the store's location is decided:
// $NIGHTGAUGE_CACHE_HOME/github-conditional when that is set, else the OS
// user cache directory's nightgauge/github-conditional (os.UserCacheDir:
// ~/Library/Caches on macOS, $XDG_CACHE_HOME or ~/.cache on Linux,
// %LocalAppData% on Windows).
//
// Per user and never inside a repository: the entries are keyed by token
// identity and hold forge answers, not repository state. An error means there
// is no cache directory; the caller then keeps memory-only stores.
func ConditionalStoreDir() (string, error) {
	if root := os.Getenv(cacheHomeEnv); root != "" {
		return filepath.Join(root, "github-conditional"), nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "nightgauge", "github-conditional"), nil
}

// processCondStore is the store every client built by NewClientWithToken
// adopts. nil means "memory-only per client", which is what a one-shot CLI
// process wants; `nightgauge serve` installs a disk-backed one at startup.
var processCondStore atomic.Pointer[ConditionalStore]

// SetProcessConditionalStore installs the store clients built after this call
// share. Pass nil to go back to per-client memory stores.
func SetProcessConditionalStore(s *ConditionalStore) { processCondStore.Store(s) }

// tokenIdentity is the store key's identity half for a token: a truncated
// SHA-256, so the token never reaches disk and two tokens never collide.
func tokenIdentity(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("nightgauge-conditional\x00" + token))
	return "tok:" + hex.EncodeToString(sum[:12])
}

func condKey(identity, url string) string {
	sum := sha256.Sum256([]byte(condStoreVersion + "\x00" + identity + "\x00" + url))
	return hex.EncodeToString(sum[:])
}

func (s *ConditionalStore) path(key string) string {
	return filepath.Join(s.dir, key[:2], key+".json")
}

// get returns the stored entry for (identity, url), consulting disk when the
// memory mirror does not hold it.
func (s *ConditionalStore) get(identity, url string) (condEntry, bool) {
	if s == nil {
		return condEntry{}, false
	}
	key := condKey(identity, url)
	s.mu.Lock()
	e, ok := s.mem[key]
	s.mu.Unlock()
	if ok {
		return e, true
	}
	if s.dir == "" {
		return condEntry{}, false
	}
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		return condEntry{}, false
	}
	if err := json.Unmarshal(data, &e); err != nil || e.URL != url || e.ETag == "" {
		// A torn, foreign or hash-colliding file is a miss, never a hit: the
		// worst a miss costs is one full read.
		return condEntry{}, false
	}
	s.remember(key, e)
	return e, true
}

// put stores an entry. Disk failures are swallowed: the store is an
// optimisation, and a request must never fail because its cache could not be
// written.
func (s *ConditionalStore) put(identity, url string, e condEntry) {
	if s == nil || e.ETag == "" {
		return
	}
	e.URL = url
	key := condKey(identity, url)
	s.remember(key, e)
	if s.dir == "" {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	p := s.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = atomicfile.Write(p, data, 0o600)
}

func (s *ConditionalStore) remember(key string, e condEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.mem) >= condStoreMaxMemEntries {
		s.mem = map[string]condEntry{}
	}
	s.mem[key] = e
}

// rawResponseKeyPrefix namespaces the transport layer's whole-response
// entries away from condGet's reduced payloads: the same URL can be read both
// ways (GetRepositoryID reads /repos/{o}/{r} raw, RepoMetadata reduces it),
// and a 304 must hand back the shape its own caller stored.
const rawResponseKeyPrefix = "raw:"

// persistedHeaders are the response headers worth replaying on a 304 served
// from disk: the ones callers read (pagination, scopes). Rate-limit headers
// always come from the live 304.
var persistedHeaders = []string{"Content-Type", "Link", "X-Oauth-Scopes", "X-Accepted-Oauth-Scopes", "X-Github-Media-Type"}

type rawResponse struct {
	Body   []byte      `json:"body"`
	Header http.Header `json:"header,omitempty"`
}

// persistedResponse returns the disk entry the transport's in-memory ETag
// layer lost to a restart. Disk-backed stores only: a memory-only store would
// just duplicate the transport's own cache.
func (c *Client) persistedResponse(url string) (*etagCacheEntry, bool) {
	c.mu.Lock()
	store, identity := c.cond, c.identity
	c.mu.Unlock()
	if store == nil || store.dir == "" {
		return nil, false
	}
	e, ok := store.getDisk(identity, rawResponseKeyPrefix+url)
	if !ok {
		return nil, false
	}
	var raw rawResponse
	if json.Unmarshal(e.Payload, &raw) != nil {
		return nil, false
	}
	return &etagCacheEntry{etag: e.ETag, body: raw.Body, header: raw.Header}, true
}

// persistResponse writes a transport-layer response to the disk store.
func (c *Client) persistResponse(url, etag string, body []byte, header http.Header) {
	c.mu.Lock()
	store, identity := c.cond, c.identity
	c.mu.Unlock()
	if store == nil || store.dir == "" {
		return
	}
	keep := http.Header{}
	for _, k := range persistedHeaders {
		if vv := header.Values(k); len(vv) > 0 {
			keep[k] = append([]string(nil), vv...)
		}
	}
	payload, err := json.Marshal(rawResponse{Body: body, Header: keep})
	if err != nil {
		return
	}
	store.putDisk(identity, rawResponseKeyPrefix+url, condEntry{ETag: etag, Payload: payload, StoredAt: time.Now().UTC()})
}

// getDisk / putDisk bypass the memory mirror, for entries (whole response
// bodies, up to 1 MiB each) the transport already keeps in memory itself.
func (s *ConditionalStore) getDisk(identity, url string) (condEntry, bool) {
	if s == nil || s.dir == "" {
		return condEntry{}, false
	}
	data, err := os.ReadFile(s.path(condKey(identity, url)))
	if err != nil {
		return condEntry{}, false
	}
	var e condEntry
	if err := json.Unmarshal(data, &e); err != nil || e.URL != url || e.ETag == "" {
		return condEntry{}, false
	}
	return e, true
}

func (s *ConditionalStore) putDisk(identity, url string, e condEntry) {
	if s == nil || s.dir == "" || e.ETag == "" {
		return
	}
	e.URL = url
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	p := s.path(condKey(identity, url))
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	_ = atomicfile.Write(p, data, 0o600)
}

// Prune deletes stored entries not written for maxAge, so URLs nobody asks for
// any more (a merged PR's reviews, a deleted branch) do not accumulate, and
// then — if what is left exceeds maxBytes — the oldest entries until it does
// not. Errors are ignored for the same reason writes are. A 304 does not
// rewrite its entry, so an entry answered 304 for the whole of maxAge is
// pruned too and read in full once more: one request per URL per maxAge, the
// price of not writing on every revalidation.
func (s *ConditionalStore) Prune(maxAge time.Duration, maxBytes int64) {
	if s == nil || s.dir == "" {
		return
	}
	type file struct {
		path string
		mod  time.Time
		size int64
	}
	var kept []file
	var total int64
	cutoff := time.Now().Add(-maxAge)
	_ = filepath.WalkDir(s.dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.ModTime().Before(cutoff) {
			_ = os.Remove(p)
			return nil
		}
		kept = append(kept, file{p, info.ModTime(), info.Size()})
		total += info.Size()
		return nil
	})
	if maxBytes <= 0 || total <= maxBytes {
		return
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].mod.Before(kept[j].mod) })
	for _, f := range kept {
		if total <= maxBytes {
			break
		}
		if os.Remove(f.path) == nil {
			total -= f.size
		}
	}
	// The memory mirror may still hold entries whose files are gone; they are
	// only an optimisation, so drop it rather than track which.
	s.mu.Lock()
	s.mem = map[string]condEntry{}
	s.mu.Unlock()
}

// RunMaintenance prunes now and then every `every` until ctx ends — the
// long-lived daemon's housekeeping, so the store stays bounded however long
// the daemon lives.
func (s *ConditionalStore) RunMaintenance(ctx context.Context, maxAge time.Duration, maxBytes int64, every time.Duration) {
	s.Prune(maxAge, maxBytes)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Prune(maxAge, maxBytes)
		}
	}
}
