package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/model"
)

// GetContactList returns the user's org directory as a contact list.
func (c *TeamsClient) GetContactList(ctx context.Context) ([]*bridgev2.ResolveIdentifierResponse, error) {
	if !c.IsLoggedIn() {
		return nil, bridgev2.ErrNotLoggedIn
	}
	if err := c.ensureValidGraphToken(ctx); err != nil {
		return nil, fmt.Errorf("graph token required for contact list: %w", err)
	}

	gc, err := c.getGraphClient(ctx)
	if err != nil {
		return nil, err
	}

	users, err := gc.ListDirectoryUsers(ctx)
	if err != nil {
		return nil, fmt.Errorf("directory listing failed: %w", err)
	}

	selfID := ""
	if c.Meta != nil {
		selfID = model.NormalizeTeamsUserID(c.Meta.TeamsUserID)
	}

	results := make([]*bridgev2.ResolveIdentifierResponse, 0, len(users))
	for _, u := range users {
		userID := model.NormalizeTeamsUserID(u.ID)
		if selfID != "" && userID == selfID {
			continue
		}
		// Track for presence polling.
		c.trackKnownUser(userID)

		name := u.DisplayName
		var identifiers []string
		if u.Mail != "" {
			identifiers = append(identifiers, "email:"+u.Mail)
		}
		results = append(results, &bridgev2.ResolveIdentifierResponse{
			UserID: networkid.UserID(userID),
			UserInfo: &bridgev2.UserInfo{
				Name:        &name,
				Identifiers: identifiers,
			},
		})
	}
	return results, nil
}
