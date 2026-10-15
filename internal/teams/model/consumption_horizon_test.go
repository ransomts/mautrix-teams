package model

import "testing"

func TestParseConsumptionHorizonLatestReadTS(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		wantTS int64
		wantOK bool
	}{
		{
			name:   "valid three parts",
			input:  "1;1769620117227;2621949452385992439",
			wantTS: 1769620117227,
			wantOK: true,
		},
		{
			name:   "missing second segment",
			input:  "1;",
			wantOK: false,
		},
		{
			name:   "single part",
			input:  "1",
			wantOK: false,
		},
		{
			name:   "non-numeric second segment",
			input:  "1;not-a-number;2",
			wantOK: false,
		},
		{
			name:   "whitespace second segment",
			input:  "1;   ;2",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTS, gotOK := ParseConsumptionHorizonLatestReadTS(tt.input)
			if gotOK != tt.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotOK && gotTS != tt.wantTS {
				t.Fatalf("ts = %d, want %d", gotTS, tt.wantTS)
			}
		})
	}
}

func TestParseConsumptionHorizonMessageID(t *testing.T) {
	for input, want := range map[string]string{
		"1791891471042;1791892337814;3958124570138072226": "1791891471042",
		"0;1769620117227;2": "",
		";1769620117227;2":  "",
		"abc;1;2":           "",
		"":                  "",
	} {
		got, ok := ParseConsumptionHorizonMessageID(input)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q, %v; want %q", input, got, ok, want)
		}
	}
}
