package meet

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/roman-16/proton-cli/internal/app"
	"github.com/roman-16/proton-cli/internal/cli/kit"
	"github.com/roman-16/proton-cli/internal/errs"
	meetsvc "github.com/roman-16/proton-cli/internal/service/meet"
	"github.com/roman-16/proton-cli/internal/ui"
)

const testLink = "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-K0NXa1b2c3d4"

func upcoming() []meetsvc.Meeting {
	later := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	sooner := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	return []meetsvc.Meeting{
		{ID: "p1", Name: "865JZ8JMHG", Title: "Later", URL: testLink, Type: meetsvc.Scheduled, Start: &later},
		{ID: "p2", Name: "AAAAAAAAAA", Title: "Now", URL: testLink, Type: meetsvc.Instant},
		{ID: "p3", Name: "E87W2DAJ60", Title: "My room", URL: testLink, Type: meetsvc.Personal},
		{ID: "p4", Name: "6MCF0BSHBR", Title: "Sooner", URL: testLink, Type: meetsvc.Recurring, Start: &sooner},
		{ID: "p5", Name: "PERMANENT1", Title: "Lobby", URL: testLink, Type: meetsvc.Permanent},
	}
}

// A meeting's link lets anybody in, so a listing never carries it, in any
// format, while get and create do.
func TestAListingNeverCarriesTheLink(t *testing.T) {
	for _, format := range []ui.Format{ui.FormatText, ui.FormatJSON, ui.FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			var out, errb bytes.Buffer
			c := &kit.Invocation{
				Ctx: context.Background(),
				App: &app.App{UI: ui.New(ui.Options{Format: format, Out: &out, Err: &errb})},
			}
			if err := listMeetings(c, upcoming()); err != nil {
				t.Fatalf("listMeetings: %v", err)
			}
			got := out.String() + errb.String()
			if strings.Contains(got, "meet.proton.me") || strings.Contains(got, "pwd-") {
				t.Errorf("the listing carries a link:\n%s", got)
			}
			if strings.Contains(got, `"url"`) || strings.Contains(got, "url:") {
				t.Errorf("the listing has a url key:\n%s", got)
			}
			for _, room := range []string{"My room", "Lobby"} {
				if strings.Contains(got, room) {
					t.Errorf("the listing of meetings holds the room %q", room)
				}
			}
			sooner, later, now := strings.Index(got, "Sooner"), strings.Index(got, "Later"), strings.Index(got, "Now")
			if sooner < 0 || later < 0 || now < 0 || sooner > later || later > now {
				t.Errorf("want soonest first and the instant meeting last:\n%s", got)
			}
		})
	}

	// The control: the record get shows does carry the link, so the check above
	// is one that can fail.
	var out bytes.Buffer
	c := &kit.Invocation{
		Ctx: context.Background(),
		App: &app.App{UI: ui.New(ui.Options{Format: ui.FormatJSON, Out: &out, Err: &bytes.Buffer{}})},
	}
	m := upcoming()[0]
	if err := kit.Show(c, ui.RecordSpec{Object: &m, Fields: meetingFields(&m)}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"url"`) || !strings.Contains(out.String(), "meet.proton.me") {
		t.Errorf("get's record has no link:\n%s", out.String())
	}
}

func TestOnlyAScheduledMeetingOfItsOwnMoves(t *testing.T) {
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		m    meetsvc.Meeting
		want []string
	}{
		{"scheduled", meetsvc.Meeting{Type: meetsvc.Scheduled, Start: &start}, nil},
		{"recurring", meetsvc.Meeting{Type: meetsvc.Recurring, Start: &start}, nil},
		{"on a calendar event", meetsvc.Meeting{
			Type: meetsvc.Scheduled, Start: &start, CalendarID: "cal1", CalendarEventID: "ev1",
		}, []string{"belongs to a calendar event", "proton calendar events update cal1/ev1"}},
		{"instant", meetsvc.Meeting{Type: meetsvc.Instant}, []string{"is an instant meeting"}},
		{"personal room", meetsvc.Meeting{Type: meetsvc.Personal}, []string{"is a room"}},
		{"permanent room", meetsvc.Meeting{Type: meetsvc.Permanent}, []string{"is a room"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			c.m.Title = "Team sync"
			err := movable(&c.m)
			if c.want == nil {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			var p *errs.Problem
			if err == nil || !errors.As(err, &p) {
				t.Fatalf("got %v, want a phrased refusal", err)
			}
			said := errs.Shown(err) + " " + strings.Join(p.Hints(), " ")
			for _, want := range c.want {
				if !strings.Contains(said, want) {
					t.Errorf("%q does not say %q", said, want)
				}
			}
			if strings.Contains(errs.Withheld(err), "Team sync") {
				t.Errorf("the title reaches the log: %q", errs.Withheld(err))
			}
		})
	}
}

func TestAMovedMeetingKeepsWhatIsNotSaid(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Vienna")
	if err != nil {
		t.Fatal(err)
	}
	was := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	wasEnd := was.Add(45 * time.Minute)
	m := &meetsvc.Meeting{Start: &was, End: &wasEnd}
	for _, c := range []struct {
		name       string
		f          schedule
		m          *meetsvc.Meeting
		start, end string
	}{
		{"a new start keeps the length", schedule{start: "2026-11-03T14:00"}, m,
			"2026-11-03T13:00:00Z", "2026-11-03T13:45:00Z"},
		{"a new length keeps the start", schedule{duration: "1h"}, m,
			"2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z"},
		{"a new end keeps the start", schedule{end: "2026-11-02T11:00"}, m,
			"2026-11-02T09:00:00Z", "2026-11-02T10:00:00Z"},
		{"a new meeting lasts half an hour", schedule{start: "2026-11-03T14:00"}, nil,
			"2026-11-03T13:00:00Z", "2026-11-03T13:30:00Z"},
	} {
		t.Run(c.name, func(t *testing.T) {
			start, end, err := c.f.times(loc, c.m)
			if err != nil {
				t.Fatalf("times: %v", err)
			}
			if got := start.UTC().Format(time.RFC3339); got != c.start {
				t.Errorf("start %s, want %s", got, c.start)
			}
			if got := end.UTC().Format(time.RFC3339); got != c.end {
				t.Errorf("end %s, want %s", got, c.end)
			}
		})
	}
	if _, _, err := (&schedule{end: "2026-11-02T08:00"}).times(loc, m); err == nil {
		t.Error("an end before the start was taken")
	}
}

// fakeChanger records the order in which a change reaches the service, and
// refuses what it is told to.
type fakeChanger struct {
	calls                    []string
	refuseRename, refuseMove error
}

func (f *fakeChanger) Rename(_ context.Context, _ *meetsvc.Meeting, _ string) error {
	f.calls = append(f.calls, "rename")
	return f.refuseRename
}

func (f *fakeChanger) Reschedule(context.Context, *meetsvc.Meeting, time.Time, time.Time, string, string) error {
	f.calls = append(f.calls, "reschedule")
	return f.refuseMove
}

// A move is the half of an update Proton is likelier to refuse, so it goes
// first: refused, it leaves the meeting untouched rather than renamed only.
func TestAnUpdateMovesBeforeItRenames(t *testing.T) {
	both := change{renaming: true, moving: true, title: "Design review", zone: "Europe/Vienna"}

	f := &fakeChanger{}
	if err := both.apply(context.Background(), f, &meetsvc.Meeting{}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.calls, ",") != "reschedule,rename" {
		t.Errorf("calls %v, want the move first", f.calls)
	}

	refused := errors.New("refused")
	f = &fakeChanger{refuseMove: refused}
	if err := both.apply(context.Background(), f, &meetsvc.Meeting{}); !errors.Is(err, refused) {
		t.Errorf("got %v, want the refusal", err)
	}
	if strings.Join(f.calls, ",") != "reschedule" {
		t.Errorf("calls %v: a refused move still renamed", f.calls)
	}

	f = &fakeChanger{refuseRename: refused}
	err := both.apply(context.Background(), f, &meetsvc.Meeting{})
	if !errors.Is(err, refused) || !strings.Contains(err.Error(), "the meeting moved") {
		t.Errorf("got %v, want it to say the move went through", err)
	}

	f = &fakeChanger{}
	if err := (change{renaming: true, title: "Design review"}).apply(context.Background(), f, &meetsvc.Meeting{}); err != nil ||
		strings.Join(f.calls, ",") != "rename" {
		t.Errorf("a rename alone made calls %v, %v", f.calls, err)
	}
}
