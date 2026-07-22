//go:build integration

package mattermost_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// channelPost is the subset of a Mattermost post object the outbound
// test asserts on.
type channelPost struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

// getChannelPosts fetches the recent posts for a channel via the
// Mattermost REST API. The response is a map keyed by post id plus an
// `order` slice; this helper flattens it into a plain slice.
func getChannelPosts(t *testing.T, channelID, adminToken string) []channelPost {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		mmURL+"/api/v4/channels/"+channelID+"/posts", nil)
	if err != nil {
		t.Fatalf("build posts request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get channel posts: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get channel posts: status %d, want 200", resp.StatusCode)
	}

	var page struct {
		Order []string               `json:"order"`
		Posts map[string]channelPost `json:"posts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatalf("decode channel posts: %v", err)
	}
	out := make([]channelPost, 0, len(page.Order))
	for _, id := range page.Order {
		out = append(out, page.Posts[id])
	}
	return out
}

// TestMattermost_OutboundPost_LandsInChannel verifies that posting to the
// fixture incoming webhook URL delivers a message into the test channel —
// the outbound (us → Mattermost) half of the integration.
func TestMattermost_OutboundPost_LandsInChannel(t *testing.T) {
	if fixtures == nil {
		t.Skip("fixtures not seeded")
	}

	const marker = "ci-test pipeline event"
	payload := `{"text": "` + marker + `", "username": "nightgauge-ci"}`

	resp, err := http.Post(fixtures.IncomingWebhookURL, "application/json",
		strings.NewReader(payload))
	if err != nil {
		t.Fatalf("post to incoming webhook: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("incoming webhook POST: status %d, want 200", resp.StatusCode)
	}

	// Mattermost delivers incoming-webhook posts asynchronously; poll the
	// channel until our marker shows up or the budget elapses.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range getChannelPosts(t, fixtures.ChannelID, fixtures.AdminToken) {
			if strings.Contains(p.Message, marker) {
				return // found — test passes
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("message %q not found in channel %q within 5s", marker, fixtures.ChannelName)
}
