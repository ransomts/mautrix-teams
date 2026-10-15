package connector

// The oof bridge bot command: your Teams automatic replies (out of office).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"

	"go.mau.fi/mautrix-teams/internal/teams/middletier"
)

const oofUsage = "[on [until <time>] <message> | from <time> until <time> <message> | off]  (times RFC 3339)"

var cmdOOF = &commands.FullHandler{
	Func: fnOOF,
	Name: "oof",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Show or set your Teams automatic replies (out of office).",
		Args:        oofUsage,
	},
	RequiresLogin: true,
}

// oofRequest is what the oof command's arguments ask for.
type oofRequest struct {
	status     string // "alwaysEnabled", "scheduled" or "disabled"
	start, end time.Time
	message    string
}

// parseOOFArgs reads "on [until T] MSG", "from T until T MSG" and "off".
func parseOOFArgs(args []string, now time.Time) (*oofRequest, error) {
	if len(args) == 0 {
		return nil, nil
	}
	parseTime := func(arg string) (time.Time, error) {
		t, err := time.Parse(time.RFC3339, arg)
		if err != nil {
			return time.Time{}, fmt.Errorf("not an RFC 3339 time: %s", arg)
		}
		return t, nil
	}
	switch strings.ToLower(args[0]) {
	case "off":
		return &oofRequest{status: "disabled"}, nil
	case "on":
		rest := args[1:]
		req := &oofRequest{status: "alwaysEnabled"}
		if len(rest) >= 2 && strings.EqualFold(rest[0], "until") {
			end, err := parseTime(rest[1])
			if err != nil {
				return nil, err
			}
			req.status, req.start, req.end, rest = "scheduled", now, end, rest[2:]
		}
		req.message = strings.Join(rest, " ")
		if req.message == "" {
			return nil, fmt.Errorf("an automatic reply needs a message")
		}
		return req, nil
	case "from":
		if len(args) < 5 || !strings.EqualFold(args[2], "until") {
			return nil, fmt.Errorf("usage: from <time> until <time> <message>")
		}
		start, err := parseTime(args[1])
		if err != nil {
			return nil, err
		}
		end, err := parseTime(args[3])
		if err != nil {
			return nil, err
		}
		if !end.After(start) {
			return nil, fmt.Errorf("the end is not after the start")
		}
		return &oofRequest{status: "scheduled", start: start, end: end, message: strings.Join(args[4:], " ")}, nil
	default:
		return nil, fmt.Errorf("usage: oof %s", oofUsage)
	}
}

// oofSetting is req applied to the current setting: the reply goes to the
// same people outside the organisation as before.
func oofSetting(req *oofRequest, current map[string]any) map[string]any {
	setting := map[string]any{}
	for k, v := range current {
		setting[k] = v
	}
	setting["status"] = req.status
	if req.status != "disabled" {
		setting["internalReplyMessage"] = req.message
		setting["externalReplyMessage"] = req.message
	}
	if req.status == "scheduled" {
		setting["scheduledStartDateTime"] = req.start.Format(time.RFC3339)
		setting["scheduledEndDateTime"] = req.end.Format(time.RFC3339)
	}
	if _, ok := setting["externalAudience"]; !ok {
		setting["externalAudience"] = "all"
	}
	return setting
}

func fnOOF(ce *commands.Event) {
	req, err := parseOOFArgs(ce.Args, time.Now())
	if err != nil {
		ce.Reply("%s", err)
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	mt, err := client.middleTierClient(ce.Ctx)
	if err != nil {
		ce.Reply("%s", err)
		return
	}
	current, err := mt.GetAutomaticReplies(ce.Ctx)
	if err != nil {
		ce.Reply("Reading your automatic replies failed: %s", err)
		return
	}
	if req == nil {
		pretty, _ := json.MarshalIndent(current, "", "  ")
		ce.Reply("Your Teams automatic replies:\n```json\n%s\n```", pretty)
		return
	}
	if err := mt.UpdateAutomaticReplies(ce.Ctx, oofSetting(req, current)); err != nil {
		ce.Reply("Setting your automatic replies failed: %s", err)
		return
	}
	switch req.status {
	case "disabled":
		ce.Reply("Automatic replies turned off.")
	case "alwaysEnabled":
		ce.Reply("Automatic replies on until turned off.")
	default:
		ce.Reply("Automatic replies on from %s until %s.", req.start.Format("Mon Jan 2 15:04"), req.end.Format("Mon Jan 2 15:04"))
	}
}

// middleTierClient is a middle tier client with fresh tokens.
func (c *TeamsClient) middleTierClient(ctx context.Context) (*middletier.Client, error) {
	c.tokenMu.Lock()
	base := ""
	if c.Meta != nil {
		base = strings.TrimSpace(c.Meta.RegionMiddleTierURL)
	}
	c.tokenMu.Unlock()
	if base == "" {
		return nil, fmt.Errorf("the Teams middle tier URL is not known yet: it comes with the next login refresh")
	}
	token, _, err := c.resourceToken(ctx, middletier.Scope)
	if err != nil {
		return nil, fmt.Errorf("getting a middle tier token failed: %w", err)
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil, fmt.Errorf("no HTTP client")
	}
	return &middletier.Client{HTTP: httpClient, BaseURL: base, Token: token, SkypeToken: c.skypeToken()}, nil
}
