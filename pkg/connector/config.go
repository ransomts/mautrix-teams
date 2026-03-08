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
	// SyncMode controls how the bridge receives Teams events.
	// "poll" (default): short-polling with adaptive backoff.
	// "longpoll": long-polling for lower latency.
	SyncMode string `yaml:"sync_mode"`
	// LogLevel controls the verbosity of bridge-specific logging.
	// Valid values: "trace", "debug", "info", "warn", "error".
	// Default: "info".
	LogLevel string `yaml:"log_level"`
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
	helper.Copy(up.Str, "sync_mode")
	helper.Copy(up.Str, "log_level")
}

func (t *TeamsConnector) GetConfig() (string, any, up.Upgrader) {
	return ExampleConfig, &t.Config, up.SimpleUpgrader(upgradeConfig)
}
