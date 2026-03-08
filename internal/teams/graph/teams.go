package graph

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type Team struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type Channel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// ChannelInfo pairs a channel with its parent team.
type ChannelInfo struct {
	ChannelName string
	TeamName    string
	TeamID      string
}

// ListJoinedTeamsAndChannels fetches all teams the user belongs to and their
// channels, returning a map from channel thread ID to ChannelInfo.
// Channel thread IDs in the Graph response use the format "19:...@thread.tacv2".
func (c *GraphClient) ListJoinedTeamsAndChannels(ctx context.Context) (map[string]ChannelInfo, error) {
	if c == nil || c.HTTP == nil {
		return nil, ErrMissingGraphHTTPClient
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return nil, ErrMissingGraphAccessToken
	}

	teams, err := c.ListJoinedTeams(ctx)
	if err != nil {
		return nil, err
	}

	result := make(map[string]ChannelInfo)
	for _, team := range teams {
		channels, err := c.listTeamChannels(ctx, team.ID)
		if err != nil {
			continue
		}
		for _, ch := range channels {
			chID := strings.TrimSpace(ch.ID)
			if chID == "" {
				continue
			}
			result[chID] = ChannelInfo{
				ChannelName: strings.TrimSpace(ch.DisplayName),
				TeamName:    strings.TrimSpace(team.DisplayName),
				TeamID:      team.ID,
			}
		}
	}
	return result, nil
}

// ListJoinedTeams fetches all teams the user has joined.
func (c *GraphClient) ListJoinedTeams(ctx context.Context) ([]Team, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://graph.microsoft.com/v1.0/me/joinedTeams?$select=id,displayName", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, GraphUploadError{Status: resp.StatusCode, BodySnippet: string(snippet)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Value []Team `json:"value"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Value, nil
}

func (c *GraphClient) listTeamChannels(ctx context.Context, teamID string) ([]Channel, error) {
	endpoint := "https://graph.microsoft.com/v1.0/teams/" + teamID + "/channels?$select=id,displayName"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.AccessToken))
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxUploadErrorBytes))
		return nil, GraphUploadError{Status: resp.StatusCode, BodySnippet: string(snippet)}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Value []Channel `json:"value"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload.Value, nil
}
