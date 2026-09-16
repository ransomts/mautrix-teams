package connector

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
	"go.mau.fi/mautrix-teams/pkg/teamsid"
)

const (
	FlowIDDeviceCode          = "device_code"
	LoginStepIDDeviceCode     = "go.mau.teams.device_code"
	LoginMethodDeviceCode     = "device_code"
	deviceCodeDefaultVerifyRL = "https://microsoft.com/devicelogin"
)

var loginFlowDeviceCode = bridgev2.LoginFlow{
	Name:        "Microsoft account (device code)",
	Description: "Sign in on any browser with a one-time code. Issues a 90-day refresh token instead of the 24-hour token taken from the Teams web app.",
	ID:          FlowIDDeviceCode,
}

// DeviceCodeLogin implements the OAuth device authorization grant as a
// bridgev2 display-and-wait login: Start shows the user code, Wait polls the
// token endpoint until the user has signed in.
type DeviceCodeLogin struct {
	Main *TeamsConnector
	User *bridgev2.User

	mu         sync.Mutex
	authClient *auth.Client
	deviceCode *auth.DeviceCode
	cancel     context.CancelFunc
}

var _ bridgev2.LoginProcessDisplayAndWait = (*DeviceCodeLogin)(nil)

func (l *DeviceCodeLogin) deviceCodeAuthClient() *auth.Client {
	client := newConfiguredAuthClient(l.Main)
	client.ClientID = l.Main.Config.ResolvedDeviceCodeClientID()
	client.Scopes = strings.Fields(l.Main.Config.ResolvedDeviceCodeScope())
	if l.User != nil {
		client.Log = &l.User.Log
	}
	return client
}

func (l *DeviceCodeLogin) Start(ctx context.Context) (*bridgev2.LoginStep, error) {
	if l == nil || l.Main == nil || l.User == nil {
		return nil, errors.New("missing login state")
	}
	l.User.Log.Info().Msg("Starting Teams device code login flow")
	client := l.deviceCodeAuthClient()
	dc, err := client.RequestDeviceCode(ctx, nil)
	if err != nil {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_DEVICE_CODE_FAILED", Err: fmt.Sprintf("Failed to start device code login: %v", err), StatusCode: http.StatusBadGateway}
	}
	l.mu.Lock()
	l.authClient = client
	l.deviceCode = dc
	l.mu.Unlock()

	verifyURL := strings.TrimSpace(dc.VerificationURI)
	if verifyURL == "" {
		verifyURL = deviceCodeDefaultVerifyRL
	}
	minutes := int(time.Until(dc.ExpiresAt).Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeDisplayAndWait,
		StepID:       LoginStepIDDeviceCode,
		Instructions: fmt.Sprintf("Open %s in any browser, sign in with your Microsoft account and enter the code below. The code expires in %d minutes.", verifyURL, minutes),
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeCode,
			Data: dc.UserCode,
		},
	}, nil
}

func (l *DeviceCodeLogin) Cancel() {
	if l == nil {
		return
	}
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if l.User != nil {
		l.User.Log.Warn().Msg("Teams device code login was canceled")
	}
}

func (l *DeviceCodeLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if l == nil || l.Main == nil || l.User == nil {
		return nil, errors.New("missing login state")
	}
	l.mu.Lock()
	client, dc := l.authClient, l.deviceCode
	if client == nil || dc == nil {
		l.mu.Unlock()
		return nil, errors.New("device code login has not been started")
	}
	pollCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.mu.Unlock()
	defer cancel()

	state, err := client.PollDeviceCode(pollCtx, dc)
	if err != nil {
		if errors.Is(err, auth.ErrDeviceCodeExpired) {
			return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_DEVICE_CODE_EXPIRED", Err: "The code expired before the sign-in was completed. Start the login again.", StatusCode: http.StatusBadRequest}
		}
		var dcErr *auth.DeviceCodeError
		if errors.As(err, &dcErr) {
			return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_DEVICE_CODE_DECLINED", Err: dcErr.Error(), StatusCode: http.StatusBadRequest}
		}
		return nil, err
	}

	meta, err := buildDeviceCodeLoginMetadata(ctx, client, state, l.Main)
	if err != nil {
		return nil, err
	}
	l.User.Log.Info().
		Str("teams_user_id", meta.TeamsUserID).
		Bool("graph_token_present", strings.TrimSpace(meta.GraphAccessToken) != "").
		Msg("Teams device code login complete")

	ul, err := l.User.NewLogin(ctx, &database.UserLogin{
		ID:         networkid.UserLoginID(meta.TeamsUserID),
		RemoteName: meta.TeamsUserID,
		Metadata:   meta,
	}, &bridgev2.NewLoginParams{DeleteOnConflict: true})
	if err != nil {
		return nil, err
	}
	startLoginConnect(ul, loginConnectBaseCtx(l.Main))
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		StepID:       "go.mau.teams.complete",
		Instructions: "Login complete.",
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// buildDeviceCodeLoginMetadata exchanges a freshly obtained device-code token
// for a skypetoken, best-effort obtains a Graph token, and assembles the login
// metadata the refresh path relies on.
func buildDeviceCodeLoginMetadata(ctx context.Context, client *auth.Client, state *auth.AuthState, main *TeamsConnector) (*teamsid.UserLoginMetadata, error) {
	if client == nil || state == nil {
		return nil, errors.New("missing auth state")
	}
	accessToken := strings.TrimSpace(state.AccessToken)
	if accessToken == "" {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_ACCESS_TOKEN", Err: "Token response had no access token", StatusCode: http.StatusBadGateway}
	}
	refreshToken := strings.TrimSpace(state.RefreshToken)
	if refreshToken == "" {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_REFRESH_TOKEN", Err: "Token response had no refresh token; make sure the scope includes offline_access", StatusCode: http.StatusBadGateway}
	}

	skResult, err := client.AcquireSkypeToken(ctx, accessToken)
	if err != nil {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_SKYPETOKEN_FAILED", Err: fmt.Sprintf("Failed to acquire skypetoken: %v", err), StatusCode: http.StatusBadGateway}
	}
	teamsUserID := auth.NormalizeTeamsUserID(skResult.SkypeID)
	if teamsUserID == "" {
		return nil, bridgev2.RespError{ErrCode: "FI.MAU.TEAMS_MISSING_USER_ID", Err: "Teams user ID missing from skypetoken response", StatusCode: http.StatusBadGateway}
	}

	meta := &teamsid.UserLoginMetadata{
		RefreshToken:         refreshToken,
		AccessTokenExpiresAt: state.ExpiresAtUnix,
		SkypeToken:           skResult.Token,
		SkypeTokenExpiresAt:  skResult.ExpiresAt,
		TeamsUserID:          teamsUserID,
		RegionChatServiceURL: skResult.ChatServiceURL,
		RegionAmsURL:         skResult.AmsURL,
		LoginMethod:          LoginMethodDeviceCode,
		ClientID:             client.ClientID,
		RefreshScope:         strings.Join(client.Scopes, " "),
		AuthorizeEndpoint:    client.AuthorizeEndpoint,
		TokenEndpoint:        client.TokenEndpoint,
		SkypeTokenEndpoint:   client.SkypeTokenEndpoint,
		RedirectURI:          client.RedirectURI,
	}

	// Graph token is best-effort: the tenant may not grant these scopes to
	// the client, and the bridge degrades gracefully without it.
	graphClient := *client
	graphClient.Scopes = strings.Fields(main.deviceCodeGraphScope())
	if graphState, graphErr := graphClient.RefreshAccessToken(ctx, refreshToken); graphErr == nil && graphState != nil {
		if token := strings.TrimSpace(graphState.GraphAccessToken); token != "" {
			meta.GraphAccessToken = token
			meta.GraphExpiresAt = graphState.GraphExpiresAt
		}
		if rt := strings.TrimSpace(graphState.RefreshToken); rt != "" {
			meta.RefreshToken = rt
		}
	} else if graphErr != nil && client.Log != nil {
		client.Log.Warn().Err(graphErr).Msg("Graph token not available for device code login; avatars, presence and uploads will be unavailable")
	}
	return meta, nil
}

// deviceCodeGraphScope returns the Graph scope for device code logins,
// tolerating a nil connector (unit tests).
func (t *TeamsConnector) deviceCodeGraphScope() string {
	if t == nil {
		return DefaultDeviceCodeGraphScope
	}
	return t.Config.ResolvedDeviceCodeGraphScope()
}
