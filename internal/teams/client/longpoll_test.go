package client

import "testing"

func TestExtractThreadIDFromResource(t *testing.T) {
	tests := []struct {
		name     string
		resource string
		want     string
	}{
		{
			name:     "full message path",
			resource: "/v1/users/ME/conversations/19:abc@thread.v2/messages/1234",
			want:     "19:abc@thread.v2",
		},
		{
			name:     "conversation only",
			resource: "/v1/users/ME/conversations/19:xyz@thread.v2",
			want:     "19:xyz@thread.v2",
		},
		{
			name:     "no conversations segment",
			resource: "/v1/users/ME/endpoints/abc",
			want:     "",
		},
		{
			name:     "empty string",
			resource: "",
			want:     "",
		},
		{
			name:     "properties path",
			resource: "/v1/users/ME/conversations/19:chat@thread.v2/properties",
			want:     "19:chat@thread.v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractThreadIDFromResource(tt.resource)
			if got != tt.want {
				t.Errorf("ExtractThreadIDFromResource(%q) = %q, want %q", tt.resource, got, tt.want)
			}
		})
	}
}
