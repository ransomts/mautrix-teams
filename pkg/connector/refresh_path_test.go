package connector

import (
	"strings"
	"testing"
)

func TestTenantTokenEndpointSelection(t *testing.T) {
	cases := map[string]string{
		"": "",
		"https://login.microsoftonline.com/common/oauth2/v2.0/token":        "",
		"https://login.microsoftonline.com/consumers/oauth2/v2.0/token":     "",
		"https://login.microsoftonline.com/0c9bf8f6-ccad/oauth2/v2.0/token": "https://login.microsoftonline.com/0c9bf8f6-ccad/oauth2/v2.0/token",
	}
	for ep, want := range cases {
		main := &TeamsConnector{Config: TeamsConfig{TokenEndpoint: ep}}
		if got := tenantTokenEndpoint(main); got != want {
			t.Errorf("endpoint %q -> %q, want %q", ep, got, want)
		}
	}
	if tenantTokenEndpoint(nil) != "" {
		t.Errorf("nil connector should yield empty")
	}
}

func TestSkypeSpacesRefreshScopeShape(t *testing.T) {
	if !strings.Contains(skypeSpacesRefreshScope, "api.spaces.skype.com") ||
		!strings.Contains(skypeSpacesRefreshScope, "offline_access") {
		t.Fatalf("unexpected refresh scope: %q", skypeSpacesRefreshScope)
	}
}
