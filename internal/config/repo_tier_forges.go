package config

import (
	"fmt"
	"net"
	"net/url"
	"sort"
	"strings"

	yaml "gopkg.in/yaml.v3"
)

// repoTierForgeMachineFields are the forges.<id> fields that decide where a
// forge credential is sent, which credential it is, and whether TLS is
// verified. They are the operator's, never a repository's: LoadMerged removes
// them from the project and local tiers before the merge, so only the machine
// tier (~/.nightgauge/config.yaml) sets them. The rest of an entry (kind,
// auth_method, owner, project_number, owner_type, default_project_id) stays
// repository-settable.
var repoTierForgeMachineFields = []string{
	"base_url",
	"graphql_url",
	"host",
	"proxy",
	"ca_bundle",
	"token_env",
	"insecure_skip_tls",
}

// stripRepoTierForgeEndpoints removes repoTierForgeMachineFields from every
// forges.<id> entry of a repository-tier YAML document, and reports the
// dotted paths it removed. The forges block is decoded the way the loader
// decodes it (aliases and `<<` merge keys resolved) before the fields are
// removed, so neither can carry one past the strip. A forges block that does
// not decode as a mapping of mappings is removed whole: the loader would
// refuse it anyway, and failing closed keeps an endpoint out.
func stripRepoTierForgeEndpoints(data []byte) ([]byte, []string) {
	if len(data) == 0 {
		return data, nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return data, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return data, nil
	}
	idx := -1
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "forges" {
			idx = i
			break
		}
	}
	if idx < 0 {
		return data, nil
	}
	var entries map[string]map[string]any
	if err := root.Content[idx+1].Decode(&entries); err != nil {
		root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		return marshalOrEmpty(&doc), []string{"forges"}
	}
	var removed []string
	for id, entry := range entries {
		for _, f := range repoTierForgeMachineFields {
			if _, ok := entry[f]; ok {
				delete(entry, f)
				removed = append(removed, "forges."+id+"."+f)
			}
		}
	}
	if len(removed) == 0 {
		return data, nil
	}
	sort.Strings(removed)
	var replacement yaml.Node
	if err := replacement.Encode(entries); err != nil {
		root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		return marshalOrEmpty(&doc), []string{"forges"}
	}
	root.Content[idx+1] = &replacement
	out, err := yaml.Marshal(&doc)
	if err != nil {
		// An anchor elsewhere in the file pointed into the replaced block.
		root.Content = append(root.Content[:idx], root.Content[idx+2:]...)
		return marshalOrEmpty(&doc), []string{"forges"}
	}
	return out, removed
}

// marshalOrEmpty serializes doc, or returns an empty document when it cannot
// be serialized, so a strip never falls back to the unstripped bytes.
func marshalOrEmpty(doc *yaml.Node) []byte {
	out, err := yaml.Marshal(doc)
	if err != nil {
		return []byte{}
	}
	return out
}

// warnStrippedForgeFields reports each forge field a repository tier set and
// LoadMerged removed.
func warnStrippedForgeFields(removed []string, tier, path string) {
	for _, p := range removed {
		warnShadowOnce("ignored:"+tier+":"+p, fmt.Sprintf(
			"WARN config: %s is in %s YAML (%s) but forge endpoints, token variables and TLS settings are owned by the machine tier (~/.nightgauge/config.yaml). The %s value is IGNORED. See docs/SETTINGS_ARCHITECTURE.md.",
			p, tier, path, tier))
	}
}

// clearLegacyForgeEndpoints applies the same rule to the legacy
// .nightgauge/config.json, already decoded into forges.
func clearLegacyForgeEndpoints(forges map[string]*ForgeConfigEntry) {
	for _, e := range forges {
		if e == nil {
			continue
		}
		e.BaseURL, e.GraphQLURL, e.Host, e.Proxy, e.CABundle, e.TokenEnv = "", "", "", "", "", ""
		e.InsecureSkipTLS = false
	}
}

// ValidateForgeEndpointURL checks a URL a forge credential is sent to: an
// absolute https: URL with a host and no userinfo. Plain http: is accepted
// only for a loopback host (localhost, 127.0.0.0/8, ::1), so a local test
// server works and a credential never crosses a network in clear text.
func ValidateForgeEndpointURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("not a valid URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("must be an absolute https:// URL with a host")
	}
	if u.User != nil {
		return fmt.Errorf("must not carry credentials in the URL")
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("must use https:// (http:// is accepted only for a loopback host)")
	default:
		return fmt.Errorf("must use https://, got scheme %q", u.Scheme)
	}
}

// ValidateForgeHost checks a forge host setting: either a bare host name
// with an optional port, or a URL that passes ValidateForgeEndpointURL.
func ValidateForgeHost(host string) error {
	h := strings.TrimSpace(host)
	if h == "" {
		return nil
	}
	if strings.Contains(h, "://") {
		return ValidateForgeEndpointURL(h)
	}
	if strings.ContainsAny(h, "/@?# \t") {
		return fmt.Errorf("must be a host name, optionally with a port")
	}
	name := h
	if hn, _, err := net.SplitHostPort(h); err == nil {
		name = hn
	}
	if name == "" {
		return fmt.Errorf("must be a host name, optionally with a port")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
