package connector

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"

	"maunium.net/go/mautrix/bridgev2"
)

// mbiRefreshScope must be sent alone: the identity provider rejects it when
// combined with offline_access ("offline_access scope request must include
// either openid scope or a resource scope", AADSTS70011).
const mbiRefreshScope = "service::api.fl.spaces.skype.com::MBI_SSL"
const mbiTokenEndpoint = "https://login.microsoftonline.com/common/oauth2/v2.0/token"

// mbiTokenEndpointFor returns the endpoint to use for MBI-scope refreshes.
// The MBI scope is only accepted on the /common endpoint, so real Microsoft
// endpoints (tenant-specific, /consumers, /organizations) are rewritten to it.
// Any other endpoint (a test server, a proxy) is left untouched.
func mbiTokenEndpointFor(configured string) string {
	trimmed := strings.TrimSpace(configured)
	if trimmed == "" {
		return mbiTokenEndpoint
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return mbiTokenEndpoint
	}
	if strings.EqualFold(parsed.Host, "login.microsoftonline.com") {
		return mbiTokenEndpoint
	}
	return trimmed
}

const graphFilesReadWriteScope = "https://graph.microsoft.com/Files.ReadWrite"
const graphTeamReadScope = "https://graph.microsoft.com/Team.ReadBasic.All"
const graphChannelReadScope = "https://graph.microsoft.com/Channel.ReadBasic.All"

var newAuthClient = auth.NewClient

// ExtractTeamsLoginMetadataFromLocalStorage parses the MSAL localStorage payload
// and exchanges its access token for a Teams skypetoken.
func ExtractTeamsLoginMetadataFromLocalStorage(ctx context.Context, rawStorage string, main *TeamsConnector) (*teamsid.UserLoginMetadata, error) {
	clientID := resolveClientID(main)
	state, err := auth.ExtractTokensFromMSALLocalStorage(rawStorage, clientID)
	if err != nil {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_INVALID_STORAGE", Err: fmt.Sprintf("Failed to extract tokens: %v", err), StatusCode: http.StatusBadRequest}
	}
	authClient := newConfiguredAuthClient(main)
	accessToken := strings.TrimSpace(state.AccessToken)
	if accessToken == "" {
		refreshToken := strings.TrimSpace(state.RefreshToken)
		if refreshToken == "" {
			return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_ACCESS_TOKEN", Err: "Access token missing from extracted state", StatusCode: http.StatusBadRequest}
		}
		refreshed, refreshErr := refreshAccessTokenForSkypeScope(ctx, authClient, refreshToken)
		if refreshErr != nil {
			return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_ACCESS_TOKEN", Err: fmt.Sprintf("Access token missing from localStorage and refresh failed: %v", refreshErr), StatusCode: http.StatusBadRequest}
		}
		accessToken = strings.TrimSpace(refreshed.AccessToken)
		if accessToken == "" {
			return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_ACCESS_TOKEN", Err: "Access token missing after refresh-token exchange", StatusCode: http.StatusBadRequest}
		}
		if rt := strings.TrimSpace(refreshed.RefreshToken); rt != "" {
			state.RefreshToken = rt
		}
		if refreshed.ExpiresAtUnix != 0 {
			state.ExpiresAtUnix = refreshed.ExpiresAtUnix
		}
		if token := strings.TrimSpace(refreshed.GraphAccessToken); token != "" {
			state.GraphAccessToken = token
			state.GraphExpiresAt = refreshed.GraphExpiresAt
		}
	}
	if strings.TrimSpace(state.GraphAccessToken) == "" {
		refreshToken := strings.TrimSpace(state.RefreshToken)
		if refreshToken != "" {
			graphState, graphErr := refreshAccessTokenForGraphScope(ctx, authClient, refreshToken)
			if graphErr == nil {
				if token := strings.TrimSpace(graphState.GraphAccessToken); token != "" {
					state.GraphAccessToken = token
					state.GraphExpiresAt = graphState.GraphExpiresAt
				}
				if rt := strings.TrimSpace(graphState.RefreshToken); rt != "" {
					state.RefreshToken = rt
				}
			}
		}
	}

	skResult, err := authClient.AcquireSkypeToken(ctx, accessToken)
	if err != nil {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_SKYPETOKEN_FAILED", Err: fmt.Sprintf("Failed to acquire skypetoken: %v", err), StatusCode: http.StatusBadRequest}
	}

	teamsUserID := auth.NormalizeTeamsUserID(skResult.SkypeID)
	if teamsUserID == "" {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_USER_ID", Err: "Teams user ID missing from skypetoken response", StatusCode: http.StatusBadRequest}
	}

	return &teamsid.UserLoginMetadata{
		RefreshToken:         state.RefreshToken,
		AccessTokenExpiresAt: state.ExpiresAtUnix,
		SkypeToken:           skResult.Token,
		SkypeTokenExpiresAt:  skResult.ExpiresAt,
		GraphAccessToken:     strings.TrimSpace(state.GraphAccessToken),
		GraphExpiresAt:       state.GraphExpiresAt,
		TeamsUserID:          teamsUserID,
		RegionChatServiceURL: skResult.ChatServiceURL,
		RegionAmsURL:         skResult.AmsURL,
	}, nil
}

func refreshAccessTokenForSkypeScope(ctx context.Context, client *auth.Client, refreshToken string) (*auth.AuthState, error) {
	// Prefer requesting the Skype MBI scope explicitly for skypetoken bootstrap.
	// The MBI scope only works on the /common endpoint, not tenant-specific ones.
	retryClient := *client
	retryClient.Scopes = []string{mbiRefreshScope}
	retryClient.TokenEndpoint = mbiTokenEndpointFor(client.TokenEndpoint)
	refreshed, err := retryClient.RefreshAccessToken(ctx, refreshToken)
	if err == nil {
		return refreshed, nil
	}

	// Fallback to default scopes for environments that don't accept MBI scope on refresh.
	fallbackClient := *client
	fallbackClient.Scopes = []string{"openid", "profile", "offline_access"}
	refreshed, fallbackErr := fallbackClient.RefreshAccessToken(ctx, refreshToken)
	if fallbackErr == nil {
		return refreshed, nil
	}

	return nil, fmt.Errorf("MBI scope refresh failed (%v); default scopes failed (%v)", err, fallbackErr)
}

// refreshAccessTokenForGraphScopeWithMeta refreshes a Graph token. Device
// code logins use the configured Graph scope against the tenant endpoint;
// localStorage logins keep the legacy scope fallbacks.
func refreshAccessTokenForGraphScopeWithMeta(ctx context.Context, client *auth.Client, refreshToken string, main *TeamsConnector, meta *teamsid.UserLoginMetadata) (*auth.AuthState, error) {
	if meta != nil && strings.TrimSpace(meta.RefreshScope) != "" {
		graphClient := *client
		graphClient.Scopes = strings.Fields(main.deviceCodeGraphScope())
		return graphClient.RefreshAccessToken(ctx, refreshToken)
	}
	return refreshAccessTokenForGraphScope(ctx, client, refreshToken)
}

func refreshAccessTokenForGraphScope(ctx context.Context, client *auth.Client, refreshToken string) (*auth.AuthState, error) {
	retryClient := *client
	retryClient.Scopes = []string{graphFilesReadWriteScope, graphTeamReadScope, graphChannelReadScope}
	refreshed, err := retryClient.RefreshAccessToken(ctx, refreshToken)
	if err == nil {
		return refreshed, nil
	}

	fallbackClient := *client
	fallbackClient.Scopes = []string{"openid", "profile", "offline_access", graphFilesReadWriteScope, graphTeamReadScope, graphChannelReadScope}
	refreshed, fallbackErr := fallbackClient.RefreshAccessToken(ctx, refreshToken)
	if fallbackErr == nil {
		return refreshed, nil
	}

	return nil, fmt.Errorf("graph scope refresh failed (%v); fallback scopes failed (%v)", err, fallbackErr)
}

func resolveClientID(main *TeamsConnector) string {
	if main != nil {
		if id := strings.TrimSpace(main.Config.ClientID); id != "" {
			return id
		}
	}
	return auth.NewClient(nil).ClientID
}

// newConfiguredAuthClient creates an auth.Client with config overrides applied.
func newConfiguredAuthClient(main *TeamsConnector) *auth.Client {
	return newConfiguredAuthClientForLogin(main, nil)
}

// newConfiguredAuthClientForLogin creates an auth.Client with config overrides applied,
// preferring per-login tenant settings over global config.
func newConfiguredAuthClientForLogin(main *TeamsConnector, meta *teamsid.UserLoginMetadata) *auth.Client {
	client := newAuthClient(nil)
	if main != nil {
		if id := strings.TrimSpace(main.Config.ClientID); id != "" {
			client.ClientID = id
		}
		if ep := strings.TrimSpace(main.Config.AuthorizeEndpoint); ep != "" {
			client.AuthorizeEndpoint = ep
		}
		if ep := strings.TrimSpace(main.Config.TokenEndpoint); ep != "" {
			client.TokenEndpoint = ep
		}
		if ep := strings.TrimSpace(main.Config.SkypeTokenEndpoint); ep != "" {
			client.SkypeTokenEndpoint = ep
		}
		if uri := strings.TrimSpace(main.Config.RedirectURI); uri != "" {
			client.RedirectURI = uri
		}
	}
	// Per-login overrides take precedence over global config.
	if meta != nil {
		if id := strings.TrimSpace(meta.ClientID); id != "" {
			client.ClientID = id
		}
		if ep := strings.TrimSpace(meta.AuthorizeEndpoint); ep != "" {
			client.AuthorizeEndpoint = ep
		}
		if ep := strings.TrimSpace(meta.TokenEndpoint); ep != "" {
			client.TokenEndpoint = ep
		}
		if ep := strings.TrimSpace(meta.SkypeTokenEndpoint); ep != "" {
			client.SkypeTokenEndpoint = ep
		}
		if uri := strings.TrimSpace(meta.RedirectURI); uri != "" {
			client.RedirectURI = uri
		}
	}
	return client
}
