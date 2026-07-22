package platform

import (
	"context"
	"fmt"
	"time"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// TeamMemberResult is the IPC-facing representation of a team member.
type TeamMemberResult struct {
	UserID   string    `json:"userId"`
	Email    string    `json:"email"`
	Name     string    `json:"name,omitempty"`
	Role     string    `json:"role"`
	JoinedAt time.Time `json:"joinedAt"`
}

// TeamService wraps the platform API's team member endpoints.
type TeamService struct {
	client *Client
}

// NewTeamService creates a team member service.
func NewTeamService(client *Client) *TeamService {
	return &TeamService{client: client}
}

// GetMembers returns the list of team members. Returns an empty slice if offline.
func (s *TeamService) GetMembers(ctx context.Context) ([]TeamMemberResult, error) {
	if !s.client.IsOnline() {
		return []TeamMemberResult{}, nil
	}

	resp, err := s.client.api.ListTeamMembersWithResponse(ctx, &api.ListTeamMembersParams{})
	if err != nil {
		return nil, fmt.Errorf("list team members: %w", err)
	}

	if resp.JSON200 == nil {
		return nil, fmt.Errorf("list team members: unexpected response %d", resp.StatusCode())
	}

	members := make([]TeamMemberResult, len(resp.JSON200.Data))
	for i, m := range resp.JSON200.Data {
		members[i] = TeamMemberResult{
			UserID:   m.UserId,
			Email:    string(m.Email),
			Role:     string(m.Role),
			JoinedAt: m.JoinedAt,
		}
		if m.Name != nil {
			members[i].Name = *m.Name
		}
	}

	return members, nil
}
