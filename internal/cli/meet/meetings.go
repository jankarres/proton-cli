package meet

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/roman-16/proton-cli/internal/cli/kit"
	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/ical"
	meetsvc "github.com/roman-16/proton-cli/internal/service/meet"
	"github.com/roman-16/proton-cli/internal/ui"
	"github.com/roman-16/proton-cli/internal/units"
)

// linkWarning is said wherever a meeting's link is shown, since the link is the
// whole of what lets somebody in.
const linkWarning = "Anyone with this link can join the meeting until it is deleted."

// defaultLength is how long a meeting lasts when the command line names only its
// start, DEFAULT_MEETING_DURATION in the web app's ScheduleMeetingForm.
const defaultLength = 30 * time.Minute

func meetingsCmd() *cobra.Command {
	c := &cobra.Command{Use: "meetings", Short: "Meetings people join by a link"}
	c.AddCommand(meetingsCreateCmd(), meetingsDeleteCmd(), meetingsGetCmd(), meetingsListCmd(), meetingsUpdateCmd())
	return c
}

// meetingRow is a meeting as a listing shows it: everything but its link. The
// link carries the password that lets anybody in, so it appears only where one
// meeting is asked for, never in a listing.
type meetingRow struct {
	ID              string              `json:"id"`
	Title           string              `json:"title"`
	Host            string              `json:"host,omitempty"`
	Type            meetsvc.MeetingType `json:"type"`
	Start           *time.Time          `json:"start,omitempty"`
	End             *time.Time          `json:"end,omitempty"`
	Timezone        string              `json:"timezone,omitempty"`
	RRule           string              `json:"rrule,omitempty"`
	CalendarID      string              `json:"calendar_id,omitempty"`
	CalendarEventID string              `json:"calendar_event_id,omitempty"`
}

func rowOf(m meetsvc.Meeting) meetingRow {
	return meetingRow{
		ID: m.Name, Title: m.Title, Host: m.Host, Type: m.Type, Start: m.Start, End: m.End,
		Timezone: m.Timezone, RRule: m.RRule, CalendarID: m.CalendarID, CalendarEventID: m.CalendarEventID,
	}
}

// isMeeting says whether a meeting belongs in this collection rather than among
// the rooms: one that happens now, once at a time, or at times that repeat.
func isMeeting(t meetsvc.MeetingType) bool {
	return t == meetsvc.Instant || t == meetsvc.Scheduled || t == meetsvc.Recurring
}

func meetingColumns() []ui.Column[meetingRow] {
	return []ui.Column[meetingRow]{
		{Header: "ID", ID: true, Cell: func(r meetingRow) string { return r.ID }},
		{Header: "TITLE", Flex: true, Cell: func(r meetingRow) string { return r.Title }},
		{Header: "START", Cell: func(r meetingRow) string { return clock(r.Start) }},
		{Header: "TYPE", Cell: func(r meetingRow) string { return r.Type.String() }},
	}
}

// clock is a meeting's time on the screen, in the zone this invocation works in.
// A meeting with no time, an instant one, shows a dash.
func clock(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return units.Time(t.Unix())
}

func meetingFields(m *meetsvc.Meeting) []ui.Field {
	fields := []ui.Field{
		{Label: "Link", Value: m.URL},
		{Label: "ID", Value: m.Name, ID: true},
		{Label: "Title", Value: m.Title},
		{Label: "Type", Value: m.Type.String()},
	}
	if m.Start != nil {
		fields = append(fields, ui.Field{Label: "Start", Value: clock(m.Start)})
	}
	if m.End != nil {
		fields = append(fields, ui.Field{Label: "End", Value: clock(m.End)})
	}
	fields = append(fields,
		ui.Field{Label: "Repeats", Value: m.RRule},
		ui.Field{Label: "Zone", Value: m.Timezone},
		ui.Field{Label: "Host", Value: m.Host},
	)
	if m.CalendarEventID != "" {
		fields = append(fields, ui.Field{
			Label: "Event", Value: kit.JoinPair(m.CalendarID, m.CalendarEventID), Ref: "calendar events",
		})
	}
	return fields
}

// judgeRef refuses a reference no meeting or room could answer to, before
// anything is sent.
func judgeRef(c *kit.Invocation) error {
	return meetsvc.CheckRef(c.Args[0])
}

// judgeMeetingRef is judgeRef for the meetings collection, which also refuses
// personal: that names a room.
func judgeMeetingRef(c *kit.Invocation) error {
	if c.Args[0] == personal {
		return kit.Fail("Your personal room is a room, not a meeting.").
			Hint(roomsCollection.command(c.Cmd.Name(), personal))
	}
	return judgeRef(c)
}

func meetingsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your upcoming meetings",
		Long: "List your upcoming meetings, soonest first.\n\n" +
			"Instant meetings are not listed, because Proton leaves them out of the\n" +
			"list. `meetings get` still finds one by its ID or link. The links are not\n" +
			"shown: `meetings get` shows one.",
		RunE: kit.Run(nil, func(c *kit.Invocation) error {
			all, err := c.App.Meet.Upcoming(c.Ctx)
			if err != nil {
				return err
			}
			return listMeetings(c, all)
		}),
	}
}

// listMeetings answers with the meetings among everything upcoming, rooms left
// to their own collection, soonest first.
func listMeetings(c *kit.Invocation, all []meetsvc.Meeting) error {
	rows := make([]meetingRow, 0, len(all))
	for _, m := range all {
		if isMeeting(m.Type) {
			rows = append(rows, rowOf(m))
		}
	}
	slices.SortStableFunc(rows, func(a, b meetingRow) int {
		switch {
		case a.Start == nil && b.Start == nil:
			return 0
		case a.Start == nil:
			return 1
		case b.Start == nil:
			return -1
		}
		return a.Start.Compare(*b.Start)
	})
	return kit.List(c, ui.TableSpec[meetingRow]{
		Noun: "meetings", Columns: meetingColumns(), Total: ui.Unknown, Page: ui.Unpaged,
	}, rows)
}

func meetingsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get REF",
		Short: "Show one meeting, link and all",
		Long: "Show one meeting, link and all.\n\n" +
			"REF is the ID in the link after id-, such as 6MCF0BSHBR, or the whole link.\n" +
			"The link appears here and when the meeting is made, never in a listing.",
		RunE: kit.Run([]kit.Step{kit.StepExpand, judgeMeetingRef}, func(c *kit.Invocation) error {
			m, err := resolveIn(c, meetingsCollection)
			if err != nil {
				return err
			}
			c.Warn("%s", linkWarning)
			return kit.Show(c, ui.RecordSpec{Object: m, Fields: meetingFields(m)})
		}),
	}
}

// schedule is what the command line says about a meeting's title and times.
type schedule struct {
	title, start, end, duration, rrule string
}

func (f *schedule) register(c *cobra.Command, verb string) {
	fl := c.Flags()
	fl.StringVar(&f.title, "title", "", verb+" the title")
	fl.StringVar(&f.start, "start", "", verb+" the start: a day and a time (2026-11-02T10:00, or full RFC 3339)")
	fl.StringVar(&f.end, "end", "", verb+" the end: a day and a time")
	fl.StringVar(&f.duration, "duration", "", verb+" how long it lasts (e.g. 15m, 1h, 1h30m)")
	fl.StringVar(&f.rrule, "rrule", "", verb+" the recurrence rule, e.g. FREQ=WEEKLY;COUNT=10")
	kit.Exclusive(c, "end", "duration")
}

// moves says whether the command line changes when a meeting happens.
func (f *schedule) moves(c *kit.Invocation) bool {
	return slices.ContainsFunc([]string{"start", "end", "duration", "rrule"}, c.Changed)
}

// check judges what can be judged from the command line alone: a title that
// says nothing, and times that do not name a time of day. The times are read
// again in the zone the account works in, which takes a request, so here only
// their form is judged.
func (f *schedule) check(creating bool) kit.Step {
	return func(c *kit.Invocation) error {
		if (creating || c.Changed("title")) && strings.TrimSpace(f.title) == "" {
			return kit.Fail("A meeting needs a title.").Hint("--title 'Team sync'")
		}
		if creating && f.start == "" {
			for _, flag := range []string{"end", "duration", "rrule"} {
				if c.Changed(flag) {
					return kit.Fail("--%s needs --start. A meeting without one is instant and has no time.", flag).
						Hint("--start 2026-11-02T10:00")
				}
			}
		}
		for _, flag := range []string{"start", "end"} {
			if !c.Changed(flag) {
				continue
			}
			value := f.start
			if flag == "end" {
				value = f.end
			}
			when, err := ical.ParseTime(value, time.UTC)
			if err != nil {
				return kit.Fail("--%s: %v", flag, err)
			}
			if when.AllDay {
				return kit.Fail("--%s names a day but not a time of day.", flag).
					Hint("--" + flag + " " + when.Time.Format("2006-01-02") + "T10:00")
			}
		}
		if c.Changed("duration") {
			dur, err := units.ParseDuration(f.duration)
			if err != nil {
				return kit.Fail("--duration: %v", err)
			}
			if dur <= 0 {
				return kit.Fail("--duration has to be longer than nothing.").Hint("--duration 30m")
			}
		}
		return nil
	}
}

// times is when a meeting runs once the command line has had its say, read in
// loc. What the command line leaves out comes from the meeting as it is, when
// there is one: its start, and its length. A meeting made with only a start
// lasts defaultLength.
func (f *schedule) times(loc *time.Location, m *meetsvc.Meeting) (start, end time.Time, err error) {
	switch {
	case f.start != "":
		written, err := ical.ParseTime(f.start, loc)
		if err != nil {
			return start, end, kit.Fail("--start: %v", err)
		}
		start = written.Time
	case m != nil && m.Start != nil:
		start = *m.Start
	default:
		return start, end, kit.Fail("The meeting has no start to keep.").Hint("--start 2026-11-02T10:00")
	}
	switch {
	case f.end != "":
		written, err := ical.ParseTime(f.end, loc)
		if err != nil {
			return start, end, kit.Fail("--end: %v", err)
		}
		end = written.Time
		if !end.After(start) {
			return start, end, kit.Fail("--end is not after the start.")
		}
	case f.duration != "":
		dur, err := units.ParseDuration(f.duration)
		if err != nil {
			return start, end, kit.Fail("--duration: %v", err)
		}
		end = start.Add(dur)
	case m != nil && m.Start != nil && m.End != nil && m.End.After(*m.Start):
		end = start.Add(m.End.Sub(*m.Start))
	default:
		end = start.Add(defaultLength)
	}
	return start, end, nil
}

// zoneOf is the zone this invocation writes against, by name for Proton and as
// the frame a time on the command line is read in.
func zoneOf(c *kit.Invocation) (string, *time.Location, error) {
	zone, err := c.App.Zone(c.Ctx)
	if err != nil {
		return "", nil, err
	}
	loc, err := c.App.Location(c.Ctx)
	if err != nil {
		return "", nil, err
	}
	return zone, loc, nil
}

// at is a time as a result names it, with the zone it was read in.
func at(t time.Time, loc *time.Location, zone string) string {
	return t.In(loc).Format("2006-01-02 15:04") + " " + zone
}

func meetingsCreateCmd() *cobra.Command {
	var f schedule
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a meeting and print its link",
		Long: "Create a meeting and print its link.\n\n" +
			"Without --start the meeting is instant: it has no time, and the link works\n" +
			"until you delete it. With --start it is scheduled, and --rrule makes it repeat.\n" +
			"--start takes a day and a time, as 2026-11-02T10:00, read in your zone.\n\n" +
			"Without --end or --duration a scheduled meeting lasts 30 minutes.",
		RunE: kit.Run([]kit.Step{f.check(true)}, func(c *kit.Invocation) error {
			nm := meetsvc.NewMeeting{Title: f.title, Type: meetsvc.Instant}
			detail := ""
			if f.start != "" {
				zone, loc, err := zoneOf(c)
				if err != nil {
					return err
				}
				start, end, err := f.times(loc, nil)
				if err != nil {
					return err
				}
				nm.Start, nm.End, nm.Timezone, nm.RRule = &start, &end, zone, f.rrule
				nm.Type = meetsvc.Scheduled
				if f.rrule != "" {
					nm.Type = meetsvc.Recurring
				}
				detail = "for " + at(start, loc, zone)
			}
			var made *meetsvc.Meeting
			if err := kit.Mutate(c, ui.ResultSpec{
				Action: ui.Created, Kind: "meetings", Count: 1, Name: f.title, Detail: detail, AnswerFollows: true,
			}, func() error {
				var err error
				made, err = c.App.Meet.Create(c.Ctx, nm)
				return err
			}); err != nil {
				return err
			}
			if made == nil || c.App.DryRun {
				return nil
			}
			c.Warn("%s", linkWarning)
			return kit.Show(c, ui.RecordSpec{Object: made, Fields: meetingFields(made)})
		}),
	}
	f.register(c, "Set")
	return c
}

func meetingsUpdateCmd() *cobra.Command {
	var f schedule
	c := &cobra.Command{
		Use:   "update REF",
		Short: "Rename a meeting, or move it to another time",
		Long: "Rename a meeting, or move it to another time.\n\n" +
			"Anything you do not mention is left alone. A meeting you move keeps its\n" +
			"length unless --end or --duration says otherwise, and --rrule '' makes it\n" +
			"happen once. The new times are read in your zone.\n\n" +
			"Only a scheduled or recurring meeting can be moved. One that belongs to a\n" +
			"calendar event moves with the event: change the event instead.",
		RunE: kit.Run([]kit.Step{f.check(false), kit.StepExpand, judgeMeetingRef}, func(c *kit.Invocation) error {
			renaming, moving := c.Changed("title"), f.moves(c)
			if !renaming && !moving {
				return kit.Fail("Nothing to change.").Hint("--title 'Team sync'", "--start 2026-11-02T10:00")
			}
			m, err := resolveIn(c, meetingsCollection)
			if err != nil {
				return err
			}
			var start, end time.Time
			var zone, rrule, detail string
			if moving {
				if err := movable(m); err != nil {
					return err
				}
				var loc *time.Location
				if zone, loc, err = zoneOf(c); err != nil {
					return err
				}
				if start, end, err = f.times(loc, m); err != nil {
					return err
				}
				rrule = m.RRule
				if c.Changed("rrule") {
					rrule = f.rrule
				}
				detail = "to " + at(start, loc, zone)
			}
			name := m.Title
			if renaming {
				name = f.title
			}
			return kit.Mutate(c, ui.ResultSpec{
				Action: ui.Updated, Kind: "meetings", Count: 1, Name: name, IDs: []string{m.Name}, Detail: detail,
			}, func() error {
				ch := change{renaming: renaming, moving: moving, title: f.title,
					start: start, end: end, rrule: rrule, zone: zone}
				return ch.apply(c.Ctx, c.App.Meet, m)
			})
		}),
	}
	f.register(c, "Replace")
	return c
}

// change is what meetings update does to one meeting.
type change struct {
	renaming, moving bool
	title            string
	start, end       time.Time
	rrule, zone      string
}

// changer is the part of the Meet service a change goes through.
type changer interface {
	Rename(ctx context.Context, m *meetsvc.Meeting, title string) error
	Reschedule(ctx context.Context, m *meetsvc.Meeting, start, end time.Time, rrule, timezone string) error
}

// apply moves the meeting before it renames it. Proton refuses a move far more
// often than a new title, and a move refused first leaves the meeting as it was
// rather than renamed and still at its old time.
func (ch change) apply(ctx context.Context, s changer, m *meetsvc.Meeting) error {
	if ch.moving {
		if err := s.Reschedule(ctx, m, ch.start, ch.end, ch.rrule, ch.zone); err != nil {
			return err
		}
	}
	if ch.renaming {
		if err := s.Rename(ctx, m, ch.title); err != nil {
			if ch.moving {
				return fmt.Errorf("the meeting moved, but its new title was refused: %w", err)
			}
			return err
		}
	}
	return nil
}

// movable refuses to move a meeting that has no time of its own to move: an
// instant one and a room, as the web app's saveMeetingSchedule refuses them, and
// one a calendar event carries, which Proton keeps at the event's time.
func movable(m *meetsvc.Meeting) error {
	switch {
	case m.CalendarEventID != "":
		return errs.Naming(m.Title, kit.Fail("\"%s\" belongs to a calendar event and moves with it.", m.Title).
			Hint(kit.Program+" calendar events update "+kit.JoinPair(m.CalendarID, m.CalendarEventID)+
				" --start 2026-11-02T10:00"))
	case m.Type == meetsvc.Instant:
		return errs.Naming(m.Title, kit.Fail("\"%s\" is an instant meeting, which has no time to move.", m.Title).
			Hint(kit.Program+" meet meetings create --title 'Team sync' --start 2026-11-02T10:00"))
	case m.Type != meetsvc.Scheduled && m.Type != meetsvc.Recurring:
		return errs.Naming(m.Title, kit.Fail("\"%s\" is a room, which has no time to move.", m.Title))
	}
	return nil
}

func meetingsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete REF",
		Short: "Delete a meeting",
		Long: "Delete a meeting.\n\n" +
			"Its link stops working for everyone you sent it to. A meeting that belongs to\n" +
			"a calendar event is deleted all the same, and the event keeps a link that no\n" +
			"longer works.",
		RunE: kit.Run([]kit.Step{kit.StepExpand, judgeMeetingRef}, func(c *kit.Invocation) error {
			m, err := resolveIn(c, meetingsCollection)
			if err != nil {
				return err
			}
			if m.CalendarEventID != "" {
				c.Warn("This meeting belongs to a calendar event. The event's Proton Meet link stops working with it.")
			}
			return kit.Mutate(c, ui.ResultSpec{
				Action: ui.Deleted, Kind: "meetings", Count: 1, Name: m.Title, IDs: []string{m.Name},
			}, func() error {
				return c.App.Meet.Delete(c.Ctx, m)
			})
		}),
	}
}
