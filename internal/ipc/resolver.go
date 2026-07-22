package ipc

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// resolverEntry is a cached resolved GitHub client for a specific (owner, repo) pair.
type resolverEntry struct {
	client           *gh.Client
	tokenFingerprint string    // sha256(token)[:8] — invalidated on token rotation
	configPath       string    // absolute path to .nightgauge/config.yaml
	configMtime      time.Time // mod time at last resolution
}

// ClientResolver resolves the correct *gh.Client for an IPC operation based on
// the target (owner, repo), automatically loading per-repo config to determine
// the configured GitHub identity.
//
// Resolution order:
//  1. Check registry for a path mapped to (owner, repo).
//  2. Load .nightgauge/config.yaml from that path.
//  3. Resolve token via config → GITHUB_TOKEN env → gh CLI.
//  4. Fingerprint the token (sha256[:8]) and mtime the config file.
//  5. Cache the result; evict on config mtime change or fingerprint change.
//
// Falls back to defaultClient when no registry entry exists for (owner, repo).
//
// When a tracker is attached (via NewClientResolverWithTracker), every newly
// resolved per-repo client gets WithRateLimitTracker chained so HTTP response
// headers feed the shared file and the proactive gate consults it before
// dispatching. The cfg-resolved GitHub user is used as the tracker key. See
// Issue #3417 for why this wiring is necessary.
type ClientResolver struct {
	mu    sync.Mutex
	cache map[string]*resolverEntry // "owner/repo" → entry

	registryMu sync.RWMutex
	registry   map[string]string // "owner/repo" → repo filesystem path

	defaultClient     *gh.Client
	suppressGHWarning bool
	tracker           *gh.SharedRateLimitTracker // optional; attached to every resolved client
}

// NewClientResolver creates a ClientResolver backed by the given default client.
// New per-repo clients will not feed the SharedRateLimitTracker — call
// NewClientResolverWithTracker instead when a tracker is available.
func NewClientResolver(defaultClient *gh.Client, suppressGHWarning bool) *ClientResolver {
	return NewClientResolverWithTracker(defaultClient, suppressGHWarning, nil)
}

// NewClientResolverWithTracker is the same as NewClientResolver but also
// attaches the tracker to every newly resolved per-repo client (Issue #3417).
// Pass nil to opt out — equivalent to NewClientResolver.
func NewClientResolverWithTracker(
	defaultClient *gh.Client,
	suppressGHWarning bool,
	tracker *gh.SharedRateLimitTracker,
) *ClientResolver {
	return &ClientResolver{
		cache:             make(map[string]*resolverEntry),
		registry:          make(map[string]string),
		defaultClient:     defaultClient,
		suppressGHWarning: suppressGHWarning,
		tracker:           tracker,
	}
}

// RegisterRepo maps (owner, repo) to a filesystem path so Resolve can find
// the repo's .nightgauge/config.yaml.
func (r *ClientResolver) RegisterRepo(owner, repo, path string) {
	r.registryMu.Lock()
	defer r.registryMu.Unlock()
	r.registry[owner+"/"+repo] = path
}

// RepoPath returns the registered filesystem path for "owner/repo", or ""
// when the repo was never registered. Used to scope a run's on-disk state
// (runtime-{N}.json) to the run's target repo instead of the IPC server's
// launch root (#215).
func (r *ClientResolver) RepoPath(ownerRepo string) string {
	r.registryMu.RLock()
	defer r.registryMu.RUnlock()
	return r.registry[ownerRepo]
}

// RegisteredPaths returns every distinct registered repo filesystem path.
// Order is unspecified.
func (r *ClientResolver) RegisteredPaths() []string {
	r.registryMu.RLock()
	defer r.registryMu.RUnlock()
	seen := make(map[string]bool, len(r.registry))
	paths := make([]string, 0, len(r.registry))
	for _, p := range r.registry {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

// Resolve returns the *gh.Client for the given (owner, repo).
//
// Cache hit conditions (both must hold):
//   - config file mtime unchanged
//   - token fingerprint unchanged
//
// On miss, loads config, resolves token, creates new client, logs once.
func (r *ClientResolver) Resolve(_ context.Context, owner, repo string) (*gh.Client, error) {
	key := owner + "/" + repo

	r.registryMu.RLock()
	repoPath := r.registry[key]
	r.registryMu.RUnlock()

	if repoPath == "" {
		return r.defaultClient, nil // no registry entry → use default client
	}

	configPath := repoPath + "/.nightgauge/config.yaml"
	var currentMtime time.Time
	if fi, err := os.Stat(configPath); err == nil {
		currentMtime = fi.ModTime()
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, ok := r.cache[key]; ok && entry.configMtime.Equal(currentMtime) {
		// Mtime hit — still need to verify token fingerprint hasn't changed.
		cfg, _ := config.Load(repoPath)
		if cfg != nil {
			tok, _ := gh.ResolveTokenChain(cfg, owner)
			fp := tokenFingerprint(tok)
			if fp == entry.tokenFingerprint {
				return entry.client, nil // full cache hit
			}
			// Fingerprint changed (token rotated) → fall through to re-resolve
			log.Printf("IPC ClientResolver: token rotated for %s (fingerprint changed) — re-resolving", key)
		} else {
			return entry.client, nil // can't re-check fingerprint; use cached
		}
	}

	// Cache miss or invalidated — full resolution.
	cfg, err := config.Load(repoPath)
	if err != nil {
		log.Printf("IPC ClientResolver: failed to load config for %s: %v — using default client", key, err)
		return r.defaultClient, nil
	}

	tok, err := gh.ResolveTokenChain(cfg, owner)
	if err != nil || tok == "" {
		log.Printf("IPC ClientResolver: no token resolved for %s — using default client", key)
		return r.defaultClient, nil
	}

	client := gh.NewClientWithToken(tok)
	fp := tokenFingerprint(tok)
	// Label the resolved identity by the TARGET owner (not the workspace root)
	// so the rate-limit tracker key and logs reflect the cross-org identity the
	// token actually belongs to (#4068).
	githubUser := cfg.ResolveGitHubUserForOwner(owner)
	// Wire the shared rate-limit tracker (Issue #3417). Without this, response
	// headers from per-repo clients are silently discarded and the proactive
	// gate is dead code for any IPC call routed through the resolver.
	if r.tracker != nil {
		// WithRateLimitWait: in-flight pipeline ops (board move-status, PR
		// create/merge, revert-status) wait out a rate-limit reset instead of
		// hard-failing and leaving an issue stuck (#3976). Dispatch decisions
		// use a separate explicit tracker read, so this never blocks dispatch.
		client = client.WithRateLimitTracker(r.tracker, githubUser).WithRateLimitWait()
	}
	// Log once per resolved identity (cache miss only, not per operation).
	log.Printf("IPC ClientResolver: resolved identity for %s (user=%q, token=...%s)", key, githubUser, fp)

	r.cache[key] = &resolverEntry{
		client:           client,
		tokenFingerprint: fp,
		configPath:       configPath,
		configMtime:      currentMtime,
	}
	return client, nil
}

// Invalidate evicts the cached client for (owner, repo). Next call to Resolve
// creates a fresh client. Call after receiving an HTTP 401 from the GitHub API.
func (r *ClientResolver) Invalidate(owner, repo string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := owner + "/" + repo
	if _, ok := r.cache[key]; ok {
		delete(r.cache, key)
		log.Printf("IPC ClientResolver: invalidated cached client for %s (401 or explicit eviction)", key)
	}
}

// tokenFingerprint returns the first 8 hex characters of SHA256(token).
// Used as a secondary cache invalidation signal without logging the token.
func tokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)[:8]
}
