package connector

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/graph"
	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// ResolveIdentifier looks up a Teams user by email or MRI and optionally creates a DM chat.
func (c *TeamsClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		return nil, fmt.Errorf("graph token required for user lookup: %w", err)
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}

	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return nil, errors.New("empty identifier")
	}

	// Try to look up by email first.
	user, err := gc.GetUserByEmail(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("graph user lookup failed: %w", err)
	}

	// If not found by email, check if it's a raw MRI.
	if user == nil {
		objID := extractObjectIDFromMRI(identifier)
		if objID != "" {
			user, err = gc.GetUserByEmail(ctx, objID)
			if err != nil {
				return nil, fmt.Errorf("graph user lookup by MRI failed: %w", err)
			}
		}
	}

	if user == nil {
		return nil, fmt.Errorf("user not found: %s", identifier)
	}

	userID := networkid.UserID(model.NormalizeTeamsUserID(user.ID))
	resp := &bridgev2.ResolveIdentifierResponse{
		UserID: userID,
		UserInfo: &bridgev2.UserInfo{
			Name: &user.DisplayName,
		},
	}

	if createChat {
		selfMRI := "8:orgid:" + extractObjectIDFromMRI(c.Meta.TeamsUserID)
		targetMRI := "8:orgid:" + user.ID

		threadID, err := c.getAPI().CreateConversation(ctx, []string{selfMRI, targetMRI})
		if err != nil {
			return nil, fmt.Errorf("failed to create conversation: %w", err)
		}

		resp.Chat = &bridgev2.CreateChatResponse{
			PortalKey: networkid.PortalKey{
				ID:       networkid.PortalID(threadID),
				Receiver: c.Login.ID,
			},
		}
	}

	return resp, nil
}

// SearchUsers searches the Teams directory for users matching a query.
func (c *TeamsClient) SearchUsers(ctx context.Context, query string) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		return nil, fmt.Errorf("graph token required for user search: %w", err)
	}

	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	people, err := gc.SearchPeople(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("people search failed: %w", err)
	}

	results := make([]*bridgev2.ResolveIdentifierResponse, 0, len(people))
	for _, p := range people {
		name := p.DisplayName
		results = append(results, &bridgev2.ResolveIdentifierResponse{
			UserID: networkid.UserID(model.NormalizeTeamsUserID(p.ID)),
			UserInfo: &bridgev2.UserInfo{
				Name: &name,
			},
		})
	}
	return results, nil
}

// CreateGroup creates a new Teams group chat from Matrix.
func (c *TeamsClient) CreateGroup(ctx context.Context, params *bridgev2.GroupCreateParams) (*bridgev2.CreateChatResponse, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidSkypeToken(ctx); err != nil {
		return nil, err
	}
	if params == nil || len(params.Participants) == 0 {
		return nil, errors.New("at least one participant is required")
	}

	selfMRI := "8:orgid:" + extractObjectIDFromMRI(c.Meta.TeamsUserID)
	mris := []string{selfMRI}
	for _, uid := range params.Participants {
		mri := "8:orgid:" + extractObjectIDFromMRI(string(uid))
		mris = append(mris, mri)
	}

	topic := ""
	if params.Name != nil && params.Name.Name != "" {
		topic = params.Name.Name
	}

	threadID, err := c.getAPI().CreateGroupConversation(ctx, topic, mris)
	if err != nil {
		return nil, fmt.Errorf("failed to create group conversation: %w", err)
	}

	return &bridgev2.CreateChatResponse{
		PortalKey: networkid.PortalKey{
			ID:       networkid.PortalID(threadID),
			Receiver: c.Login.ID,
		},
	}, nil
}

// getGraphClient creates a GraphClient with the current Graph access token.
func (c *TeamsClient) getGraphClient(ctx context.Context) (*graph.GraphClient, error) {
	graphToken, err := c.Meta.GetGraphAccessToken()
	if err != nil || graphToken == "" {
		return nil, fmt.Errorf("no graph access token available")
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil, fmt.Errorf("no http client available")
	}
	gc := graph.NewClient(httpClient)
	gc.AccessToken = graphToken
	return gc, nil
}

// extractObjectIDFromMRI extracts the Azure AD object ID from a Teams MRI like "8:orgid:uuid".
func extractObjectIDFromMRI(mri string) string {
	mri = strings.TrimSpace(mri)
	if idx := strings.LastIndex(mri, ":"); idx >= 0 {
		return mri[idx+1:]
	}
	return mri
}
