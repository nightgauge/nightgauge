package forgecmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// commandRunner wraps the exec.Command surface needed by readGHToken.
// Tests substitute a fake to avoid spawning real processes.
type commandRunner interface {
	Output() ([]byte, error)
}

var newCommand = func(name string, args ...string) commandRunner {
	return exec.Command(name, args...)
}

func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Token status and config-file token management",
		Long:  longAuth,
	}
	cmd.AddCommand(authStatusCmd(), authLoginCmd(), authLogoutCmd(), authRefreshCmd(), authWhoamiCmd(), authTokenCmd(), authAssertCmd())
	return cmd
}

// authAssertCmd is the deterministic preflight permission assertion (#4068).
// It resolves the per-repo identity, confirms the EFFECTIVE login matches the
// configured github_user, and confirms push (and optionally admin) on the
// target repo — failing loudly with a one-line remediation when the resolved
// identity is wrong or lacks the required access, rather than producing an
// un-mergeable PR later.
func authAssertCmd() *cobra.Command {
	var requireAdmin bool
	cmd := &cobra.Command{
		Use:          "assert",
		Short:        "Assert the resolved per-repo identity matches github_user and has push (--admin for admin)",
		Long:         longAuthAssert,
		SilenceUsage: true,
		Example:      `  nightgauge forge auth assert --repo Acme-Community/acmesvc-tracker`,
		RunE: func(cmd *cobra.Command, args []string) error {
			owner, repo, err := parseRepo(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			res, err := assertIdentity(cmd.Context(), owner, repo, requireAdmin)
			if err != nil {
				return emitError(cmd, fmt.Errorf("auth assert: %w", err))
			}
			// Render the structured result (JSON or human) first so callers
			// always get the full picture, then fail the command (non-zero
			// exit) when the assertion did not pass.
			if rErr := renderForCmd(cmd, res); rErr != nil {
				return rErr
			}
			if !res.OK {
				// Stable, machine-greppable failure line on stderr in addition
				// to the rendered body; the returned error drives the non-zero
				// exit code without re-printing the (already rendered) body.
				fmt.Fprintf(cmd.ErrOrStderr(), "error: identity assertion failed: %s\n", res.Reason)
				if res.Remediation != "" {
					fmt.Fprintf(cmd.ErrOrStderr(), "remediation: %s\n", res.Remediation)
				}
				return errIdentityAssertionFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&requireAdmin, "admin", false, "Also require admin permission (ruleset/branch-protection bypass)")
	return cmd
}

// errIdentityAssertionFailed is returned by `forge auth assert` to drive a
// non-zero exit when the resolved identity is wrong or lacks the required
// access. The human-readable reason/remediation are printed to stderr by the
// command; this sentinel is intentionally terse so cobra does not re-print a
// noisy usage block (SilenceUsage is set).
var errIdentityAssertionFailed = errors.New("identity assertion failed")

// assertIdentity is the package-level worker swapped by tests. The production
// implementation (realAssertIdentity) resolves the configured github_user for
// the target owner, builds a client via the standard resolution chain, and
// checks Whoami + repo write/admin access. Tests reassign this to avoid network
// I/O and exercise the verb's pass/fail/remediation branches.
var assertIdentity = realAssertIdentity

// realAssertIdentity performs the live identity + permission check.
func realAssertIdentity(ctx context.Context, owner, repo string, requireAdmin bool) (AuthAssertJSON, error) {
	out := AuthAssertJSON{
		V:             1,
		Repo:          owner + "/" + repo,
		AdminRequired: requireAdmin,
	}

	wd, err := os.Getwd()
	if err != nil {
		return out, fmt.Errorf("getwd: %w", err)
	}
	cfg, err := config.Load(wd)
	if err != nil || cfg == nil {
		cfg = &config.Config{}
	}

	expected := cfg.ResolveGitHubUserForOwner(owner)
	out.ExpectedLogin = expected
	if expected == "" {
		// No configured identity for this owner — nothing to assert. Treat as a
		// pass so single-identity repos are unaffected; the scheduler/guard skip
		// the same way.
		out.OK = true
		out.Reason = "no github_user configured for owner — identity assertion skipped"
		return out, nil
	}

	client, err := gh.NewClientFromConfig(cfg, owner, "")
	if err != nil {
		return out, fmt.Errorf("resolve client for %s: %w", expected, err)
	}

	actor, err := client.Whoami(ctx)
	if err != nil {
		return out, fmt.Errorf("whoami: %w", err)
	}
	out.ActualLogin = actor.Login

	if !strings.EqualFold(actor.Login, expected) {
		out.OK = false
		out.Reason = fmt.Sprintf("resolved identity is %q but config expects %q for owner %q", actor.Login, expected, owner)
		out.Remediation = identityRemediation(expected)
		return out, nil
	}

	hasWrite, err := client.HasRepoWriteAccess(ctx, expected, owner, repo)
	if err != nil {
		// Fail-closed: a permission-read failure is a denial, but surface the
		// actionable cause rather than a generic "permission check failed".
		out.OK = false
		out.Reason = fmt.Sprintf("could not read collaborator permission for %q on %s/%s (token may lack access to query it): %v", expected, owner, repo, err)
		out.Remediation = identityRemediation(expected)
		return out, nil
	}
	out.HasPush = hasWrite
	// HasRepoWriteAccess returns true for both "write" and "admin"; resolve
	// the precise level only when admin is required (one extra branch keeps
	// the common push-only path to a single REST call).
	if !hasWrite {
		out.OK = false
		out.Reason = fmt.Sprintf("identity %q lacks push access on %s/%s", expected, owner, repo)
		out.Remediation = fmt.Sprintf("grant %q write (or admin) on %s/%s, or set github_user to a collaborator who has it", expected, owner, repo)
		return out, nil
	}

	if requireAdmin {
		hasAdmin, err := client.HasRepoAdminAccess(ctx, expected, owner, repo)
		if err != nil {
			out.OK = false
			out.Reason = fmt.Sprintf("could not read admin permission for %q on %s/%s: %v", expected, owner, repo, err)
			out.Remediation = identityRemediation(expected)
			return out, nil
		}
		out.HasAdmin = hasAdmin
		if !hasAdmin {
			out.OK = false
			out.Reason = fmt.Sprintf("identity %q lacks admin access on %s/%s (required to bypass a review ruleset)", expected, owner, repo)
			out.Remediation = fmt.Sprintf("grant %q admin on %s/%s, or remove the required-review ruleset bypass dependency", expected, owner, repo)
			return out, nil
		}
	}

	out.OK = true
	return out, nil
}

// identityRemediation returns the canonical one-line fix for a wrong/unauthorized
// identity: fetch the configured user's token with ambient env stripped.
func identityRemediation(user string) string {
	return fmt.Sprintf("run: GH_TOKEN=$(env -u GH_TOKEN -u GITHUB_TOKEN gh auth token --user %s) gh ... (or authenticate gh for %q: gh auth login --user %s)", user, user, user)
}

// authTokenCmd prints the resolved GitHub token to stdout via the standard
// resolution chain (config github_auth.token → GITHUB_TOKEN env → `gh auth
// token --user <github_user>`). This is the canonical way for skills and the
// VSCode extension host to obtain the pipeline's identity token for their own
// `gh`/`gh api` subprocesses, so they authenticate as the configured user
// rather than the machine's ambient active gh account (#3892). Pipeline skills
// already reference `nightgauge forge auth token`; this provides it.
//
// Only the bare token is written to stdout (no JSON), so it composes cleanly:
//
//	export GITHUB_TOKEN=$(nightgauge forge auth token)
func authTokenCmd() *cobra.Command {
	var ownerOverride string
	var identityOnly bool
	cmd := &cobra.Command{
		Use:          "token",
		Short:        "Print the resolved GitHub token (config → GITHUB_TOKEN → gh auth token --user)",
		Long:         longAuthToken,
		SilenceUsage: true,
		Example:      `  export GITHUB_TOKEN=$(nightgauge forge auth token)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("getwd: %w", err)
			}
			cfg, err := config.Load(wd)
			if err != nil || cfg == nil {
				cfg = &config.Config{}
			}
			owner := ownerOverride
			if owner == "" {
				owner = cfg.Owner
			}
			// --identity-only: emit a token ONLY when the owner declares a
			// per-repo identity (github_user). For a repo with no configured
			// identity, print nothing and exit 0 so the caller (guard.sh) keeps
			// whatever ambient GH_TOKEN it already has — never overriding a
			// correctly-injected ambient token with the machine's default gh
			// account (#4068). Resolution failures are likewise silent so the
			// caller falls back to the ambient value.
			if identityOnly {
				if cfg.ResolveGitHubUserForOwner(owner) == "" {
					return nil
				}
				if token, terr := gh.ResolveTokenChain(cfg, owner); terr == nil && token != "" {
					fmt.Fprintln(cmd.OutOrStdout(), token)
				}
				return nil
			}
			token, err := gh.ResolveTokenChain(cfg, owner)
			if err != nil {
				return fmt.Errorf("resolve github token: %w", err)
			}
			if token == "" {
				return fmt.Errorf("no github token resolved (set github_auth.token, GITHUB_TOKEN, or authenticate gh for the configured github_user)")
			}
			fmt.Fprintln(cmd.OutOrStdout(), token)
			return nil
		},
	}
	cmd.Flags().StringVar(&ownerOverride, "owner", "", "Resolve a per-owner token (defaults to the config owner)")
	cmd.Flags().BoolVar(&identityOnly, "identity-only", false, "Emit a token only when the repo configures a per-repo github_user; print nothing otherwise (guard.sh uses this to override only configured-identity repos)")
	return cmd
}

func authWhoamiCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "whoami",
		Short:        "Print the login of the currently authenticated actor",
		Long:         longAuthWhoami,
		SilenceUsage: true,
		Example:      `  nightgauge forge auth whoami --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			actor, err := client.Auth().Whoami(cmd.Context())
			if err != nil {
				return emitError(cmd, fmt.Errorf("whoami: %w", err))
			}
			return renderForCmd(cmd, ActorFromForge(actor))
		},
	}
	return cmd
}

func authStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "status",
		Short:        "Report token validity, scopes, and resolution source",
		Long:         longAuthStatus,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := forgeFromContext(cmd)
			if err != nil {
				return emitError(cmd, err)
			}
			info, err := client.Auth().CheckTokenScopes(cmd.Context())
			if err != nil {
				return emitError(cmd, fmt.Errorf("check token scopes: %w", err))
			}
			source, masked := authSourceAndMaskedToken()
			if info.Resolution != "" {
				source = info.Resolution
			}
			out := AuthStatusJSON{
				V:             1,
				Login:         info.Login,
				Scopes:        info.Scopes,
				Missing:       info.MissingScopes,
				MaskedToken:   masked,
				Source:        source,
				OrgMembership: info.OrgMemberships,
				Valid:         info.Valid,
			}
			return renderForCmd(cmd, out)
		},
	}
	return cmd
}

func authLoginCmd() *cobra.Command {
	var (
		token     string
		fromStdin bool
	)
	cmd := &cobra.Command{
		Use:          "login",
		Short:        "Store a token in OS credential storage",
		Long:         longAuthLogin,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !fromStdin && token == "" {
				return emitError(cmd, fmt.Errorf("provide --token <value> or --from-stdin"))
			}
			t := token
			if fromStdin {
				v, err := readTokenFromStdin(cmd.InOrStdin())
				if err != nil {
					return emitError(cmd, fmt.Errorf("read token from stdin: %w", err))
				}
				t = v
			}
			t = strings.TrimSpace(t)
			if t == "" {
				return emitError(cmd, fmt.Errorf("token is empty"))
			}
			if err := storeTokenInKeyring(t); err != nil {
				return emitError(cmd, fmt.Errorf("store token in OS credential storage: %w", err))
			}
			paths, err := scrubPlaintextCredentials()
			if err != nil {
				return emitError(cmd, fmt.Errorf("scrub plaintext configuration: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v":           1,
				"loggedIn":    true,
				"masked":      MaskToken(t),
				"storage":     "os-keyring",
				"scrubbed":    paths,
				"warning":     "",
				"warningHint": "Run 'nightgauge forge auth status' to validate scopes.",
			})
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Token value (avoid in shells; prefer --from-stdin)")
	cmd.Flags().BoolVar(&fromStdin, "from-stdin", false, "Read token from standard input")
	return cmd
}

func authLogoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "logout",
		Short:        "Clear the token from OS credential storage",
		Long:         longAuthLogout,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := removeTokenFromKeyring(); err != nil {
				return emitError(cmd, fmt.Errorf("clear OS credential storage: %w", err))
			}
			paths, err := scrubPlaintextCredentials()
			if err != nil {
				return emitError(cmd, fmt.Errorf("scrub plaintext configuration: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v":         1,
				"loggedOut": true,
				"cleared":   true,
				"scrubbed":  paths,
			})
		},
	}
	return cmd
}

func authRefreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "refresh",
		Short:        "Refresh the token in OS credential storage",
		Long:         longAuthRefresh,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := readGHToken()
			if err != nil {
				return emitError(cmd, fmt.Errorf("read gh token: %w", err))
			}
			t = strings.TrimSpace(t)
			if t == "" {
				return emitError(cmd, fmt.Errorf("gh CLI returned an empty token — run 'gh auth login' first"))
			}
			if err := storeTokenInKeyring(t); err != nil {
				return emitError(cmd, fmt.Errorf("refresh OS credential storage: %w", err))
			}
			paths, err := scrubPlaintextCredentials()
			if err != nil {
				return emitError(cmd, fmt.Errorf("scrub plaintext configuration: %w", err))
			}
			return renderForCmd(cmd, map[string]any{
				"v":         1,
				"refreshed": true,
				"masked":    MaskToken(t),
				"storage":   "os-keyring",
				"scrubbed":  paths,
			})
		},
	}
	return cmd
}

var storeTokenInKeyring = func(token string) error {
	cmd := exec.Command("gh", "auth", "login", "--hostname", "github.com", "--with-token")
	cmd.Stdin = strings.NewReader(token + "\n")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh auth login failed")
	}
	return nil
}

var removeTokenFromKeyring = func() error {
	cmd := exec.Command("gh", "auth", "logout", "--hostname", "github.com")
	if err := cmd.Run(); err != nil {
		// Logout is intentionally idempotent: an absent keyring entry is already safe.
		return nil
	}
	return nil
}

// scrubPlaintextCredentials atomically removes literal credentials from every
// YAML tier reachable by the CLI. Environment references are configuration,
// not credentials, and are preserved.
var scrubPlaintextCredentials = func() ([]string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	paths := []string{
		filepath.Join(wd, ".nightgauge", "config.yaml"),
		filepath.Join(wd, ".nightgauge", "config.local.yaml"),
	}
	if machinePath, pathErr := config.MachineConfigPath(); pathErr == nil {
		paths = append(paths, machinePath)
	}
	var changed []string
	for _, path := range paths {
		ok, scrubErr := scrubCredentialFile(path)
		if scrubErr != nil {
			return changed, scrubErr
		}
		if ok {
			changed = append(changed, path)
		}
	}
	return changed, nil
}

func scrubCredentialFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return false, fmt.Errorf("parse %s: %w", path, err)
	}
	root := &doc
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
	}
	changed := deleteLiteralAt(root, []string{"github_auth", "token"})
	changed = deleteLiteralMapValues(root, []string{"github_auth", "tokens"}) || changed
	if !changed {
		return false, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return false, err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-scrub-*")
	if err != nil {
		return false, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmpPath, path)
}

func deleteLiteralAt(root *yaml.Node, path []string) bool {
	parent := yamlMappingAt(root, path[:len(path)-1])
	if parent == nil {
		return false
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value != path[len(path)-1] {
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(parent.Content[i+1].Value), "env:") {
			return false
		}
		parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
		return true
	}
	return false
}

func deleteLiteralMapValues(root *yaml.Node, path []string) bool {
	n := yamlMappingAt(root, path)
	if n == nil {
		return false
	}
	changed := false
	for i := len(n.Content) - 2; i >= 0; i -= 2 {
		if !strings.HasPrefix(strings.TrimSpace(n.Content[i+1].Value), "env:") {
			n.Content = append(n.Content[:i], n.Content[i+2:]...)
			changed = true
		}
	}
	return changed
}

func yamlMappingAt(root *yaml.Node, path []string) *yaml.Node {
	cur := root
	for _, segment := range path {
		if cur == nil || cur.Kind != yaml.MappingNode {
			return nil
		}
		var next *yaml.Node
		for i := 0; i+1 < len(cur.Content); i += 2 {
			if cur.Content[i].Value == segment {
				next = cur.Content[i+1]
				break
			}
		}
		cur = next
	}
	if cur != nil && cur.Kind == yaml.MappingNode {
		return cur
	}
	return nil
}

// --- helpers (overridable in tests) ---

// readTokenFromStdin reads a single line from r, trimming whitespace.
func readTokenFromStdin(r io.Reader) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("no token on stdin")
	}
	return strings.TrimSpace(scanner.Text()), nil
}

// authSourceAndMaskedToken inspects the environment to determine which
// resolution tier supplied the token used by the current process. It
// re-reads env+config; the result is informational only.
var authSourceAndMaskedToken = func() (source, masked string) {
	if v := os.Getenv("GITHUB_TOKEN"); v != "" {
		return "env", MaskToken(v)
	}
	return "config", "****"
}

// writeTokenToConfig writes the token to .nightgauge/config.yaml under
// github_auth.token. The function is permissive — it creates the file if
// missing and preserves any existing fields.
var writeTokenToConfig = func(token string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(wd, ".nightgauge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "config.yaml")
	doc, err := loadYAMLDoc(path)
	if err != nil {
		return "", err
	}
	if err := setNestedString(doc, []string{"github_auth", "token"}, token); err != nil {
		return "", err
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// clearTokenFromConfig removes github_auth.token from the project
// config.yaml. Returns (path, cleared, err); cleared is false when no
// token was previously written.
var clearTokenFromConfig = func() (string, bool, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", false, err
	}
	path := filepath.Join(wd, ".nightgauge", "config.yaml")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, false, nil
	} else if err != nil {
		return path, false, err
	}
	doc, err := loadYAMLDoc(path)
	if err != nil {
		return path, false, err
	}
	cleared := deleteNestedKey(doc, []string{"github_auth", "token"})
	out, err := yaml.Marshal(doc)
	if err != nil {
		return path, cleared, err
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return path, cleared, err
	}
	return path, cleared, nil
}

// readGHToken shells out to `gh auth token`. Tests override this.
var readGHToken = func() (string, error) {
	cmd := newCommand("gh", "auth", "token")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// loadYAMLDoc reads a yaml.Node tree from path. A missing file is
// treated as an empty mapping document so callers can write into it.
func loadYAMLDoc(path string) (*yaml.Node, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		root := &yaml.Node{Kind: yaml.MappingNode}
		return root, nil
	}
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0], nil
	}
	if doc.Kind == 0 {
		return &yaml.Node{Kind: yaml.MappingNode}, nil
	}
	return &doc, nil
}

// setNestedString navigates path into root (creating mapping nodes as
// needed) and sets the leaf to the given string value.
func setNestedString(root *yaml.Node, path []string, value string) error {
	if root == nil || root.Kind != yaml.MappingNode {
		return fmt.Errorf("setNestedString: root is not a mapping")
	}
	cur := root
	for i, key := range path {
		idx := findChildKey(cur, key)
		if i == len(path)-1 {
			val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
			if idx >= 0 {
				cur.Content[idx+1] = val
			} else {
				cur.Content = append(cur.Content,
					&yaml.Node{Kind: yaml.ScalarNode, Value: key},
					val,
				)
			}
			return nil
		}
		if idx >= 0 && cur.Content[idx+1].Kind == yaml.MappingNode {
			cur = cur.Content[idx+1]
			continue
		}
		next := &yaml.Node{Kind: yaml.MappingNode}
		if idx >= 0 {
			cur.Content[idx+1] = next
		} else {
			cur.Content = append(cur.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Value: key},
				next,
			)
		}
		cur = next
	}
	return nil
}

// deleteNestedKey removes the leaf at path. Returns true when the key
// was present and removed.
func deleteNestedKey(root *yaml.Node, path []string) bool {
	if root == nil || root.Kind != yaml.MappingNode || len(path) == 0 {
		return false
	}
	cur := root
	for i, key := range path {
		idx := findChildKey(cur, key)
		if idx < 0 {
			return false
		}
		if i == len(path)-1 {
			cur.Content = append(cur.Content[:idx], cur.Content[idx+2:]...)
			return true
		}
		if cur.Content[idx+1].Kind != yaml.MappingNode {
			return false
		}
		cur = cur.Content[idx+1]
	}
	return false
}

// findChildKey returns the index of the key node within m.Content, or
// -1 when the key is absent. m.Content is laid out as alternating
// key/value pairs in yaml.MappingNode.
func findChildKey(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return i
		}
	}
	return -1
}
