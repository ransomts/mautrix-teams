package connector

// Bridge bot commands that set one's own Teams status and status note, and
// publishing one's own Teams presence to Matrix account data, so a client
// can tell when one is on Do Not Disturb.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2/commands"

	"go.mau.fi/mautrix-teams/internal/teams/presence"
)

// ownPresenceAccountDataType is the account data event carrying one's own
// Teams presence, set through the double puppet whenever it changes.
const ownPresenceAccountDataType = "fi.mau.teams.presence"

var helpSectionStatus = commands.HelpSection{Name: "Teams status", Order: 25}

// statusWords maps the status command's words to Teams availabilities.
var statusWords = map[string]string{
	"available": "Available",
	"busy":      "Busy",
	"dnd":       "DoNotDisturb",
	"brb":       "BeRightBack",
	"away":      "Away",
	"offline":   "Offline",
	"reset":     "",
}

const (
	statusUsage = "<available|busy|dnd|brb|away|offline|reset> [<duration, e.g. 1h> | <RFC 3339 time>]"
	noteUsage   = "[until <duration | RFC 3339 time>] <text> | clear"
)

var cmdStatus = &commands.FullHandler{
	Func: fnStatus,
	Name: "status",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Set your Teams status, until a time or for a while (reset: back to automatic).",
		Args:        statusUsage,
	},
	RequiresLogin: true,
}

const locationUsage = "<office|remote|clear> [<duration, e.g. 4h> | <RFC 3339 time>]"

var locationWords = map[string]int{
	"office": presence.LocationOffice,
	"remote": presence.LocationRemote,
	"clear":  presence.LocationUnknown,
}

var cmdLocation = &commands.FullHandler{
	Func: fnLocation,
	Name: "location",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Set your Teams work location (until the end of the day, unless a time is given).",
		Args:        locationUsage,
	},
	RequiresLogin: true,
}

func fnLocation(ce *commands.Event) {
	if len(ce.Args) < 1 || len(ce.Args) > 2 {
		ce.Reply("Usage: `$cmdprefix location %s`", locationUsage)
		return
	}
	location, ok := locationWords[strings.ToLower(ce.Args[0])]
	if !ok {
		ce.Reply("Unknown location %q.  Usage: `$cmdprefix location %s`", ce.Args[0], locationUsage)
		return
	}
	var arg string
	if len(ce.Args) == 2 {
		arg = ce.Args[1]
	}
	expiry, err := parseExpiry(arg, time.Now())
	if err != nil {
		ce.Reply("%s", err)
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	pc, err := client.presenceClient(ce.Ctx)
	if err == nil {
		err = pc.SetWorkLocation(ce.Ctx, location, expiry)
	}
	if err != nil {
		ce.Reply("Setting your Teams work location failed: %s", err)
		return
	}
	go client.pollPresence(context.WithoutCancel(ce.Ctx))
	if location == presence.LocationUnknown {
		ce.Reply("Teams work location cleared.")
	} else {
		ce.Reply("Teams work location set to %s%s.", ce.Args[0], expiryPhrase(arg, expiry))
	}
}

var cmdNote = &commands.FullHandler{
	Func: fnNote,
	Name: "note",
	Help: commands.HelpMeta{
		Section:     helpSectionStatus,
		Description: "Set your Teams status note, shown to people when they message you (clear: remove it).",
		Args:        noteUsage,
	},
	RequiresLogin: true,
}

// registerStatusCommands adds the status commands to the bridge's processor.
func registerStatusCommands(proc any) {
	if p, ok := proc.(*commands.Processor); ok {
		p.AddHandlers(cmdStatus, cmdNote, cmdLocation, cmdChat, cmdOOF, cmdFiles, cmdForward, cmdSave, cmdUnsave)
	}
}

// errorf is fmt.Errorf, for command errors shown to the user as they are.
func errorf(format string, args ...any) error { return fmt.Errorf(format, args...) }

// parseExpiry reads "1h"-style durations and RFC 3339 times; "" is no expiry.
func parseExpiry(arg string, now time.Time) (time.Time, error) {
	if arg == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(arg); err == nil {
		if d <= 0 {
			return time.Time{}, fmt.Errorf("duration must be positive: %s", arg)
		}
		return now.Add(d), nil
	}
	if t, err := time.Parse(time.RFC3339, arg); err == nil {
		if !t.After(now) {
			return time.Time{}, fmt.Errorf("time is in the past: %s", arg)
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("not a duration (1h, 30m) or an RFC 3339 time: %s", arg)
}

// expiryPhrase says how long a status or note lasts, from the command's
// argument: "for 1h", or "until Wed 22:31" in the time's own UTC offset
// (the bridge's clock need not be in the user's time zone); "" for none.
func expiryPhrase(arg string, expiry time.Time) string {
	if expiry.IsZero() {
		return ""
	}
	if _, err := time.ParseDuration(arg); err == nil {
		return " for " + arg
	}
	return " until " + expiry.Format("Mon 15:04")
}

// commandClient is the Teams client of the command's user, or nil after
// replying why there is none.
func commandClient(ce *commands.Event) *TeamsClient {
	login := ce.User.GetDefaultLogin()
	if login == nil {
		ce.Reply("You are not logged in to Teams.")
		return nil
	}
	client, ok := login.Client.(*TeamsClient)
	if !ok || client == nil {
		ce.Reply("Your Teams login is not connected.")
		return nil
	}
	return client
}

func fnStatus(ce *commands.Event) {
	if len(ce.Args) < 1 || len(ce.Args) > 2 {
		ce.Reply("Usage: `$cmdprefix status %s`", statusUsage)
		return
	}
	availability, ok := statusWords[strings.ToLower(ce.Args[0])]
	if !ok {
		ce.Reply("Unknown status %q.  Usage: `$cmdprefix status %s`", ce.Args[0], statusUsage)
		return
	}
	var arg string
	if len(ce.Args) == 2 {
		arg = ce.Args[1]
	}
	expiry, err := parseExpiry(arg, time.Now())
	if err != nil {
		ce.Reply("%s", err)
		return
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	pc, err := client.presenceClient(ce.Ctx)
	if err == nil {
		err = pc.SetAvailability(ce.Ctx, availability, expiry)
	}
	if err != nil {
		ce.Reply("Setting your Teams status failed: %s", err)
		return
	}
	go client.pollPresence(context.WithoutCancel(ce.Ctx))
	if availability == "" {
		ce.Reply("Teams status reset to automatic.")
	} else {
		ce.Reply("Teams status set to %s%s.", availability, expiryPhrase(arg, expiry))
	}
}

func fnNote(ce *commands.Event) {
	args := ce.Args
	if len(args) == 0 {
		ce.Reply("Usage: `$cmdprefix note %s`", noteUsage)
		return
	}
	var expiry time.Time
	var untilArg string
	if strings.EqualFold(args[0], "until") {
		if len(args) < 3 {
			ce.Reply("Usage: `$cmdprefix note %s`", noteUsage)
			return
		}
		var err error
		untilArg = args[1]
		if expiry, err = parseExpiry(untilArg, time.Now()); err != nil {
			ce.Reply("%s", err)
			return
		}
		args = args[2:]
	}
	message := strings.Join(args, " ")
	if len(args) == 1 && strings.EqualFold(args[0], "clear") {
		message = ""
	}
	client := commandClient(ce)
	if client == nil {
		return
	}
	pc, err := client.presenceClient(ce.Ctx)
	if err == nil {
		err = pc.PublishNote(ce.Ctx, message, expiry, true)
	}
	if err != nil {
		ce.Reply("Setting your Teams status note failed: %s", err)
		return
	}
	go client.pollPresence(context.WithoutCancel(ce.Ctx))
	if message == "" {
		ce.Reply("Teams status note cleared.")
	} else {
		ce.Reply("Teams status note set%s.", expiryPhrase(untilArg, expiry))
	}
}

// presenceClient is a client for the Teams presence service with a fresh token.
func (c *TeamsClient) presenceClient(ctx context.Context) (*presence.Client, error) {
	token, _, err := c.ensurePresenceToken(ctx)
	if err != nil {
		return nil, err
	}
	httpClient := c.getConsumerHTTP()
	if httpClient == nil {
		return nil, fmt.Errorf("no HTTP client")
	}
	return &presence.Client{HTTP: httpClient, Token: token}, nil
}

// publishOwnPresence puts one's own Teams presence in one's Matrix account
// data (ownPresenceAccountDataType), through the double puppet.
func (c *TeamsClient) publishOwnPresence(ctx context.Context, p presence.Presence) error {
	now := time.Now()
	_, status := mapTeamsPresence(p.Availability, p.Activity)
	content := map[string]any{
		"availability":  p.Availability,
		"activity":      p.Activity,
		"status":        status,
		"note":          p.Note.Text(now),
		"out_of_office": p.CalendarData.IsOutOfOffice,
		"location":      p.WorkLocation.Words(now),
		"updated_ts":    now.UnixMilli(),
	}
	return c.publishAccountData(ctx, ownPresenceAccountDataType, content)
}
