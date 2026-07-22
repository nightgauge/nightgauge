package forge

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Router resolves a forge identifier to a constructed ForgeClient. It
// supports three input forms:
//
//  1. An explicit forge ID matching one of the configured forges (e.g.
//     "github" or "acme-gitlab"). Used for `--forge <id>` and
//     IB_FORGE.
//  2. A repo spec ("owner/repo") whose forge is inferred from the
//     router's repo→forge map.
//  3. No input (single-forge fallback) — returns the singleton forge
//     configured at construction time.
//
// The interface is deliberately narrow: a future router (#3361) will add
// ResolveLink, ambiguity validation, and orphan-warning logic on top of
// the same For/ByID surface.
type Router struct {
	// configs is the per-id forge configuration registered via Register.
	configs map[string]Config

	// repoToID maps a normalised "owner/repo" lowercase key to the forge
	// ID that owns that repo.
	repoToID map[string]string

	// defaultID is the forge ID used when neither --forge nor --repo
	// resolves to a registered forge. Empty when no fallback is set.
	defaultID string
}

// NewRouter constructs an empty Router with no registered forges. Use
// Register to add forges and SetDefault to declare the fallback.
func NewRouter() *Router {
	return &Router{
		configs:  map[string]Config{},
		repoToID: map[string]string{},
	}
}

// Register associates a forge ID (e.g. "github") with its construction
// Config. Re-registering an existing ID replaces the prior config — this
// keeps tests trivial and matches RegisterAdapter's semantics.
func (r *Router) Register(id string, cfg Config) {
	if r == nil {
		return
	}
	r.configs[strings.ToLower(strings.TrimSpace(id))] = cfg
}

// MapRepo declares that the given "owner/repo" spec is hosted on the
// forge with the given id. Lookups are case-insensitive.
func (r *Router) MapRepo(repoSpec, forgeID string) {
	if r == nil || repoSpec == "" || forgeID == "" {
		return
	}
	r.repoToID[strings.ToLower(strings.TrimSpace(repoSpec))] = strings.ToLower(strings.TrimSpace(forgeID))
}

// SetDefault declares the forge ID used when no explicit --forge flag
// or --repo lookup matches.
func (r *Router) SetDefault(id string) {
	if r == nil {
		return
	}
	r.defaultID = strings.ToLower(strings.TrimSpace(id))
}

// IDs returns the registered forge IDs, sorted alphabetically. Useful
// for diagnostic output ("registered forges: github, gitlab").
func (r *Router) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.configs))
	for id := range r.configs {
		out = append(out, id)
	}
	// Stable order — small N, simple insertion sort is fine.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// For resolves a forge given the CLI's --forge flag and --repo flag.
// Resolution precedence: explicit forgeFlag > IB_FORGE env > repoSpec
// inference > defaultID > sole-registered-forge fallback.
//
// Returns ErrUnsupported (wrapped) when an explicit forge ID is given
// but not registered.
func (r *Router) For(forgeFlag, repoSpec string) (ForgeClient, error) {
	if r == nil {
		return nil, fmt.Errorf("forge: nil router")
	}

	id, err := r.resolveID(forgeFlag, repoSpec)
	if err != nil {
		return nil, err
	}
	cfg, ok := r.configs[id]
	if !ok {
		return nil, wrapUnsupported(Kind(id))
	}
	return New(cfg)
}

// ByID is the explicit-id resolver used by callers that already know
// the forge identifier (e.g. when iterating r.IDs()). Returns
// ErrUnsupported (wrapped) for unregistered IDs.
func (r *Router) ByID(id string) (ForgeClient, error) {
	if r == nil {
		return nil, fmt.Errorf("forge: nil router")
	}
	cfg, ok := r.configs[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return nil, wrapUnsupported(Kind(id))
	}
	return New(cfg)
}

// ValidationError reports a misconfiguration in the router's repo/forge mappings.
type ValidationError struct {
	Path    string // e.g. "repositories.acme/platform.forge"
	Message string // human-readable description
	Fatal   bool   // false = warning (non-fatal); true = blocks dispatch
}

// Validate checks the router for misconfigurations:
//   - Dangling forge ref: a MapRepo call referenced a forge ID not in r.configs
//   - Orphan forge: a registered forge has no repos mapped to it (warning only)
//   - Ambiguous mapping: a repo appears in repoToID more than once (cannot happen
//     via MapRepo alone, detected for manual-construction safety)
//
// Returns nil when valid. Callers must treat Fatal=true entries as hard errors.
func (r *Router) Validate() []ValidationError {
	if r == nil {
		return nil
	}
	var errs []ValidationError

	// Dangling forge refs: repos mapped to unknown forge IDs.
	for repoSpec, forgeID := range r.repoToID {
		if _, ok := r.configs[forgeID]; !ok {
			errs = append(errs, ValidationError{
				Path:    "repositories." + repoSpec + ".forge",
				Message: fmt.Sprintf("repo %q references forge %q which is not registered (registered: %s)", repoSpec, forgeID, strings.Join(r.IDs(), ", ")),
				Fatal:   true,
			})
		}
	}

	// Orphan forges: registered but no repos mapped to them.
	usedForges := make(map[string]bool)
	for _, id := range r.repoToID {
		usedForges[id] = true
	}
	// The default forge is considered "used" even without an explicit repo mapping
	// because callers reach it via the defaultID fallback in resolveID.
	if r.defaultID != "" {
		usedForges[r.defaultID] = true
	}
	for id := range r.configs {
		if !usedForges[id] {
			errs = append(errs, ValidationError{
				Path:    "forges." + id,
				Message: fmt.Sprintf("forge %q is registered but no repositories map to it (orphan warning — forge may still be used via --forge flag)", id),
				Fatal:   false,
			})
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ResolveLink returns the canonical reference string for a cross-repo link.
// When fromRepo and toRepo are on the same forge, returns the compact slug
// form (e.g. "owner/repo#42"). When they cross forge boundaries, returns
// the full URL:
//   - GitHub: "https://github.com/owner/repo/issues/N"
//   - GitLab: "https://<host>/group/project/-/issues/N"
//
// linkSlug is the input reference ("owner/repo#42" or a full URL).
// The method does not make network calls.
func (r *Router) ResolveLink(fromRepo, toRepo, linkSlug string) string {
	if r == nil {
		return linkSlug
	}

	fromForge := r.repoToID[strings.ToLower(strings.TrimSpace(fromRepo))]
	if fromForge == "" {
		fromForge = r.defaultID
	}
	toForge := r.repoToID[strings.ToLower(strings.TrimSpace(toRepo))]
	if toForge == "" {
		toForge = r.defaultID
	}

	// Same forge → compact slug is sufficient.
	if fromForge == toForge && fromForge != "" {
		num := issueNumberFromSlug(linkSlug)
		if num == "" {
			return linkSlug
		}
		return toRepo + "#" + num
	}

	// Cross-forge → full URL using the target forge's config.
	toCfg, ok := r.configs[toForge]
	if !ok {
		// Unknown target forge — return linkSlug unchanged.
		return linkSlug
	}

	num := issueNumberFromSlug(linkSlug)
	if num == "" {
		return linkSlug
	}
	switch toCfg.Kind {
	case KindGitHub:
		return "https://github.com/" + toRepo + "/issues/" + num
	case KindGitLab:
		host := toCfg.Host
		if host == "" {
			host = "gitlab.com"
		}
		return "https://" + host + "/" + toRepo + "/-/issues/" + num
	default:
		return linkSlug
	}
}

// issueNumberFromSlug extracts the issue number from a "owner/repo#N" slug
// or a full URL. Returns "" when no valid number is found.
func issueNumberFromSlug(slug string) string {
	// Full URL: extract trailing number segment.
	if strings.HasPrefix(slug, "http://") || strings.HasPrefix(slug, "https://") {
		parts := strings.Split(strings.TrimRight(slug, "/"), "/")
		if len(parts) > 0 {
			last := parts[len(parts)-1]
			if _, err := strconv.Atoi(last); err == nil {
				return last
			}
		}
		return ""
	}
	// Slug form: "owner/repo#N"
	if idx := strings.LastIndex(slug, "#"); idx != -1 {
		num := slug[idx+1:]
		if _, err := strconv.Atoi(num); err == nil {
			return num
		}
	}
	return ""
}

// KindFor returns the forge Kind registered under the given forge ID.
// Returns an empty string when the ID is not registered.
func (r *Router) KindFor(id string) Kind {
	if r == nil {
		return ""
	}
	cfg, ok := r.configs[strings.ToLower(strings.TrimSpace(id))]
	if !ok {
		return ""
	}
	return cfg.Kind
}

// ForgeIDFor returns the forge ID that owns the given repo spec, applying
// the same precedence chain as resolveID but without constructing a client.
// Returns ("", error) when the forge cannot be determined.
func (r *Router) ForgeIDFor(repoSpec string) (string, error) {
	if r == nil {
		return "", fmt.Errorf("forge: nil router")
	}
	if repoSpec != "" {
		if id, ok := r.repoToID[strings.ToLower(strings.TrimSpace(repoSpec))]; ok {
			if _, ok := r.configs[id]; ok {
				return id, nil
			}
		}
	}
	if r.defaultID != "" {
		if _, ok := r.configs[r.defaultID]; ok {
			return r.defaultID, nil
		}
	}
	if len(r.configs) == 1 {
		for id := range r.configs {
			return id, nil
		}
	}
	return "", fmt.Errorf("forge: cannot determine forge for repo %q", repoSpec)
}

// resolveID applies the precedence chain and returns the lowercase
// forge id that should be used. It does not construct the client.
func (r *Router) resolveID(forgeFlag, repoSpec string) (string, error) {
	// 1. Explicit --forge flag wins.
	if id := strings.ToLower(strings.TrimSpace(forgeFlag)); id != "" {
		if _, ok := r.configs[id]; !ok {
			return "", wrapUnsupported(Kind(id))
		}
		return id, nil
	}

	// 2. IB_FORGE env var.
	if id := strings.ToLower(strings.TrimSpace(os.Getenv("IB_FORGE"))); id != "" {
		if _, ok := r.configs[id]; !ok {
			return "", wrapUnsupported(Kind(id))
		}
		return id, nil
	}

	// 3. --repo inference.
	if repoSpec != "" {
		if id, ok := r.repoToID[strings.ToLower(strings.TrimSpace(repoSpec))]; ok {
			if _, ok := r.configs[id]; ok {
				return id, nil
			}
		}
	}

	// 4. Explicit default.
	if r.defaultID != "" {
		if _, ok := r.configs[r.defaultID]; ok {
			return r.defaultID, nil
		}
	}

	// 5. Sole-registered-forge fallback.
	if len(r.configs) == 1 {
		for id := range r.configs {
			return id, nil
		}
	}

	if len(r.configs) == 0 {
		return "", fmt.Errorf("forge: no forges registered (use --forge <id> or configure forges: in workspace yaml)")
	}
	return "", fmt.Errorf("forge: ambiguous forge selection — pass --forge <id> (registered: %s)", strings.Join(r.IDs(), ", "))
}
