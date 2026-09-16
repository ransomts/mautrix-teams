package connector

import "testing"

func TestIsLikelyThreadID(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{input: "", want: false},
		{input: "8:live:someone", want: false},
		{input: "19:abc@thread.v2", want: true},
		{input: "19:abc@THREAD.V2", want: true},
		{input: "19:abc@thread.skype", want: true},
		{input: "19:abc@unq.gbl.spaces", want: true},
		// Channels: Teams sends channel system messages from the channel itself.
		{input: "19:0b2f21633c704cdf970e6eacff0b60b5@thread.tacv2", want: true},
		{input: "19:meeting_abc@thread.v2", want: true},
		{input: "8:orgid:c61ce4d2-56e3-47d9-9e26-96fbff37fb62", want: false},
		{input: "28:some-bot", want: false},
	}

	for _, tc := range cases {
		if got := isLikelyThreadID(tc.input); got != tc.want {
			t.Fatalf("isLikelyThreadID(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestIsSystemMessageType(t *testing.T) {
	for input, want := range map[string]bool{
		"ThreadActivity/AddMember":    true,
		"ThreadActivity/TopicUpdate":  true,
		"threadactivity/deletemember": true,
		"Control/Typing":              true,
		"RichText/Html":               false,
		"Text":                        false,
		"Event/Call":                  false,
		"RichText/Media_GenericFile":  false,
		"":                            false,
	} {
		if got := isSystemMessageType(input); got != want {
			t.Errorf("isSystemMessageType(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestIsAMSURL(t *testing.T) {
	region := "https://us-prod.asyncgw.teams.microsoft.com/"
	for input, want := range map[string]bool{
		"https://us-prod.asyncgw.teams.microsoft.com/v1/objects/0-wus-d5-abc/views/imgo": true,
		"https://eu-prod.asyncgw.teams.microsoft.com/v1/objects/0-x/views/imgo":          true,
		"https://us-api.asm.skype.com/v1/objects/0-x/views/imgo":                         true,
		"https://media2.giphy.com/media/x/giphy.gif":                                     false,
		"https://example.com/photo.png":                                                  false,
		"https://teams.microsoft.com/l/message/19:x/1":                                   false,
		"": false,
	} {
		if got := isAMSURL(input, region); got != want {
			t.Errorf("isAMSURL(%q) = %v, want %v", input, got, want)
		}
	}
}
