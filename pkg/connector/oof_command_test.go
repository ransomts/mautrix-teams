package connector

import (
	"testing"
	"time"
)

func TestParseOOFArgs(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	req, err := parseOOFArgs([]string{"on", "Away", "until", "Monday"}, now)
	if err != nil || req.status != "alwaysEnabled" || req.message != "Away until Monday" {
		t.Errorf("on: %+v %v", req, err)
	}
	req, err = parseOOFArgs([]string{"on", "until", "2026-10-20T08:00:00-04:00", "At", "a", "conference"}, now)
	if err != nil || req.status != "scheduled" || !req.start.Equal(now) || req.message != "At a conference" {
		t.Errorf("on until: %+v %v", req, err)
	}
	req, err = parseOOFArgs([]string{"from", "2026-12-20T00:00:00-05:00", "until", "2027-01-05T00:00:00-05:00", "Winter", "break"}, now)
	if err != nil || req.status != "scheduled" || req.message != "Winter break" {
		t.Errorf("from: %+v %v", req, err)
	}
	if req, err := parseOOFArgs(nil, now); req != nil || err != nil {
		t.Errorf("show: %+v %v", req, err)
	}
	for _, bad := range [][]string{{"on"}, {"from", "x", "until", "y", "m"}, {"sideways"},
		{"from", "2026-12-20T00:00:00Z", "until", "2026-12-19T00:00:00Z", "m"}} {
		if _, err := parseOOFArgs(bad, now); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
	setting := oofSetting(&oofRequest{status: "scheduled", start: now, end: now.Add(48 * time.Hour), message: "Out"},
		map[string]any{"externalAudience": "contactsOnly", "status": "disabled"})
	if setting["status"] != "scheduled" || setting["externalAudience"] != "contactsOnly" ||
		setting["internalReplyMessage"] != "Out" || setting["scheduledEndDateTime"] != "2026-10-16T12:00:00Z" {
		t.Errorf("setting: %v", setting)
	}
}
