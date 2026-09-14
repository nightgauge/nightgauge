// Package adaptercompat is the single source of what Nightgauge knows about
// each coding CLI's versions: the floor, the newest version anyone tested, the
// upstream feeds that announce a release, how the CLI is installed, and which
// captured fixtures back the parsers that read it.
//
// There is one manifest per CLI adapter, embedded from manifests/<adapter>.json.
// SDK and HTTP adapters (claude-sdk, gemini-sdk, lm-studio, ollama) spawn no
// binary of their own and have no manifest.
//
// Every manifest is decoded strictly and validated before any caller sees it.
// Nothing here runs a command: install recipes, catalog commands and feed
// references are data. Callers may print them as remediation text; they must
// never hand one to a shell or start a process with it.
package adaptercompat

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// ManifestDir is where the manifests live, relative to the repository root.
// LoadDir reads the manifests under this directory of the filesystem it is
// given, so that filesystem is rooted at the repository root and the fixture
// paths the manifests list resolve against it.
const ManifestDir = "internal/adaptercompat/manifests"

//go:embed manifests/*.json
var embedded embed.FS

// Floor policies. A floor under FloorWarn reports a CLI below it and keeps the
// adapter usable; a floor under FloorFailClosed makes the adapter unusable.
const (
	FloorWarn       = "warn"
	FloorFailClosed = "fail_closed"
)

// Feed kinds: where an upstream release is announced.
const (
	FeedGitHub    = "github"    // Ref is owner/repo; releases are read from its tags
	FeedNPM       = "npm"       // Ref is the npm package name
	FeedInstaller = "installer" // Ref is the https URL of the vendor's install script
)

// Manifest is one CLI adapter's compatibility record.
type Manifest struct {
	Adapter string `json:"adapter"`
	Binary  string `json:"binary"`

	// MinVersion is the oldest version Nightgauge supports. Empty means no
	// floor, and MinVersionReason must then say why.
	MinVersion       string `json:"min_version"`
	MinVersionReason string `json:"min_version_reason,omitempty"`
	FloorPolicy      string `json:"floor_policy"`

	// MaxTested is the newest version anyone verified Nightgauge against.
	// Empty means none was, and MaxTestedReason must then say why.
	MaxTested       string `json:"max_tested"`
	MaxTestedReason string `json:"max_tested_reason,omitempty"`

	Feeds []Feed `json:"feeds"`
	// CompanionFeeds are upstreams the CLI depends on at run time without
	// shipping them, such as the local model servers OpenCode drives.
	CompanionFeeds []Feed `json:"companion_feeds"`

	Install Install `json:"install"`
	// ManagedInstall is a display-only install recipe. It is printed, never run.
	ManagedInstall string `json:"managed_install"`

	RequiredFlags []string `json:"required_flags"`
	Catalog       Catalog  `json:"catalog"`
	// Fixtures are repository-relative paths of the captured files that back
	// the parsers reading this CLI's output.
	Fixtures []string `json:"fixtures"`

	// OpenCode-only. ConfigSchemaSHA256 is the SHA-256 of the OpenCode config
	// schema pinned for max_tested, which every generated per-run config is
	// validated against (internal/execution/adapters/schema_contract_test.go).
	// PluginAPIVersion and ExperimentalHooks stay empty until the plugin
	// scaffold and the plugin events fill them.
	ConfigSchemaSHA256 string   `json:"config_schema_sha256"`
	PluginAPIVersion   string   `json:"plugin_api_version"`
	ExperimentalHooks  []string `json:"experimental_hooks"`
}

// Feed names one upstream that announces releases.
type Feed struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	// TagPattern, for a github feed, is an anchored regular expression a tag
	// must match to count as a release. Repositories tag more than releases.
	TagPattern string `json:"tag_pattern,omitempty"`
}

// Install is how the CLI is installed: exactly one of NPM (a package name)
// or Installer (an https URL) is set. Display-only.
type Install struct {
	NPM       string `json:"npm,omitempty"`
	Installer string `json:"installer,omitempty"`
}

// Catalog is how the CLI lists the models it serves: exactly one of Command
// (display-only) or SkipReason (why there is no listing to read) is set.
type Catalog struct {
	Command    string `json:"command,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
}

// Error reports a manifest that failed to load. It names the manifest file,
// the adapter and the offending field.
type Error struct {
	File    string // manifest file name, e.g. codex.json
	Adapter string // the adapter the file is for
	Field   string // the JSON field at fault; empty when the file is not valid JSON
	Msg     string
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "adaptercompat: manifest %s (adapter %q)", e.File, e.Adapter)
	if e.Field != "" {
		fmt.Fprintf(&b, ": field %s", e.Field)
	}
	b.WriteString(": ")
	b.WriteString(e.Msg)
	return b.String()
}

// Load decodes and validates the embedded manifests, sorted by adapter.
//
// A shipped binary has no repository tree, so Load checks the shape of each
// fixture path but not that the file exists; LoadDir does both, and the
// package tests run it over the repository to hold the embedded set to that.
func Load() ([]Manifest, error) {
	sub, err := fs.Sub(embedded, "manifests")
	if err != nil {
		return nil, fmt.Errorf("adaptercompat: embedded manifests: %w", err)
	}
	return load(sub, nil)
}

// LoadDir decodes and validates the manifests under ManifestDir of tree, a
// filesystem rooted at the repository root, and refuses any fixture that does
// not exist in tree.
func LoadDir(tree fs.FS) ([]Manifest, error) {
	sub, err := fs.Sub(tree, ManifestDir)
	if err != nil {
		return nil, fmt.Errorf("adaptercompat: %s: %w", ManifestDir, err)
	}
	return load(sub, tree)
}

var embeddedIndex = sync.OnceValues(func() (map[string]Manifest, error) {
	ms, err := Load()
	if err != nil {
		return nil, err
	}
	idx := make(map[string]Manifest, len(ms))
	for _, m := range ms {
		idx[m.Adapter] = m
	}
	return idx, nil
})

// Get returns the embedded manifest for adapter. It reports false when there
// is none, and when the embedded set failed to load; Load returns that error.
// The returned value shares its slices with every other caller: read only.
func Get(adapter string) (Manifest, bool) {
	idx, err := embeddedIndex()
	if err != nil {
		return Manifest{}, false
	}
	m, ok := idx[adapter]
	return m, ok
}

// load reads every manifest in dir. tree, when non-nil, is the repository
// root the fixture paths must exist in.
func load(dir fs.FS, tree fs.FS) ([]Manifest, error) {
	entries, err := fs.ReadDir(dir, ".")
	if err != nil {
		return nil, fmt.Errorf("adaptercompat: read manifests: %w", err)
	}
	var out []Manifest
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".json" {
			return nil, fmt.Errorf("adaptercompat: manifests directory holds %q; only <adapter>.json files belong there", name)
		}
		data, err := fs.ReadFile(dir, name)
		if err != nil {
			return nil, fmt.Errorf("adaptercompat: read %s: %w", name, err)
		}
		m, err := decode(name, data)
		if err != nil {
			return nil, err
		}
		if err := m.validate(name, tree); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, errors.New("adaptercompat: no manifests found")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Adapter < out[j].Adapter })
	return out, nil
}

// decode parses one manifest, refusing unknown keys and trailing data.
func decode(file string, data []byte) (Manifest, error) {
	adapter := strings.TrimSuffix(file, ".json")
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, &Error{File: file, Adapter: adapter, Field: decodeErrorField(err), Msg: err.Error()}
	}
	if _, err := dec.Token(); err != io.EOF {
		return Manifest{}, &Error{File: file, Adapter: adapter, Msg: "trailing data after the manifest object"}
	}
	return m, nil
}

// decodeErrorField names the field a decode error is about, when it has one.
func decodeErrorField(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return typeErr.Field
	}
	const unknown = "json: unknown field "
	if msg := err.Error(); strings.HasPrefix(msg, unknown) {
		if f, uerr := strconv.Unquote(strings.TrimPrefix(msg, unknown)); uerr == nil {
			return f
		}
	}
	return ""
}

var (
	semverRe    = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	npmNameRe   = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)
	githubRepRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)
	httpsURLRe  = regexp.MustCompile(`^https://[A-Za-z0-9.-]+(/[A-Za-z0-9._~/-]*)?$`)
	binaryRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	flagRe      = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9-]*$`)
	sha256Re    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func (m Manifest) validate(file string, tree fs.FS) error {
	stem := strings.TrimSuffix(file, ".json")
	fail := func(field, format string, args ...any) error {
		return &Error{File: file, Adapter: stem, Field: field, Msg: fmt.Sprintf(format, args...)}
	}

	if m.Adapter != stem {
		return fail("adapter", "%q does not match the file name; the file must be %s.json", m.Adapter, m.Adapter)
	}
	if !binaryRe.MatchString(m.Binary) {
		return fail("binary", "%q is not a binary name", m.Binary)
	}

	if err := checkVersion(m.MinVersion, m.MinVersionReason, "min_version", fail); err != nil {
		return err
	}
	if err := checkVersion(m.MaxTested, m.MaxTestedReason, "max_tested", fail); err != nil {
		return err
	}
	if m.MinVersion != "" && m.MaxTested != "" && compareSemver(m.MinVersion, m.MaxTested) > 0 {
		return fail("min_version", "%s is greater than max_tested %s", m.MinVersion, m.MaxTested)
	}
	if m.FloorPolicy != FloorWarn && m.FloorPolicy != FloorFailClosed {
		return fail("floor_policy", "%q is not %q or %q", m.FloorPolicy, FloorWarn, FloorFailClosed)
	}

	if len(m.Feeds) == 0 {
		return fail("feeds", "is empty; at least one upstream feed is required")
	}
	for i, f := range m.Feeds {
		if err := checkFeed(f, fmt.Sprintf("feeds[%d]", i), fail); err != nil {
			return err
		}
	}
	for i, f := range m.CompanionFeeds {
		if err := checkFeed(f, fmt.Sprintf("companion_feeds[%d]", i), fail); err != nil {
			return err
		}
	}

	switch {
	case (m.Install.NPM == "") == (m.Install.Installer == ""):
		return fail("install", "set exactly one of npm or installer")
	case m.Install.NPM != "" && !npmNameRe.MatchString(m.Install.NPM):
		return fail("install.npm", "%q is not an npm package name", m.Install.NPM)
	case m.Install.Installer != "" && !httpsURLRe.MatchString(m.Install.Installer):
		return fail("install.installer", "%q is not a plain https URL", m.Install.Installer)
	}
	if strings.IndexFunc(m.ManagedInstall, unicode.IsControl) >= 0 {
		return fail("managed_install", "contains a control character; it is one line of display text")
	}

	for i, fl := range m.RequiredFlags {
		if !flagRe.MatchString(fl) {
			return fail(fmt.Sprintf("required_flags[%d]", i), "%q is not a command-line flag", fl)
		}
	}
	if (strings.TrimSpace(m.Catalog.Command) == "") == (strings.TrimSpace(m.Catalog.SkipReason) == "") {
		return fail("catalog", "set exactly one of command or skip_reason")
	}
	for i, p := range m.Fixtures {
		if err := checkFixture(p, tree); err != nil {
			return fail(fmt.Sprintf("fixtures[%d]", i), "%q %v", p, err)
		}
	}

	if m.ConfigSchemaSHA256 != "" && !sha256Re.MatchString(m.ConfigSchemaSHA256) {
		return fail("config_schema_sha256", "%q is not a lowercase hex SHA-256", m.ConfigSchemaSHA256)
	}
	return nil
}

type failFunc func(field, format string, args ...any) error

func checkVersion(version, reason, field string, fail failFunc) error {
	if version == "" {
		if strings.TrimSpace(reason) == "" {
			return fail(field, "is empty and %s_reason does not say why", field)
		}
		return nil
	}
	if !semverRe.MatchString(version) {
		return fail(field, "%q is not a semver version (MAJOR.MINOR.PATCH)", version)
	}
	return nil
}

func checkFeed(f Feed, field string, fail failFunc) error {
	switch f.Kind {
	case FeedNPM:
		if !npmNameRe.MatchString(f.Ref) {
			return fail(field+".ref", "%q is not an npm package name", f.Ref)
		}
	case FeedGitHub:
		if !githubRepRe.MatchString(f.Ref) {
			return fail(field+".ref", "%q is not an owner/repo", f.Ref)
		}
	case FeedInstaller:
		if !httpsURLRe.MatchString(f.Ref) {
			return fail(field+".ref", "%q is not a plain https URL", f.Ref)
		}
	default:
		return fail(field+".kind", "%q is not %q, %q or %q", f.Kind, FeedGitHub, FeedNPM, FeedInstaller)
	}
	if f.TagPattern == "" {
		return nil
	}
	if f.Kind != FeedGitHub {
		return fail(field+".tag_pattern", "applies to github feeds only")
	}
	if !strings.HasPrefix(f.TagPattern, "^") || !strings.HasSuffix(f.TagPattern, "$") {
		return fail(field+".tag_pattern", "%q must be anchored with ^ and $", f.TagPattern)
	}
	if _, err := regexp.Compile(f.TagPattern); err != nil {
		return fail(field+".tag_pattern", "%q does not compile: %v", f.TagPattern, err)
	}
	return nil
}

// checkFixture refuses a fixture path that could point outside the
// repository, and, when tree is non-nil, one that does not exist in it.
func checkFixture(p string, tree fs.FS) error {
	switch {
	case p == "":
		return errors.New("is empty")
	case strings.HasPrefix(p, "/") || filepath.IsAbs(p) || filepath.VolumeName(p) != "":
		return errors.New("is absolute; fixtures are repository-relative")
	case strings.Contains(p, ".."):
		return errors.New("contains ..; fixtures stay inside the repository")
	case strings.Contains(p, `\`) || !fs.ValidPath(p):
		return errors.New("is not a clean slash-separated repository path")
	}
	if tree == nil {
		return nil
	}
	info, err := fs.Stat(tree, p)
	if err != nil {
		return errors.New("does not exist in the tree")
	}
	if !info.Mode().IsRegular() {
		return errors.New("is not a regular file")
	}
	return nil
}

// compareSemver compares two versions semverRe already accepted.
func compareSemver(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
