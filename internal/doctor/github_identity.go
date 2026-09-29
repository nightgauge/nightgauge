package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/nightgauge/nightgauge/internal/config"
	gh "github.com/nightgauge/nightgauge/internal/github"
)

// GitHub identity and board diagnosis (#2094). A failed board read used to
// surface as GitHub's raw "Could not resolve to a ProjectV2 with the number
// 3", although the cause was that the App lacked Organization → Projects, and
// then that the org had not accepted the added permission. Doctor can read
// both facts with the App JWT, so it names the cause and hands the operator
// the exact page to fix it on.
//
// Security: every link is built on https://github.com/ from ids and slugs the
// API returned, validated against GitHub's login and slug alphabets; nothing
// read from config becomes part of a URL. The JWT and the installation token
// never reach a finding: the inspector keeps them in request headers.

// Codes this file adds (ADR-025 § 2).
const (
	codeAppLacksPermission   = "NGD036" // github_identity: the App does not declare a permission
	codeAppPermissionPending = "NGD037" // github_identity: declared, not yet accepted by the installation
	codeAppSuspended         = "NGD038" // github_identity: the installation is suspended
	codeAppUnverifiable      = "NGD039" // github_identity: App or installation could not be read
	codeBoardWrongNumber     = "NGD040" // board_population: no such project; lists the owner's projects
	codeBoardNoAccess        = "NGD041" // board_population: the identity cannot see projects
	codeBoardDeleted         = "NGD042" // board_population: the project number existed and is gone
)

// verbGHAuthRefresh adds OAuth scopes to gh's stored credential.
const verbGHAuthRefresh = "github.auth_refresh"

// checkAgainStep is the "check again" affordance every manual remedy here
// carries: it re-runs only the check that owns code.
func checkAgainStep(code string) string {
	return "Check again: `nightgauge doctor --only " + code + "`"
}

// permissionRequirement is one GitHub App permission the pipeline needs
// (docs/CONFIGURATION.md § GitHub App identity).
type permissionRequirement struct {
	Name     string // API name, e.g. "organization_projects"
	Level    string // minimum level: read or write
	Label    string // as GitHub's settings page names it
	Severity Severity
	OrgOnly  bool   // only an organization installation can grant it
	Effect   string // what fails without it
}

var requiredAppPermissions = []permissionRequirement{
	{Name: "contents", Level: "write", Label: "Repository → Contents", Severity: SeverityBlocker,
		Effect: "the pipeline cannot push branches"},
	{Name: "issues", Level: "write", Label: "Repository → Issues", Severity: SeverityBlocker,
		Effect: "the pipeline cannot read or update issues"},
	{Name: "pull_requests", Level: "write", Label: "Repository → Pull requests", Severity: SeverityBlocker,
		Effect: "the pipeline cannot open or merge pull requests"},
	{Name: "organization_projects", Level: "write", Label: "Organization → Projects", Severity: SeverityBlocker, OrgOnly: true,
		Effect: `the board cannot be read or moved ("Could not resolve to a ProjectV2")`},
	{Name: "checks", Level: "write", Label: "Repository → Checks", Severity: SeverityWarning,
		Effect: "the pipeline cannot read or report check runs"},
	{Name: "actions", Level: "read", Label: "Repository → Actions", Severity: SeverityWarning,
		Effect: "the pipeline cannot read workflow runs"},
}

// appRequirement returns the named requirement; the name is one of
// requiredAppPermissions', so a miss is a programming error.
func appRequirement(name string) permissionRequirement {
	for _, r := range requiredAppPermissions {
		if r.Name == name {
			return r
		}
	}
	panic("doctor: unknown App permission requirement " + name)
}

// levelRank orders permission levels; an absent permission ranks 0.
func levelRank(level string) int {
	switch strings.ToLower(level) {
	case "read":
		return 1
	case "write":
		return 2
	case "admin":
		return 3
	}
	return 0
}

func levelLabel(level string) string {
	switch strings.ToLower(level) {
	case "write":
		return "Read and write"
	case "read":
		return "Read-only"
	case "admin":
		return "Admin"
	}
	return "No access"
}

func orNone(level string) string {
	if level == "" {
		return "none"
	}
	return level
}

// appInspector reads the App and its installation (gh.AppInspector).
type appInspector interface {
	App(ctx context.Context) (*gh.AppInfo, error)
	Installation(ctx context.Context) (*gh.AppInstallation, error)
}

// newAppInspector builds the inspector; tests point it at an httptest server.
var newAppInspector = func(creds *gh.AppCredentials) appInspector { return gh.NewAppInspector(creds) }

// appProbe is what doctor learned about the configured App, read once per run
// and shared by github_identity and board_population.
type appProbe struct {
	creds    *gh.AppCredentials
	credsErr error
	app      *gh.AppInfo
	appErr   error
	inst     *gh.AppInstallation
	instErr  error
	// tokenRefreshed: the cached installation token was minted with other
	// permissions than the installation has now, and was discarded.
	tokenRefreshed bool
}

// githubApp returns the App probe, or nil when no App is configured for the
// owner. The client's App wins; otherwise the config's, so an App that could
// not mint (a suspended installation) is still diagnosed.
func (e *Env) githubApp(ctx context.Context) *appProbe {
	e.appOnce.Do(func() {
		var creds *gh.AppCredentials
		var credsErr error
		if e.Client != nil && e.Client.App() != nil {
			creds = e.Client.App()
		} else if e.Cfg != nil && e.Cfg.Owner != "" {
			creds, credsErr = gh.ResolveApp(e.Cfg, e.Cfg.Owner)
		}
		if creds == nil && credsErr == nil {
			return
		}
		e.app = probeApp(ctx, creds, credsErr, e.Client)
	})
	return e.app
}

// probeApp reads the App and its installation. When the installation's
// permissions differ from those the cached token was minted with, the token
// is discarded, on disk and in client, so the next request carries the
// installation's current permissions.
func probeApp(ctx context.Context, creds *gh.AppCredentials, credsErr error, client *gh.Client) *appProbe {
	p := &appProbe{creds: creds, credsErr: credsErr}
	if creds == nil {
		return p
	}
	in := newAppInspector(creds)
	p.app, p.appErr = in.App(ctx)
	p.inst, p.instErr = in.Installation(ctx)
	if p.instErr == nil && !p.inst.Suspended() {
		if inv, err := gh.SyncAppTokenPermissions(creds, p.inst.Permissions); err == nil && inv {
			p.tokenRefreshed = true
			client.ResetAppToken()
		}
	}
	return p
}

// --- links ------------------------------------------------------------------

var (
	githubLoginRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	githubSlugRe  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,99})$`)
)

const githubWeb = "https://github.com/"

func isOrgAccount(a gh.AppAccount) bool { return strings.EqualFold(a.Type, "Organization") }

// appPermissionsURL is the App's permission settings page, or "".
func appPermissionsURL(app *gh.AppInfo) string {
	if app == nil || !githubSlugRe.MatchString(app.Slug) {
		return ""
	}
	if isOrgAccount(app.Owner) {
		if !githubLoginRe.MatchString(app.Owner.Login) {
			return ""
		}
		return githubWeb + "organizations/" + app.Owner.Login + "/settings/apps/" + app.Slug + "/permissions"
	}
	return githubWeb + "settings/apps/" + app.Slug + "/permissions"
}

// installationURL is the installation's settings page, where an owner of the
// account reviews and accepts requested permissions, or "".
func installationURL(inst *gh.AppInstallation) string {
	if inst == nil || inst.ID <= 0 {
		return ""
	}
	id := strconv.FormatInt(inst.ID, 10)
	if isOrgAccount(inst.Account) {
		if !githubLoginRe.MatchString(inst.Account.Login) {
			return ""
		}
		return githubWeb + "organizations/" + inst.Account.Login + "/settings/installations/" + id
	}
	return githubWeb + "settings/installations/" + id
}

// appInstallURL is the page that installs the App on an account, or "".
func appInstallURL(app *gh.AppInfo) string {
	if app == nil || !githubSlugRe.MatchString(app.Slug) {
		return ""
	}
	return githubWeb + "apps/" + app.Slug + "/installations/new"
}

// ownerProjectsURL is the owner's project list, from the login GitHub
// returned, or "".
func ownerProjectsURL(login string, ownerType gh.OwnerType) string {
	if !githubLoginRe.MatchString(login) {
		return ""
	}
	if ownerType == gh.OwnerTypeUser {
		return githubWeb + "users/" + login + "/projects"
	}
	return githubWeb + "orgs/" + login + "/projects"
}

// withLinks sets r.Links to the non-empty urls.
func withLinks(r Remedy, urls ...string) Remedy {
	for _, u := range urls {
		if u != "" {
			r.Links = append(r.Links, u)
		}
	}
	return r
}

// openStep names a page to open, or falls back to where to find it.
func openStep(url, fallback string) string {
	if url == "" {
		return fallback
	}
	return "Open " + url
}

// --- github_identity: App permissions ---------------------------------------

func appName(p *appProbe) string {
	if p.app != nil && p.app.Slug != "" {
		return p.app.Slug
	}
	return p.creds.String()
}

func installationAccount(p *appProbe) string {
	if p.inst != nil && p.inst.Account.Login != "" {
		return p.inst.Account.Login
	}
	return "the owner"
}

// appPermissionFindings compares the permissions the pipeline needs with the
// App's declared permissions and the installation's granted ones.
func appPermissionFindings(p *appProbe) []Finding {
	const check = "github_identity"
	if p == nil {
		return nil
	}
	if p.credsErr != nil || p.appErr != nil || p.instErr != nil {
		return []Finding{appUnverifiableFinding(p)}
	}
	instURL := installationURL(p.inst)
	instID := strconv.FormatInt(p.inst.ID, 10)
	if p.inst.Suspended() {
		ev := map[string]string{"app": appName(p), "installation_id": instID, "account": p.inst.Account.Login,
			"suspended_at": p.inst.SuspendedAt.UTC().Format("2006-01-02T15:04:05Z")}
		if p.inst.SuspendedBy != nil && p.inst.SuspendedBy.Login != "" {
			ev["suspended_by"] = p.inst.SuspendedBy.Login
		}
		return []Finding{newFinding(check, codeAppSuspended, SeverityBlocker,
			"GitHub App installation on "+installationAccount(p)+" is suspended",
			"a suspended installation cannot mint tokens, so pipeline traffic falls back to a personal token or fails",
			ev, []string{appName(p), instID},
			withLinks(manualRemedy("unsuspend", "Unsuspend the App's installation on "+installationAccount(p), check,
				openStep(instURL, "Open the installation under the account's Settings → GitHub Apps"),
				"An owner of "+installationAccount(p)+" chooses Unsuspend",
				checkAgainStep(codeAppSuspended)), instURL))}
	}
	var out []Finding
	for _, req := range requiredAppPermissions {
		if req.OrgOnly && !isOrgAccount(p.inst.Account) {
			continue
		}
		declared, granted := p.app.Permissions[req.Name], p.inst.Permissions[req.Name]
		ev := map[string]string{"app": appName(p), "installation_id": instID, "account": p.inst.Account.Login,
			"permission": req.Name, "required": req.Level, "declared": orNone(declared), "granted": orNone(granted)}
		identity := []string{appName(p), instID, req.Name}
		switch {
		case levelRank(declared) < levelRank(req.Level):
			appURL := appPermissionsURL(p.app)
			out = append(out, newFinding(check, codeAppLacksPermission, req.Severity,
				fmt.Sprintf("GitHub App %s lacks permission %s (%s)", appName(p), req.Label, req.Level),
				fmt.Sprintf("the App declares %s as %s, so no installation can grant it and %s",
					req.Label, orNone(declared), req.Effect),
				ev, identity,
				withLinks(manualRemedy("app-permission", "Add "+req.Label+" to the App's permissions", check,
					openStep(appURL, "Open the App's settings: Developer settings → GitHub Apps → "+appName(p)+" → Permissions & events"),
					"Set "+req.Label+" to "+levelLabel(req.Level)+" and save; GitHub asks each installation to accept the change",
					"An owner of "+installationAccount(p)+" accepts it: "+openStep(instURL, "the installation's settings page"),
					checkAgainStep(codeAppLacksPermission)), appURL, instURL)))
		case levelRank(granted) < levelRank(req.Level):
			out = append(out, newFinding(check, codeAppPermissionPending, req.Severity,
				fmt.Sprintf("permission %s is pending acceptance on %s's installation", req.Label, installationAccount(p)),
				fmt.Sprintf("the App declares %s as %s, but the installation has granted %s: until an owner accepts the change, %s",
					req.Label, declared, orNone(granted), req.Effect),
				ev, identity,
				withLinks(manualRemedy("accept", "Accept the App's requested permissions on "+installationAccount(p), check,
					openStep(instURL, "Open the installation under "+installationAccount(p)+"'s Settings → GitHub Apps"),
					"An owner of "+installationAccount(p)+" reviews the request and accepts the new permissions",
					checkAgainStep(codeAppPermissionPending)), instURL)))
		}
	}
	return out
}

// appUnverifiableFinding reports an App whose permissions could not be read.
// It names the failure class and GitHub's message, never a credential.
func appUnverifiableFinding(p *appProbe) Finding {
	const check = "github_identity"
	ev := map[string]string{"state": "unverifiable"}
	reason, err := "", p.credsErr
	if err == nil {
		err = p.appErr
	}
	if err == nil {
		err = p.instErr
	}
	var steps []string
	var links []string
	var apiErr *gh.AppAPIError
	switch {
	case p.credsErr != nil:
		reason = "github_auth.app is configured but unusable: " + p.credsErr.Error()
		steps = append(steps, "Fix github_auth.app in the machine config: its id and private_key_path must name the App's key")
	case errors.As(err, &apiErr) && apiErr.Status == 404 && p.appErr == nil:
		reason = fmt.Sprintf("installation %d was not found: the App is not installed on the owner, or github_auth.app.installations names the wrong id", p.creds.InstallationID)
		steps = append(steps, openStep(appInstallURL(p.app), "Install the App on the owner"),
			"Set github_auth.app.installations.<owner> to the installation id in the installation page's URL")
		links = append(links, appInstallURL(p.app))
	case errors.As(err, &apiErr) && apiErr.Status == 401:
		reason = "GitHub refused the App's JWT: the App id or the private key does not match the App"
		steps = append(steps, "Check github_auth.app.id against the App's settings, and generate a new private key there if needed")
	default:
		reason = "the App's permissions could not be read: " + err.Error()
		steps = append(steps, "Retry when api.github.com is reachable")
	}
	if apiErr != nil {
		ev["endpoint"], ev["http_status"], ev["message"] = apiErr.Endpoint, strconv.Itoa(apiErr.Status), apiErr.Message
	} else if err != nil {
		ev["error"] = err.Error()
	}
	if p.creds != nil {
		ev["app"] = p.creds.String()
	}
	steps = append(steps, checkAgainStep(codeAppUnverifiable))
	r := withLinks(manualRemedy("investigate", "Make the App's permissions readable", check, steps...), links...)
	return newFinding(check, codeAppUnverifiable, SeverityWarning,
		"GitHub App permissions unverifiable", reason, ev, []string{ev["app"], ev["http_status"]}, r)
}

// --- board_population: re-diagnosing a ProjectV2 resolution failure ---------

// isProjectResolutionFailure reports whether a board read failed because the
// project could not be resolved or reached, as opposed to a network error.
func isProjectResolutionFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "could not resolve to a projectv2") ||
		strings.Contains(msg, "resource not accessible by integration")
}

// boardDiagnosis is what the board re-diagnosis reads.
type boardDiagnosis struct {
	cfg     *config.Config
	readErr error
	app     *appProbe          // non-nil only when the client authenticates as the App
	scopes  *gh.TokenScopeInfo // personal token scopes, when probed
	list    func(ctx context.Context) (gh.OwnerProjects, error)
}

func boardOwnerType(cfg *config.Config) gh.OwnerType {
	if strings.EqualFold(cfg.OwnerType, "user") {
		return gh.OwnerTypeUser
	}
	return gh.OwnerTypeOrg
}

// scopeCovers reports whether the advertised scopes include one of want.
func scopeCovers(scopes []string, want ...string) bool {
	for _, s := range scopes {
		for _, w := range want {
			if strings.EqualFold(strings.TrimSpace(s), w) {
				return true
			}
		}
	}
	return false
}

// diagnoseBoardFailure turns "Could not resolve to a ProjectV2" into its
// cause: the identity cannot see projects (NGD041), the number names no
// project (NGD040, listing the owner's projects), or the project was deleted
// (NGD042). GitHub's own message stays in the evidence. ok is false when the
// failure is not a project-resolution failure.
func diagnoseBoardFailure(ctx context.Context, d boardDiagnosis) ([]Finding, string, bool) {
	const check = "board_population"
	if !isProjectResolutionFailure(d.readErr) {
		return nil, "", false
	}
	cfg := d.cfg
	board := fmt.Sprintf("%s/%d", cfg.Owner, cfg.ProjectNumber)
	number := strconv.Itoa(cfg.ProjectNumber)
	ev := func(extra map[string]string) map[string]string {
		m := map[string]string{"board": board, "project_number": number, "owner": cfg.Owner, "error": d.readErr.Error()}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	noAccess := func(reason, cause string, extra map[string]string, remedies ...Remedy) ([]Finding, string, bool) {
		e := ev(extra)
		e["reason"] = reason
		title := fmt.Sprintf("the board's identity cannot see %s's projects: %s", cfg.Owner, cause)
		return []Finding{newFinding(check, codeBoardNoAccess, SeverityBlocker, title,
			"GitHub answers a project the identity may not see as if it did not exist; the cause is the identity's access, not the project number",
			e, []string{board, reason}, remedies...)}, title, true
	}

	// 1. Can this identity see projects at all?
	permissionKnown := false
	if p := d.app; p != nil && p.creds != nil && boardOwnerType(cfg) == gh.OwnerTypeOrg {
		if p.credsErr == nil && p.appErr == nil && p.instErr == nil {
			instURL := installationURL(p.inst)
			req := appRequirement("organization_projects")
			declared, granted := p.app.Permissions[req.Name], p.inst.Permissions[req.Name]
			base := map[string]string{"app": appName(p), "permission": req.Name, "declared": orNone(declared), "granted": orNone(granted)}
			switch {
			case p.inst.Suspended():
				return noAccess("installation_suspended", "the App's installation is suspended", base,
					withLinks(manualRemedy("unsuspend", "Unsuspend the App's installation", check,
						openStep(instURL, "Open the installation's settings"),
						"An owner of "+installationAccount(p)+" chooses Unsuspend", checkAgainStep(codeBoardNoAccess)), instURL))
			case levelRank(declared) < 1:
				appURL := appPermissionsURL(p.app)
				return noAccess("app_lacks_projects", "GitHub App "+appName(p)+" lacks Organization → Projects", base,
					withLinks(manualRemedy("app-permission", "Add Organization → Projects to the App's permissions", check,
						openStep(appURL, "Open the App's Permissions & events settings"),
						"Set Organization → Projects to Read and write and save",
						"An owner of "+installationAccount(p)+" accepts it: "+openStep(instURL, "the installation's settings page"),
						checkAgainStep(codeBoardNoAccess)), appURL, instURL))
			case levelRank(granted) < 1:
				return noAccess("projects_pending_acceptance", "Organization → Projects is pending acceptance on "+installationAccount(p)+"'s installation", base,
					withLinks(manualRemedy("accept", "Accept the App's requested permissions", check,
						openStep(instURL, "Open the installation's settings"),
						"An owner of "+installationAccount(p)+" accepts the new permissions",
						checkAgainStep(codeBoardNoAccess)), instURL))
			}
			permissionKnown = true
		}
	} else if s := d.scopes; d.app == nil && s != nil && s.ScopesAdvertised {
		if !scopeCovers(s.Scopes, "project", "read:project") {
			return noAccess("token_lacks_project_scope", "the token lacks the project scope",
				map[string]string{"login": s.Login, "missing": "project", "granted": strings.Join(s.Scopes, ", ")},
				ghRefreshRemedy(check, []string{"project"}),
				manualRemedy("token", "Issue a token with the project scope", check,
					"Or export GITHUB_TOKEN with a classic token that has the project scope",
					checkAgainStep(codeBoardNoAccess)))
		}
		permissionKnown = true
	}

	// 2. Which projects does the owner have?
	listed, listErr := gh.OwnerProjects{}, errors.New("no project lister")
	if d.list != nil {
		listed, listErr = d.list(ctx)
	}
	if listErr != nil {
		title := fmt.Sprintf("project %d under %s could not be resolved, and %s's projects could not be listed to say why", cfg.ProjectNumber, cfg.Owner, cfg.Owner)
		return []Finding{newFinding(check, "NGD013", SeverityBlocker, title,
			"the configured board could not be resolved; listing the owner's projects also failed, so the cause is unknown",
			ev(map[string]string{"state": "read_failed", "list_error": listErr.Error()}), []string{board, "read_failed"},
			manualRemedy("access", "Confirm the board exists and the credential can read it", check,
				"Confirm project "+number+" exists under "+cfg.Owner,
				"Confirm the token or App can read projects (classic token: `project` scope; App: Organization → Projects)",
				checkAgainStep("NGD013")))}, title, true
	}
	projectsURL := ownerProjectsURL(listed.Login, boardOwnerType(cfg))
	available, maxNumber := "", 0
	parts := make([]string, 0, len(listed.Projects))
	for _, p := range listed.Projects {
		if p.Number == cfg.ProjectNumber {
			// It exists and is visible: reading its items failed instead.
			title := fmt.Sprintf("project %d under %s (%s) is visible, but its items could not be read", cfg.ProjectNumber, cfg.Owner, p.Title)
			return []Finding{newFinding(check, "NGD013", SeverityBlocker, title,
				"the identity can list the project, but reading its items failed; GitHub's message is in the evidence",
				ev(map[string]string{"state": "read_failed"}), []string{board, "read_failed"},
				withLinks(manualRemedy("access", "Give the credential access to the project's items", check,
					"Share the project with the identity, or grant it project write access",
					checkAgainStep("NGD013")), ownerProjectsURL(listed.Login, boardOwnerType(cfg))))}, title, true
		}
		label := strconv.Itoa(p.Number) + " (" + p.Title
		if p.Closed {
			label += ", closed"
		}
		parts = append(parts, label+")")
		if p.Number > maxNumber {
			maxNumber = p.Number
		}
	}
	available = strings.Join(parts, ", ")
	if len(listed.Projects) == 0 && !permissionKnown {
		return noAccess("no_projects_visible", "no project of "+cfg.Owner+" is visible to it",
			map[string]string{"available": "none"},
			withLinks(manualRemedy("access", "Give the credential read access to "+cfg.Owner+"'s projects", check,
				"Fine-grained token: grant Organization → Projects (read and write) for "+cfg.Owner,
				"Classic token: add the project scope (`gh auth refresh -h github.com -s project`)",
				"GitHub App: add Organization → Projects and have "+cfg.Owner+" accept it",
				checkAgainStep(codeBoardNoAccess)), projectsURL))
	}
	extra := map[string]string{"available": available}
	if available == "" {
		extra["available"] = "none"
	}
	fix := withLinks(manualRemedy("renumber", "Point project_number at one of the owner's projects", check,
		"Set project_number in .nightgauge/config.yaml to one of: "+extra["available"],
		"Or create the board with `nightgauge repo-init`",
		openStep(projectsURL, "See the owner's projects on GitHub"),
		checkAgainStep(codeBoardWrongNumber)), projectsURL)
	if cfg.ProjectNumber > maxNumber {
		title := fmt.Sprintf("project %d does not exist under %s", cfg.ProjectNumber, cfg.Owner)
		if available != "" {
			title += "; its projects are " + available
		} else {
			title += "; it has no projects"
		}
		return []Finding{newFinding(check, codeBoardWrongNumber, SeverityBlocker, title,
			"the configured project number names no project this identity can see, and the identity can see the owner's projects",
			ev(extra), []string{board}, fix)}, title, true
	}
	fix.Steps[len(fix.Steps)-1] = checkAgainStep(codeBoardDeleted)
	title := fmt.Sprintf("project %d under %s was deleted or is not shared with this identity; its projects are %s", cfg.ProjectNumber, cfg.Owner, available)
	return []Finding{newFinding(check, codeBoardDeleted, SeverityBlocker, title,
		"project numbers are never reused, and the owner has projects numbered above this one: this board existed and is gone, or it is private and not shared with the identity",
		ev(extra), []string{board}, fix)}, title, true
}

// --- personal token: gh auth refresh ------------------------------------------

// refreshableScopes is the closed set of scopes the refresh verb may request.
var refreshableScopes = map[string]bool{
	"repo": true, "project": true, "read:project": true, "read:org": true, "workflow": true,
}

// ghRefreshRemedy is the confirm remedy that adds scopes to gh's credential.
func ghRefreshRemedy(check string, scopes []string) Remedy {
	return Remedy{ID: "gh-refresh", Kind: RemedyConfirm, Verb: verbGHAuthRefresh, Verify: check,
		Summary: "Add the " + strings.Join(scopes, ", ") + " scope with `gh auth refresh`",
		Preview: "gh auth refresh -h github.com -s " + strings.Join(scopes, ",") +
			": opens GitHub in a browser (with a one-time code) to grant the scopes to gh's active account; " +
			"refused unless that account is the one doctor checked"}
}

// refreshScopes reads and validates the scopes a finding asks for.
func refreshScopes(f Finding) ([]string, error) {
	raw, err := evidence(f, "missing")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !refreshableScopes[s] {
			return nil, fmt.Errorf("scope %q is not one doctor may request", s)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("finding %s names no scope", f.Code)
	}
	return out, nil
}

// ghCLI runs gh with the ambient GH_TOKEN/GITHUB_TOKEN removed, so gh acts on
// its stored credential. interactive attaches the terminal (the device-code
// flow); otherwise stdout is returned. Tests replace it.
var ghCLI = func(ctx context.Context, interactive bool, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GH_TOKEN=") || strings.HasPrefix(kv, "GITHUB_TOKEN=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env
	if interactive {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stderr, os.Stderr
		return "", cmd.Run()
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// stdinIsTerminal reports whether an operator can answer gh's prompt.
var stdinIsTerminal = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }

// authRefreshPrecondition re-derives the claim: the scopes are ones doctor may
// request, an operator is at a terminal, and gh's active account is the login
// doctor found lacking them (refreshing another account would not help).
func (v *verbs) authRefreshPrecondition(ctx context.Context, f Finding) error {
	if _, err := refreshScopes(f); err != nil {
		return err
	}
	login, err := evidence(f, "login")
	if err != nil {
		return err
	}
	if !stdinIsTerminal() {
		return errors.New("gh auth refresh shows a one-time code and waits for the browser: run `nightgauge doctor --fix` in a terminal")
	}
	active, err := ghCLI(ctx, false, "api", "user", "--jq", ".login")
	if err != nil || active == "" {
		return fmt.Errorf("gh's active account could not be read (is gh installed and logged in?): %v", err)
	}
	if !strings.EqualFold(active, login) {
		return fmt.Errorf("gh's active account is %s, but doctor checked %s's token: refresh %s's credential instead", active, login, login)
	}
	return nil
}

// authRefreshApply runs `gh auth refresh -h github.com -s <scopes>` in the
// terminal, then points doctor's client at the refreshed token so the
// engine's verification reads the new scopes. A GH_TOKEN/GITHUB_TOKEN this
// process exported from the old token is updated to the new one.
func (v *verbs) authRefreshApply(ctx context.Context, f Finding) error {
	scopes, err := refreshScopes(f)
	if err != nil {
		return err
	}
	old, _ := ghCLI(ctx, false, "auth", "token", "-h", "github.com")
	if _, err := ghCLI(ctx, true, "auth", "refresh", "-h", "github.com", "-s", strings.Join(scopes, ",")); err != nil {
		return fmt.Errorf("gh auth refresh: %w", err)
	}
	fresh, err := ghCLI(ctx, false, "auth", "token", "-h", "github.com")
	if err != nil || fresh == "" {
		return fmt.Errorf("gh auth token after refresh: %v", err)
	}
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if cur := os.Getenv(name); old != "" && cur == old {
			_ = os.Setenv(name, fresh)
		}
	}
	// Apply runs after the scan's checks have finished; verification builds
	// its Env from this one, so no check reads Client concurrently.
	v.env.Client = gh.NewClientWithToken(fresh)
	return nil
}
