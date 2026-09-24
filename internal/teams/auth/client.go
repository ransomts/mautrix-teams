package auth

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/net/http2"
)

// DefaultHTTPTimeout bounds ordinary API requests (token exchange, message
// listing, uploads). Long-poll requests must not use it; see
// client.LongPoll, which derives a client without a global timeout.
const DefaultHTTPTimeout = 60 * time.Second

// HTTP/2 health check. Teams negotiates HTTP/2, so every request to it is
// multiplexed over one pooled connection. When the host's route changes
// (dock unplugged, VPN up/down, Wi-Fi roam) that connection is silently
// blackholed: writes land in the kernel buffer, nothing ever comes back, and
// Go keeps reusing it until the kernel abandons retransmission (~15 min on
// Linux). Meanwhile every send and poll hangs to DefaultHTTPTimeout. Pinging
// once no frame has been read for ReadIdleTimeout drops a dead connection
// within ReadIdleTimeout+PingTimeout; the next request dials fresh.
const (
	http2ReadIdleTimeout = 30 * time.Second
	http2PingTimeout     = 15 * time.Second
)

const (
	defaultAuthorizeEndpoint  = "https://login.live.com/oauth20_authorize.srf"
	defaultTokenEndpoint      = "https://login.microsoftonline.com/consumers/oauth2/v2.0/token"
	defaultSkypeTokenEndpoint = "https://teams.live.com/api/auth/v1.0/authz/consumer"
	defaultClientID           = "4b3e8f46-56d3-427f-b1e2-d239b2ea6bca"
	defaultRedirectURI        = "https://teams.live.com/v2"
)

var defaultScopes = []string{
	"openid",
	"profile",
	"offline_access",
	"https://graph.microsoft.com/Files.ReadWrite",
	"https://graph.microsoft.com/Team.ReadBasic.All",
	"https://graph.microsoft.com/Channel.ReadBasic.All",
}

type Client struct {
	HTTP               *http.Client
	CookieStore        *CookieStore
	AuthorizeEndpoint  string
	TokenEndpoint      string
	SkypeTokenEndpoint string
	ClientID           string
	RedirectURI        string
	Scopes             []string
	Log                *zerolog.Logger
}

func NewClient(store *CookieStore) *Client {
	transport, _ := newTransport()

	var jar http.CookieJar
	if store != nil {
		jar = store.Jar
	}

	httpClient := &http.Client{
		Jar:       jar,
		Transport: &trackingTransport{base: transport, store: store},
		Timeout:   DefaultHTTPTimeout,
	}
	logger := zerolog.Nop()

	return &Client{
		HTTP:               httpClient,
		CookieStore:        store,
		AuthorizeEndpoint:  defaultAuthorizeEndpoint,
		TokenEndpoint:      defaultTokenEndpoint,
		SkypeTokenEndpoint: defaultSkypeTokenEndpoint,
		ClientID:           defaultClientID,
		RedirectURI:        defaultRedirectURI,
		Scopes:             append([]string(nil), defaultScopes...),
		Log:                &logger,
	}
}

// newTransport builds the transport shared by every Teams request: a copy of
// the default transport (proxy from the environment, dial/TLS timeouts,
// idle-connection limits) with HTTP/2 forced on and the health check above.
// The *http2.Transport is returned so the settings can be inspected; it is
// nil if HTTP/2 could not be configured, in which case the transport still
// works over HTTP/1.1.
func newTransport() (*http.Transport, *http2.Transport) {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{}
	}
	transport.ForceAttemptHTTP2 = true
	transport.DisableCompression = true

	h2, err := http2.ConfigureTransports(transport)
	if err != nil {
		return transport, nil
	}
	h2.ReadIdleTimeout = http2ReadIdleTimeout
	h2.PingTimeout = http2PingTimeout
	return transport, h2
}

func (c *Client) AttachSkypeToken(req *http.Request, token string) {
	if req == nil || token == "" {
		return
	}
	req.Header.Set("authentication", "skypetoken="+token)
}

func (c *Client) AuthorizeURL(codeChallenge, state string) (string, error) {
	authURL, err := url.Parse(c.AuthorizeEndpoint)
	if err != nil {
		return "", err
	}
	query := authURL.Query()
	query.Set("client_id", c.ClientID)
	query.Set("redirect_uri", c.RedirectURI)
	query.Set("response_type", "code")
	query.Set("response_mode", "fragment")
	query.Set("scope", strings.Join(c.Scopes, " "))
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	if state != "" {
		query.Set("state", state)
	}
	authURL.RawQuery = query.Encode()
	return authURL.String(), nil
}

type trackingTransport struct {
	base  http.RoundTripper
	store *CookieStore
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.store != nil {
		t.store.TrackRequest(req)
	}
	resp, err := t.base.RoundTrip(req)
	countRequest(req, resp)
	return resp, err
}
