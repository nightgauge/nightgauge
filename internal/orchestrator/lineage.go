package orchestrator

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nightgauge/nightgauge/internal/adaptercompat"
	"github.com/nightgauge/nightgauge/internal/atomicfile"
	"github.com/nightgauge/nightgauge/internal/models"
)

// Session lineage (#55). A lineage is the run of consecutive dispatches that
// can share one warm prompt cache. It breaks — and a handoff is recorded — on
// the first of these that differs, checked in this fixed precedence.
const (
	LineageAccountChange     = "account_change"
	LineageRepoChange        = "repo_change"
	LineageIssueChange       = "issue_change"
	LineageProviderChange    = "provider_change"
	LineageModelFamilyChange = "model_family_change"
	LineageEffortClassChange = "effort_class_change"
	LineageExpired           = "expired"
)

// LineageKey is what a lineage is made of. AccountHash is a salted workspace
// hash and is never written to history; the lineage id derived from the key
// is safe to persist.
type LineageKey struct {
	Provider    string
	AccountHash string
	ModelFamily string
	EffortClass string
	Repo        string
	Issue       int
	RunID       string
}

func (k LineageKey) canonical() string {
	return strings.Join([]string{k.Provider, k.AccountHash, k.ModelFamily, k.EffortClass,
		k.Repo, fmt.Sprint(k.Issue), k.RunID}, "\x1f")
}

// NextLineageID is the id of a dispatch with key next: prevID when the
// lineage continues, otherwise a new id chained to prevID, so a return to an
// earlier key (A → B → A) and an expiry each name a lineage of their own.
func NextLineageID(prevID string, next LineageKey, t LineageTransition) string {
	if t.Same && prevID != "" {
		return prevID
	}
	sum := sha256.Sum256([]byte(next.canonical() + "\x1e" + prevID))
	return "lin-" + hex.EncodeToString(sum[:])[:12]
}

// LineageTransition reports whether next continues prev's lineage and, when
// not, the first reason it does not.
type LineageTransition struct {
	Same   bool
	Reason string
}

// CompareLineage compares two consecutive dispatches. idle is the time since
// the previous dispatch's stage last used the cache. A model change the
// adapter declares compatible (same_family over one family) keeps the lineage.
// An account is an identity on one provider, so a provider switch — which
// always changes the account hash — reports provider_change, not
// account_change.
func CompareLineage(prev, next LineageKey, caps adaptercompat.CacheCaps, idle time.Duration) LineageTransition {
	switch {
	case prev.Provider == next.Provider && prev.AccountHash != next.AccountHash:
		return LineageTransition{Reason: LineageAccountChange}
	case prev.Repo != next.Repo:
		return LineageTransition{Reason: LineageRepoChange}
	case prev.Issue != next.Issue:
		return LineageTransition{Reason: LineageIssueChange}
	case prev.Provider != next.Provider:
		return LineageTransition{Reason: LineageProviderChange}
	case prev.ModelFamily != next.ModelFamily && !compatibleModels(prev.ModelFamily, next.ModelFamily, caps):
		return LineageTransition{Reason: LineageModelFamilyChange}
	case prev.EffortClass != next.EffortClass:
		return LineageTransition{Reason: LineageEffortClassChange}
	case caps.ExpirySeconds > 0 && idle > time.Duration(caps.ExpirySeconds)*time.Second:
		return LineageTransition{Reason: LineageExpired}
	}
	return LineageTransition{Same: true}
}

// compatibleModels reports a same_family transition: two concrete ids of one
// family share the prefix before their version ("claude-sonnet-5" and
// "claude-sonnet-5-5"). Anything the adapter does not declare is incompatible.
func compatibleModels(a, b string, caps adaptercompat.CacheCaps) bool {
	if caps.CompatibleModelTransitions != adaptercompat.TransitionsSameFamily {
		return false
	}
	return modelFamilyStem(a) != "" && modelFamilyStem(a) == modelFamilyStem(b)
}

func modelFamilyStem(id string) string {
	parts := strings.Split(id, "-")
	for len(parts) > 0 && strings.Trim(parts[len(parts)-1], "0123456789.") == "" {
		parts = parts[:len(parts)-1]
	}
	return strings.Join(parts, "-")
}

// ResumeAllowed fails closed: a session recorded under another account,
// repository, issue, run or provider is never resumed.
func ResumeAllowed(prev, next LineageKey) bool {
	return prev.AccountHash != "" && prev.AccountHash == next.AccountHash &&
		prev.Repo == next.Repo && prev.Issue == next.Issue &&
		prev.RunID == next.RunID && prev.Provider == next.Provider
}

// EffortClass collapses an effort rung onto the class a cache depends on.
func EffortClass(effort string) string {
	switch effort {
	case "low", "medium":
		return "light"
	case "high", "xhigh", "max":
		return "deep"
	}
	return ""
}

// modelFamily is the registry's concrete id for model on provider, or the
// model as given when the registry does not resolve it.
func modelFamily(provider, model string) string {
	if d, ok := models.Resolve(provider, model); ok {
		return d.ID
	}
	return model
}

// lineageSaltFile is the per-clone random salt the identity hash is keyed
// with. Local state only, mode 0600, never committed or synced.
const lineageSaltFile = "lineage-salt"

// workspaceIdentityHash is HMAC-SHA256(salt, descriptor), creating the salt
// on first use. descriptor names an identity (adapter, auth mode, credential
// home) and never carries a credential.
func workspaceIdentityHash(stateDir, descriptor string) (string, error) {
	if stateDir == "" {
		return "", errors.New("lineage: no state directory")
	}
	path := filepath.Join(stateDir, lineageSaltFile)
	salt, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && len(salt) != 32) {
		salt = make([]byte, 32)
		if _, err := rand.Read(salt); err != nil {
			return "", err
		}
		if err := atomicfile.Write(path, salt, 0o600); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, salt)
	mac.Write([]byte(descriptor))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// credentialEnv is, per provider, the env vars that select WHICH account a
// CLI runs as: an API key's presence (never its value) and a credential home.
var credentialEnv = map[string]struct{ apiKey, home []string }{
	"anthropic": {[]string{"ANTHROPIC_API_KEY"}, []string{"CLAUDE_CONFIG_DIR"}},
	"openai":    {[]string{"OPENAI_API_KEY", "CODEX_API_KEY"}, []string{"CODEX_HOME"}},
	"google":    {[]string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, []string{"GEMINI_CLI_HOME"}},
	"xai":       {[]string{"XAI_API_KEY"}, []string{"GROK_HOME"}},
	"copilot":   {[]string{"GH_TOKEN", "GITHUB_TOKEN"}, []string{"COPILOT_HOME"}},
}

// identityDescriptor names the account an adapter runs as without carrying
// any secret: adapter, auth mode (api-key or subscription) and credential
// home path.
func identityDescriptor(adapter string, getenv func(string) string) string {
	env := credentialEnv[models.ProviderForAdapter(adapter)]
	mode := "subscription"
	for _, k := range env.apiKey {
		if getenv(k) != "" {
			mode = "api-key"
			break
		}
	}
	home := ""
	for _, k := range env.home {
		if v := getenv(k); v != "" {
			home = v
			break
		}
	}
	return strings.Join([]string{"provider=" + models.ProviderForAdapter(adapter), "adapter=" + adapter, "auth=" + mode, "home=" + home}, ";")
}
