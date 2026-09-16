package connector

import (
	_ "embed"
	"strings"

	"github.com/rs/zerolog"

	up "go.mau.fi/util/configupgrade"
)

//go:embed example-config.yaml
var ExampleConfig string

type TeamsConfig struct {
	// OAuth client ID used by the Teams web app. This must match the ID used in MSAL localStorage keys.
	// If unset, the connector uses the default client ID from internal/teams/auth.
	ClientID string `yaml:"client_id"`
	// OAuth authorize endpoint. For enterprise tenants, set to
	// https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/authorize
	AuthorizeEndpoint string `yaml:"authorize_endpoint"`
	// OAuth token endpoint. For enterprise tenants, set to
	// https://login.microsoftonline.com/<tenant-id>/oauth2/v2.0/token
	TokenEndpoint string `yaml:"token_endpoint"`
	// Skype token endpoint. For enterprise tenants, set to
	// https://teams.microsoft.com/api/authsvc/v1.0/authz
	SkypeTokenEndpoint string `yaml:"skype_token_endpoint"`
	// OAuth redirect URI. For enterprise tenants, may need to be set to
	// https://teams.microsoft.com/go
	RedirectURI string `yaml:"redirect_uri"`
	// DeviceCodeClientID is the OAuth public client used by the device code
	// login flow. It must be a public client that the tenant allows to use the
	// device authorization grant. Default: the Microsoft Teams desktop client.
	DeviceCodeClientID string `yaml:"device_code_client_id"`
	// DeviceCodeScope is the scope requested by the device code login and used
	// on every refresh for such logins. The resulting access token must be one
	// the skypetoken endpoint accepts.
	DeviceCodeScope string `yaml:"device_code_scope"`
	// DeviceCodeGraphScope is the scope used to obtain a Microsoft Graph token
	// with the device-code refresh token (avatars, presence, file uploads).
	DeviceCodeGraphScope string `yaml:"device_code_graph_scope"`
	// SyncMode controls how the bridge receives Teams events.
	// "poll" (default): short-polling with adaptive backoff.
	// "longpoll": long-polling for lower latency.
	SyncMode string `yaml:"sync_mode"`
	// LogLevel controls the verbosity of bridge-specific logging.
	// Valid values: "trace", "debug", "info", "warn", "error".
	// Default: "info".
	LogLevel string `yaml:"log_level"`
}

const (
	// DefaultDeviceCodeClientID is the Microsoft Teams desktop client, a
	// first-party public client that is allowed to use the device
	// authorization grant and is pre-consented for the Teams and Graph scopes
	// the bridge needs.
	DefaultDeviceCodeClientID = "1fec8e78-bce4-4aaf-ab1b-5451cc387264"
	// DefaultDeviceCodeScope yields an access token for the Teams
	// authorization service (api.spaces.skype.com), which the enterprise
	// skypetoken endpoint exchanges for a skypetoken.
	DefaultDeviceCodeScope = "https://api.spaces.skype.com/Authorization.ReadWrite offline_access openid profile"
	// DefaultDeviceCodeGraphScope is requested on refresh to obtain a Graph token.
	DefaultDeviceCodeGraphScope = "https://graph.microsoft.com/Files.ReadWrite https://graph.microsoft.com/Team.ReadBasic.All https://graph.microsoft.com/Channel.ReadBasic.All https://graph.microsoft.com/Presence.Read.All offline_access"
)

// ResolvedDeviceCodeClientID returns the configured device code client ID or the default.
func (c *TeamsConfig) ResolvedDeviceCodeClientID() string {
	if c != nil {
		if id := strings.TrimSpace(c.DeviceCodeClientID); id != "" {
			return id
		}
	}
	return DefaultDeviceCodeClientID
}

// ResolvedDeviceCodeScope returns the configured device code scope or the default.
func (c *TeamsConfig) ResolvedDeviceCodeScope() string {
	if c != nil {
		if scope := strings.TrimSpace(c.DeviceCodeScope); scope != "" {
			return scope
		}
	}
	return DefaultDeviceCodeScope
}

// ResolvedDeviceCodeGraphScope returns the configured Graph scope for device code logins or the default.
func (c *TeamsConfig) ResolvedDeviceCodeGraphScope() string {
	if c != nil {
		if scope := strings.TrimSpace(c.DeviceCodeGraphScope); scope != "" {
			return scope
		}
	}
	return DefaultDeviceCodeGraphScope
}

// ParsedLogLevel returns the zerolog.Level for the configured LogLevel string.
func (c *TeamsConfig) ParsedLogLevel() zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(c.LogLevel)) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error":
		return zerolog.ErrorLevel
	case "info", "":
		return zerolog.InfoLevel
	default:
		return zerolog.InfoLevel
	}
}

func upgradeConfig(helper up.Helper) {
	helper.Copy(up.Str, "client_id")
	helper.Copy(up.Str, "authorize_endpoint")
	helper.Copy(up.Str, "token_endpoint")
	helper.Copy(up.Str, "skype_token_endpoint")
	helper.Copy(up.Str, "redirect_uri")
	helper.Copy(up.Str, "device_code_client_id")
	helper.Copy(up.Str, "device_code_scope")
	helper.Copy(up.Str, "device_code_graph_scope")
	helper.Copy(up.Str, "sync_mode")
	helper.Copy(up.Str, "log_level")
}

func (t *TeamsConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &t.Config, up.SimpleUpgrader(upgradeConfig)
}
