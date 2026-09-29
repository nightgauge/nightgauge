package doctor

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
	"github.com/nightgauge/nightgauge/internal/layout"
)

// fakeApp is an httptest stand-in for GET /app and GET
// /app/installations/{id}. It records every Authorization header it saw, so
// a test can assert the JWT never reaches a finding.
type fakeApp struct {
	slug      string
	appPerms  map[string]string
	instPerms map[string]string
	suspended bool
	instGone  bool

	mu   sync.Mutex
	auth []string
}

func (f *fakeApp) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.auth = append(f.auth, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		f.mu.Unlock()
		switch r.URL.Path {
		case "/app":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "slug": f.slug,
				"owner": map[string]string{"login": "acme", "type": "Organization"}, "permissions": f.appPerms})
		case "/app/installations/42":
			if f.instGone {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"Not Found"}`))
				return
			}
			body := map[string]any{"id": 42, "account": map[string]string{"login": "acme", "type": "Organization"},
				"target_type": "Organization", "permissions": f.instPerms}
			if f.suspended {
				body["suspended_at"] = "2026-09-01T00:00:00Z"
				body["suspended_by"] = map[string]string{"login": "octo-admin", "type": "User"}
			}
			_ = json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	old := newAppInspector
	newAppInspector = func(c *gh.AppCredentials) appInspector { return gh.NewAppInspectorWithBase(c, srv.URL) }
	t.Cleanup(func() { newAppInspector = old })
	return srv
}

func testAppCreds(t *testing.T) *gh.AppCredentials {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return &gh.AppCredentials{AppID: "7", InstallationID: 42, PrivateKeyPEM: pemBytes}
}

// fullPerms is every permission the pipeline needs, at its required level.
func fullPerms() map[string]string {
	out := map[string]string{"metadata": "read"}
	for _, r := range requiredAppPermissions {
		out[r.Name] = r.Level
	}
	return out
}

func without(m map[string]string, key string) map[string]string {
	out := map[string]string{}
	for k, v := range m {
		if k != key {
			out[k] = v
		}
	}
	return out
}

func probeFake(t *testing.T, f *fakeApp) (*appProbe, []Finding) {
	t.Helper()
	t.Setenv(layout.EnvStateHome, t.TempDir())
	f.serve(t)
	p := probeApp(context.Background(), testAppCreds(t), nil, nil)
	fs := appPermissionFindings(p)
	raw, _ := json.Marshal(BuildResult([]CheckResult{{ID: "github_identity", Findings: fs}}))
	for _, jwt := range f.auth {
		if jwt == "" || strings.Contains(string(raw), jwt) {
			t.Fatalf("request without a JWT, or the JWT reached a finding")
		}
	}
	return p, fs
}

func remedyAllText(r Remedy) string {
	return r.Summary + " " + strings.Join(r.Steps, " ") + " " + strings.Join(r.Links, " ")
}

// #2094: the App's declared permissions and the installation's granted ones
// are compared with what the pipeline needs, and each gap is its own code.
func TestIdentityDiagnosis(t *testing.T) {
	appLink := "https://github.com/organizations/acme/settings/apps/ng-pipeline/permissions"
	instLink := "https://github.com/organizations/acme/settings/installations/42"

	t.Run("App lacks organization_projects", func(t *testing.T) {
		perms := without(fullPerms(), "organization_projects")
		_, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: perms, instPerms: perms})
		f := onlyFinding(t, fs, codeAppLacksPermission, SeverityBlocker)
		if f.Evidence["permission"] != "organization_projects" || f.Evidence["declared"] != "none" {
			t.Errorf("evidence = %v", f.Evidence)
		}
		r := f.Remedies[0]
		if r.Kind != RemedyManual || r.Verb != "" || len(r.Links) != 2 || r.Links[0] != appLink || r.Links[1] != instLink {
			t.Errorf("want a manual remedy linking the App's permissions then the installation, got %+v", r)
		}
		if last := r.Steps[len(r.Steps)-1]; !strings.Contains(last, "nightgauge doctor --only NGD036") {
			t.Errorf("manual remedy must end with the check-again step, got %q", last)
		}
	})

	t.Run("declared but not accepted is pending", func(t *testing.T) {
		_, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(),
			instPerms: without(fullPerms(), "organization_projects")})
		f := onlyFinding(t, fs, codeAppPermissionPending, SeverityBlocker)
		r := f.Remedies[0]
		if len(r.Links) != 1 || r.Links[0] != instLink || !strings.Contains(remedyAllText(r), "--only NGD037") {
			t.Errorf("want the installation's review page and a check-again step, got %+v", r)
		}
		if !strings.Contains(f.Title, "Organization → Projects") || !strings.Contains(f.Title, "pending acceptance") {
			t.Errorf("title = %q", f.Title)
		}
	})

	t.Run("both granted is healthy", func(t *testing.T) {
		if _, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(), instPerms: fullPerms()}); len(fs) != 0 {
			t.Errorf("want no finding, got %s", findingsText(fs))
		}
	})

	t.Run("a lower level than required counts as missing", func(t *testing.T) {
		perms := fullPerms()
		perms["issues"] = "read"
		_, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: perms, instPerms: perms})
		if f := onlyFinding(t, fs, codeAppLacksPermission, SeverityBlocker); f.Evidence["declared"] != "read" || f.Evidence["required"] != "write" {
			t.Errorf("evidence = %v", f.Evidence)
		}
	})

	t.Run("suspended installation", func(t *testing.T) {
		_, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(), instPerms: fullPerms(), suspended: true})
		f := onlyFinding(t, fs, codeAppSuspended, SeverityBlocker)
		if f.Evidence["suspended_by"] != "octo-admin" || f.Remedies[0].Links[0] != instLink {
			t.Errorf("suspended finding = %+v", f)
		}
	})

	t.Run("installation not found is unverifiable, with the install link", func(t *testing.T) {
		_, fs := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(), instGone: true})
		f := onlyFinding(t, fs, codeAppUnverifiable, SeverityWarning)
		if f.Evidence["http_status"] != "404" || f.Remedies[0].Links[0] != "https://github.com/apps/ng-pipeline/installations/new" {
			t.Errorf("unverifiable finding = %+v", f)
		}
	})

	t.Run("links are built only from API values on github.com", func(t *testing.T) {
		perms := without(fullPerms(), "organization_projects")
		_, fs := probeFake(t, &fakeApp{slug: "evil.example/x?", appPerms: perms, instPerms: perms})
		for _, f := range fs {
			for _, r := range f.Remedies {
				for _, l := range r.Links {
					if !strings.HasPrefix(l, "https://github.com/") || strings.Contains(l, "evil") {
						t.Errorf("link %q escapes github.com or embeds an unvalidated slug", l)
					}
				}
			}
		}
	})

	t.Run("a cached token minted with other permissions is discarded", func(t *testing.T) {
		state := t.TempDir()
		f := &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(), instPerms: fullPerms()}
		f.serve(t)
		t.Setenv(layout.EnvStateHome, state)
		cache := filepath.Join(state, "github-app-token-42.json")
		stale, _ := json.Marshal(map[string]any{"token": "ghs_old", "expires_at": time.Now().Add(time.Hour),
			"permissions_hash": gh.PermissionsHash(without(fullPerms(), "organization_projects"))})
		if err := os.WriteFile(cache, stale, 0o600); err != nil {
			t.Fatal(err)
		}
		p := probeApp(context.Background(), testAppCreds(t), nil, nil)
		if !p.tokenRefreshed {
			t.Error("the probe must report the stale token")
		}
		if _, err := os.Stat(cache); !os.IsNotExist(err) {
			t.Errorf("the stale cache must be removed, stat err = %v", err)
		}
	})
}

// fakeProjectLister answers ListOwnerProjects from an httptest GraphQL server.
func fakeProjectLister(t *testing.T, body string) func(context.Context) (gh.OwnerProjects, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := gh.NewClientWithURL("ghp_test", srv.URL)
	return func(ctx context.Context) (gh.OwnerProjects, error) {
		return gh.ListOwnerProjects(ctx, c, "acme", gh.OwnerTypeOrg)
	}
}

const twoProjects = `{"data":{"organization":{"login":"acme","projectsV2":{"nodes":[` +
	`{"number":1,"title":"Roadmap","closed":false},{"number":2,"title":"Nightgauge","closed":false}]}}}}`

// #2094: "Could not resolve to a ProjectV2" is re-diagnosed before it is
// reported; GitHub's message stays in the evidence, never alone in the title.
func TestBoardFailureDiagnosis(t *testing.T) {
	resolveErr := func(n string) error {
		return errors.New("list open items on project " + n + ": Could not resolve to a ProjectV2 with the number " + n + ".")
	}
	patWithProject := &gh.TokenScopeInfo{Login: "octo", Scopes: []string{"repo", "project"}, ScopesAdvertised: true, Valid: true}
	diagnose := func(t *testing.T, d boardDiagnosis) []Finding {
		t.Helper()
		fs, detail, ok := diagnoseBoardFailure(context.Background(), d)
		if !ok || detail == "" {
			t.Fatalf("a ProjectV2 resolution failure must be diagnosed")
		}
		for _, f := range fs {
			if strings.HasPrefix(f.Title, "list open items") || f.Title == d.readErr.Error() {
				t.Errorf("title is GitHub's raw message: %q", f.Title)
			}
			if f.Evidence["error"] != d.readErr.Error() {
				t.Errorf("the raw message must stay in evidence, got %v", f.Evidence)
			}
		}
		return fs
	}

	t.Run("wrong number with the permission present lists the projects", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"), scopes: patWithProject,
			list: fakeProjectLister(t, twoProjects)})
		f := onlyFinding(t, fs, codeBoardWrongNumber, SeverityBlocker)
		if f.Evidence["available"] != "1 (Roadmap), 2 (Nightgauge)" || !strings.Contains(f.Title, "1 (Roadmap), 2 (Nightgauge)") {
			t.Errorf("want the owner's projects listed, got title %q evidence %v", f.Title, f.Evidence)
		}
		r := f.Remedies[0]
		if r.Links[0] != "https://github.com/orgs/acme/projects" || !strings.Contains(remedyAllText(r), "--only NGD040") {
			t.Errorf("remedy = %+v", r)
		}
	})

	t.Run("a number below the owner's highest is a deleted project", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 2}
		body := strings.Replace(twoProjects, `"number":2`, `"number":5`, 1)
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("2"), scopes: patWithProject,
			list: fakeProjectLister(t, body)})
		onlyFinding(t, fs, codeBoardDeleted, SeverityBlocker)
	})

	t.Run("a classic token without the project scope gets a confirm refresh", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"),
			scopes: &gh.TokenScopeInfo{Login: "octo", Scopes: []string{"repo"}, ScopesAdvertised: true},
			list:   fakeProjectLister(t, twoProjects)})
		f := onlyFinding(t, fs, codeBoardNoAccess, SeverityBlocker)
		r := f.Remedies[0]
		if r.Kind != RemedyConfirm || r.Verb != verbGHAuthRefresh || f.Evidence["missing"] != "project" ||
			!strings.Contains(r.Preview, "gh auth refresh -h github.com -s project") {
			t.Errorf("want a confirm gh auth refresh remedy, got %+v (evidence %v)", r, f.Evidence)
		}
	})

	t.Run("an App without Organization → Projects is named", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		perms := without(fullPerms(), "organization_projects")
		p, _ := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: perms, instPerms: perms})
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"), app: p, list: fakeProjectLister(t, twoProjects)})
		if f := onlyFinding(t, fs, codeBoardNoAccess, SeverityBlocker); f.Evidence["reason"] != "app_lacks_projects" {
			t.Errorf("reason = %q", f.Evidence["reason"])
		}
	})

	t.Run("an App whose installation has not accepted Projects is named", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		p, _ := probeFake(t, &fakeApp{slug: "ng-pipeline", appPerms: fullPerms(), instPerms: without(fullPerms(), "organization_projects")})
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"), app: p, list: fakeProjectLister(t, twoProjects)})
		f := onlyFinding(t, fs, codeBoardNoAccess, SeverityBlocker)
		if f.Evidence["reason"] != "projects_pending_acceptance" ||
			f.Remedies[0].Links[0] != "https://github.com/organizations/acme/settings/installations/42" {
			t.Errorf("finding = %+v", f)
		}
	})

	t.Run("no visible project and unknown permission is an access problem", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"),
			scopes: &gh.TokenScopeInfo{Login: "octo", ScopesAdvertised: false},
			list:   fakeProjectLister(t, `{"data":{"organization":{"login":"acme","projectsV2":{"nodes":[]}}}}`)})
		if f := onlyFinding(t, fs, codeBoardNoAccess, SeverityBlocker); f.Evidence["reason"] != "no_projects_visible" {
			t.Errorf("reason = %q", f.Evidence["reason"])
		}
	})

	t.Run("a failed listing still never reports the raw message alone", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		fs := diagnose(t, boardDiagnosis{cfg: cfg, readErr: resolveErr("3"), scopes: patWithProject,
			list: fakeProjectLister(t, `{"data":{"organization":null},"errors":[{"message":"boom"}]}`)})
		if f := onlyFinding(t, fs, "NGD013", SeverityBlocker); !strings.Contains(f.Evidence["list_error"], "boom") {
			t.Errorf("evidence = %v", f.Evidence)
		}
	})

	t.Run("other failures are left to the generic finding", func(t *testing.T) {
		cfg := &config.Config{Owner: "acme", DefaultRepo: "r", ProjectNumber: 3}
		if _, _, ok := diagnoseBoardFailure(context.Background(), boardDiagnosis{cfg: cfg, readErr: errors.New("dial tcp: timeout")}); ok {
			t.Error("a network error is not a resolution failure")
		}
	})
}

// Every code a check in this package emits resolves to its check, so
// `nightgauge doctor --only <code>` (the check-again step) always runs it.
func TestEveryCodeResolvesToACheck(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	codeRe := regexp.MustCompile(`"(NGD\d{3})"`)
	seen := map[string]bool{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range codeRe.FindAllStringSubmatch(string(src), -1) {
			seen[m[1]] = true
		}
	}
	delete(seen, codeTimeout) // NGD000 belongs to every check
	if len(seen) < 40 {
		t.Fatalf("scan found only %d codes; the source scan is broken", len(seen))
	}
	reg := DefaultRegistry()
	for code := range seen {
		if _, err := reg.Select([]string{code}); err != nil {
			t.Errorf("%s: %v", code, err)
		}
	}
}

func TestRegistrySelect(t *testing.T) {
	reg := DefaultRegistry()
	sub, err := reg.Select([]string{"NGD040"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(sub.IDs(), ","); got != "github_auth,project,board_population" {
		t.Errorf("--only NGD040 runs %s; want board_population and its dependencies, in registry order", got)
	}
	if _, err := reg.Select([]string{"NGD999", "nope"}); err == nil || !strings.Contains(err.Error(), "NGD999, nope") {
		t.Errorf("an unknown code must be an error naming it, got %v", err)
	}
	res, err := RunDoctorOnly(context.Background(), nil, nil, nil, nil, []string{"ngd010"})
	if err != nil || len(res.Results) != 1 || res.Results[0].ID != "config" {
		t.Errorf("--only ngd010 = %+v, %v; want only the config check", res.Results, err)
	}
}

// The scope findings offer `gh auth refresh` as a confirm remedy, run through
// the verb registry.
func TestScopeFindingsOfferGHRefresh(t *testing.T) {
	fs, _ := scopeFindings(&gh.TokenScopeInfo{Login: "octo", Valid: false, MissingScopes: []string{"project"}, ScopesAdvertised: true})
	f := onlyFinding(t, fs, "NGD006", SeverityBlocker)
	if r := f.Remedies[0]; r.Kind != RemedyConfirm || r.Verb != verbGHAuthRefresh || f.Evidence["login"] != "octo" {
		t.Errorf("NGD006 remedy = %+v", r)
	}
	fs, _ = scopeFindings(&gh.TokenScopeInfo{Login: "octo", Valid: true, Scopes: []string{"repo", "project"}, ScopesAdvertised: true})
	f = onlyFinding(t, fs, codeReadOrg, SeverityWarning)
	if r := f.Remedies[0]; r.Kind != RemedyConfirm || f.Evidence["missing"] != "read:org" {
		t.Errorf("NGD034 remedy = %+v", r)
	}
	if _, ok := BuiltinVerbs(&Env{}).Lookup(verbGHAuthRefresh); !ok {
		t.Error("github.auth_refresh is not registered")
	}
}

func TestAuthRefreshVerb(t *testing.T) {
	var calls []string
	active := "octo"
	tokens := []string{"gho_old", "gho_new"}
	oldCLI, oldTTY := ghCLI, stdinIsTerminal
	t.Cleanup(func() { ghCLI, stdinIsTerminal = oldCLI, oldTTY })
	ghCLI = func(_ context.Context, interactive bool, args ...string) (string, error) {
		line := strings.Join(args, " ")
		calls = append(calls, line)
		switch {
		case line == "api user --jq .login":
			return active, nil
		case line == "auth token -h github.com":
			tok := tokens[0]
			if len(tokens) > 1 {
				tokens = tokens[1:]
			}
			return tok, nil
		case strings.HasPrefix(line, "auth refresh"):
			if !interactive {
				t.Error("gh auth refresh must run attached to the terminal")
			}
			return "", nil
		}
		t.Fatalf("unexpected gh call %q", line)
		return "", nil
	}
	tty := true
	stdinIsTerminal = func() bool { return tty }
	t.Setenv("GH_TOKEN", "gho_old")

	env := &Env{}
	v := &verbs{env: env}
	f := Finding{Code: "NGD006", Evidence: map[string]string{"missing": "project, read:org", "login": "octo"}}
	ctx := context.Background()

	if err := v.authRefreshPrecondition(ctx, f); err != nil {
		t.Fatalf("precondition: %v", err)
	}
	active = "someone-else"
	if err := v.authRefreshPrecondition(ctx, f); err == nil {
		t.Error("refreshing another gh account must be refused")
	}
	active, tty = "octo", false
	if err := v.authRefreshPrecondition(ctx, f); err == nil {
		t.Error("without a terminal the refresh must be refused")
	}
	tty = true
	bad := Finding{Code: "NGD006", Evidence: map[string]string{"missing": "admin:enterprise", "login": "octo"}}
	if err := v.authRefreshPrecondition(ctx, bad); err == nil {
		t.Error("a scope outside the allowlist must be refused")
	}

	calls = nil
	if err := v.authRefreshApply(ctx, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(calls, "|"), "auth refresh -h github.com -s project,read:org") {
		t.Errorf("gh calls = %v", calls)
	}
	if env.Client == nil || os.Getenv("GH_TOKEN") != "gho_new" {
		t.Errorf("verification must read the refreshed token: client=%v GH_TOKEN=%q", env.Client, os.Getenv("GH_TOKEN"))
	}
}
