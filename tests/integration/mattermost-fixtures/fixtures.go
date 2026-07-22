// Package mmfixtures provides a deterministic Mattermost fixture seeder
// for the integration test harness (#3381). Resources are created
// idempotently — each Seed call first checks for an existing fixture by
// deterministic name and reuses it instead of erroring out, so the
// harness is safe to re-run after a partial failure both locally and in
// CI.
//
// The seeder talks to the Mattermost REST API v4 directly via net/http
// rather than pulling in a Mattermost client dependency — it mirrors the
// GitLab seeder in tests/integration/seed for consistency. See ADR-002
// in the issue knowledge base for why team-edition + a separate Postgres
// is used over the deprecated all-in-one preview image.
package mmfixtures

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Deterministic fixture names. Every ensureXxx method keys idempotency
// off these, so a re-run reuses the resources a prior run created.
const (
	defaultTeamName     = "nightgauge-test"
	defaultTeamDisplay  = "Nightgauge Test"
	defaultChannelName  = "ci-test-channel"
	defaultChannelDisp  = "CI Test Channel"
	defaultBotUsername  = "mm-ci-bot"
	defaultBotPassword  = "Nightgauge-Bot-1"
	incomingWebhookName = "ci-incoming"
	outgoingWebhookName = "ci-outgoing"
	adminEmail          = "admin@nightgauge.test"

	// outgoingTriggerWord is registered on the fixture outgoing webhook.
	// The slash-command tests do not depend on Mattermost actually
	// delivering the webhook (they POST to the receiver directly — see
	// ADR-003), but a non-empty trigger word keeps the webhook valid.
	outgoingTriggerWord = "/nightgauge"

	// outgoingCallbackURL is a placeholder — the slash-command tests
	// capture the webhook's signing token and POST to the in-process
	// receiver themselves, so Mattermost never calls this URL.
	outgoingCallbackURL = "http://localhost:1"
)

// Fixtures is the JSON-serializable output of a seed run. Tests read it
// to learn the IDs, URLs, and tokens they need.
type Fixtures struct {
	BaseURL              string `json:"base_url"`
	AdminToken           string `json:"admin_token"`
	TeamID               string `json:"team_id"`
	TeamName             string `json:"team_name"`
	ChannelID            string `json:"channel_id"`
	ChannelName          string `json:"channel_name"`
	BotUserID            string `json:"bot_user_id"`
	IncomingWebhookID    string `json:"incoming_webhook_id"`
	IncomingWebhookURL   string `json:"incoming_webhook_url"`
	OutgoingWebhookID    string `json:"outgoing_webhook_id"`
	OutgoingWebhookToken string `json:"outgoing_webhook_token"`
}

// Seeder holds a Mattermost REST client and creates deterministic
// fixtures. AdminUser/AdminPass bootstrap the system-admin session that
// every REST call after login authenticates with.
type Seeder struct {
	BaseURL   string
	AdminUser string
	AdminPass string
	HTTP      *http.Client

	// token is the admin session token captured by login during Seed.
	token string
}

// NewSeeder constructs a Seeder against the given Mattermost instance.
// adminUser/adminPass must identify a system-admin account — on a fresh
// instance the seeder creates it (the first account on an open server
// is granted system-admin).
func NewSeeder(baseURL, adminUser, adminPass string) *Seeder {
	return &Seeder{
		BaseURL:   strings.TrimRight(baseURL, "/"),
		AdminUser: adminUser,
		AdminPass: adminPass,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// Seed performs the full fixture creation: admin session, team,
// channel, bot user, incoming webhook, outgoing webhook. Idempotent —
// re-running against the same instance returns the existing fixtures
// rather than erroring.
func (s *Seeder) Seed(ctx context.Context) (*Fixtures, error) {
	if s.BaseURL == "" {
		return nil, errors.New("mmfixtures: BaseURL is required")
	}
	if s.AdminUser == "" || s.AdminPass == "" {
		return nil, errors.New("mmfixtures: AdminUser and AdminPass are required")
	}

	if err := s.ensureAdminSession(ctx); err != nil {
		return nil, fmt.Errorf("ensure admin session: %w", err)
	}

	out := &Fixtures{BaseURL: s.BaseURL, AdminToken: s.token}

	team, err := s.ensureTeam(ctx, defaultTeamName)
	if err != nil {
		return nil, fmt.Errorf("ensure team: %w", err)
	}
	out.TeamID = team.ID
	out.TeamName = team.Name

	ch, err := s.ensureChannel(ctx, team.ID, defaultChannelName)
	if err != nil {
		return nil, fmt.Errorf("ensure channel: %w", err)
	}
	out.ChannelID = ch.ID
	out.ChannelName = ch.Name

	botID, err := s.ensureBotUser(ctx, team.ID, defaultBotUsername, defaultBotPassword)
	if err != nil {
		return nil, fmt.Errorf("ensure bot user: %w", err)
	}
	out.BotUserID = botID

	inID, err := s.ensureIncomingWebhook(ctx, ch.ID, incomingWebhookName)
	if err != nil {
		return nil, fmt.Errorf("ensure incoming webhook: %w", err)
	}
	out.IncomingWebhookID = inID
	out.IncomingWebhookURL = s.BaseURL + "/hooks/" + inID

	outID, outToken, err := s.ensureOutgoingWebhook(ctx, team.ID, ch.ID, outgoingWebhookName)
	if err != nil {
		return nil, fmt.Errorf("ensure outgoing webhook: %w", err)
	}
	out.OutgoingWebhookID = outID
	out.OutgoingWebhookToken = outToken

	return out, nil
}

// ---------- admin session ----------

// ensureAdminSession logs in as the configured admin user and stores the
// session token. On a fresh Mattermost instance no users exist yet, so a
// 401 triggers a one-shot create-the-first-user attempt (the first
// account on an open server is granted system-admin) followed by a retry.
func (s *Seeder) ensureAdminSession(ctx context.Context) error {
	err := s.login(ctx)
	if err == nil {
		return nil
	}
	if !isStatus(err, http.StatusUnauthorized) {
		return err
	}

	// First boot: create the system-admin account, then retry login.
	body := map[string]any{
		"email":    adminEmail,
		"username": s.AdminUser,
		"password": s.AdminPass,
	}
	if cErr := s.doJSON(ctx, http.MethodPost, "/api/v4/users", body, nil); cErr != nil {
		// Tolerate "already exists" (400) — a concurrent seeder or the
		// CI bootstrap step may have won the race. Any other error is
		// fatal.
		if !isStatus(cErr, http.StatusBadRequest) {
			return fmt.Errorf("create admin user: %w", cErr)
		}
	}
	return s.login(ctx)
}

// login posts admin credentials to /users/login and captures the session
// token from the Token response header (Mattermost returns the token in
// a header, not the body).
func (s *Seeder) login(ctx context.Context) error {
	payload, err := json.Marshal(map[string]string{
		"login_id": s.AdminUser,
		"password": s.AdminPass,
	})
	if err != nil {
		return fmt.Errorf("marshal login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.BaseURL+"/api/v4/users/login", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &apiError{Status: resp.StatusCode, Body: string(snippet), Op: "POST /api/v4/users/login"}
	}
	tok := resp.Header.Get("Token")
	if tok == "" {
		return errors.New("mmfixtures: login succeeded but Token response header was empty")
	}
	s.token = tok
	return nil
}

// ---------- team ----------

type team struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (s *Seeder) ensureTeam(ctx context.Context, name string) (*team, error) {
	var existing team
	err := s.doJSON(ctx, http.MethodGet, "/api/v4/teams/name/"+name, nil, &existing)
	if err == nil && existing.ID != "" {
		return &existing, nil
	}
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return nil, err
	}
	body := map[string]any{
		"name":         name,
		"display_name": defaultTeamDisplay,
		"type":         "O", // open team
	}
	var created team
	if err := s.doJSON(ctx, http.MethodPost, "/api/v4/teams", body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// ---------- channel ----------

type channel struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

func (s *Seeder) ensureChannel(ctx context.Context, teamID, name string) (*channel, error) {
	var existing channel
	err := s.doJSON(ctx, http.MethodGet,
		fmt.Sprintf("/api/v4/teams/%s/channels/name/%s", teamID, name), nil, &existing)
	if err == nil && existing.ID != "" {
		return &existing, nil
	}
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return nil, err
	}
	body := map[string]any{
		"team_id":      teamID,
		"name":         name,
		"display_name": defaultChannelDisp,
		"type":         "O", // public channel — outgoing webhooks require it
	}
	var created channel
	if err := s.doJSON(ctx, http.MethodPost, "/api/v4/channels", body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// ---------- bot user ----------

// ensureBotUser creates a regular user account that stands in as the
// test bot and adds it to the team. Mattermost's dedicated bot-account
// API needs extra permission scoping; a plain user is sufficient for the
// fixture and keeps the seeder simple.
func (s *Seeder) ensureBotUser(ctx context.Context, teamID, username, password string) (string, error) {
	var existing struct {
		ID string `json:"id"`
	}
	err := s.doJSON(ctx, http.MethodGet, "/api/v4/users/username/"+username, nil, &existing)
	if err != nil && !isStatus(err, http.StatusNotFound) {
		return "", err
	}

	userID := existing.ID
	if userID == "" {
		body := map[string]any{
			"email":    username + "@nightgauge.test",
			"username": username,
			"password": password,
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := s.doJSON(ctx, http.MethodPost, "/api/v4/users", body, &created); err != nil {
			return "", err
		}
		userID = created.ID
	}
	if userID == "" {
		return "", errors.New("mmfixtures: bot user creation returned empty id")
	}

	// Add to the team. A 400/409 means the membership already exists —
	// tolerate it so re-runs stay idempotent.
	member := map[string]any{"team_id": teamID, "user_id": userID}
	if err := s.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/v4/teams/%s/members", teamID), member, nil); err != nil {
		if !isStatus(err, http.StatusBadRequest) && !isStatus(err, http.StatusConflict) {
			return "", fmt.Errorf("add team member: %w", err)
		}
	}
	return userID, nil
}

// ---------- incoming webhook ----------

// ensureIncomingWebhook returns the id of the channel's incoming webhook,
// creating it if no webhook with the deterministic display name exists.
func (s *Seeder) ensureIncomingWebhook(ctx context.Context, channelID, displayName string) (string, error) {
	var existing []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		ChannelID   string `json:"channel_id"`
	}
	if err := s.doJSON(ctx, http.MethodGet,
		"/api/v4/hooks/incoming?page=0&per_page=200", nil, &existing); err != nil {
		return "", err
	}
	for _, h := range existing {
		if h.DisplayName == displayName && h.ChannelID == channelID {
			return h.ID, nil
		}
	}
	body := map[string]any{
		"channel_id":   channelID,
		"display_name": displayName,
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := s.doJSON(ctx, http.MethodPost, "/api/v4/hooks/incoming", body, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("mmfixtures: incoming webhook creation returned empty id")
	}
	return created.ID, nil
}

// ---------- outgoing webhook ----------

// ensureOutgoingWebhook returns the id and signing token of the channel's
// outgoing webhook, creating it if no webhook with the deterministic
// display name exists. The token is the value the slash-command tests
// verify against — Mattermost returns it both on creation and in the
// list response, so idempotent re-runs recover it without recreating.
func (s *Seeder) ensureOutgoingWebhook(ctx context.Context, teamID, channelID, displayName string) (id, token string, err error) {
	var existing []struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		ChannelID   string `json:"channel_id"`
		Token       string `json:"token"`
	}
	if err := s.doJSON(ctx, http.MethodGet,
		"/api/v4/hooks/outgoing?page=0&per_page=200", nil, &existing); err != nil {
		return "", "", err
	}
	for _, h := range existing {
		if h.DisplayName == displayName && h.ChannelID == channelID {
			if h.Token == "" {
				return "", "", fmt.Errorf("mmfixtures: existing outgoing webhook %q has an empty token", h.ID)
			}
			return h.ID, h.Token, nil
		}
	}
	body := map[string]any{
		"team_id":       teamID,
		"channel_id":    channelID,
		"display_name":  displayName,
		"trigger_words": []string{outgoingTriggerWord},
		"callback_urls": []string{outgoingCallbackURL},
	}
	var created struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := s.doJSON(ctx, http.MethodPost, "/api/v4/hooks/outgoing", body, &created); err != nil {
		return "", "", err
	}
	if created.ID == "" || created.Token == "" {
		return "", "", errors.New("mmfixtures: outgoing webhook creation returned empty id or token")
	}
	return created.ID, created.Token, nil
}

// ---------- low-level HTTP ----------

// apiError carries the status code and body snippet of a non-2xx
// Mattermost response so callers can switch on the status.
type apiError struct {
	Status int
	Body   string
	Op     string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("mattermost %s: status %d: %s", e.Op, e.Status, e.Body)
}

// isStatus reports whether err is an *apiError carrying the given HTTP
// status code. The ensureXxx methods use it to distinguish "not found,
// so create it" from a genuine transport or server failure.
func isStatus(err error, status int) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) {
		return apiErr.Status == status
	}
	return false
}

// doJSON issues a request and (optionally) decodes the JSON response.
// When out is nil the response body is drained and discarded. Non-2xx
// responses return an *apiError so callers can switch on status code.
func (s *Seeder) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal: %w", err)
		}
		rdr = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.BaseURL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("do %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &apiError{Status: resp.StatusCode, Body: string(snippet), Op: method + " " + path}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}
