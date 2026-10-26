package meet

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/roman-16/proton-cli/internal/app"
	"github.com/roman-16/proton-cli/internal/cli/kit"
	"github.com/roman-16/proton-cli/internal/errs"
	meetsvc "github.com/roman-16/proton-cli/internal/service/meet"
	"github.com/roman-16/proton-cli/internal/ui"
)

// A room's link lets anybody in as a meeting's does, so the rooms listing never
// carries it either, and holds the rooms alone, the personal room first.
func TestARoomListingNeverCarriesTheLink(t *testing.T) {
	for _, format := range []ui.Format{ui.FormatText, ui.FormatJSON, ui.FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			var out, errb bytes.Buffer
			c := &kit.Invocation{
				Ctx: context.Background(),
				App: &app.App{UI: ui.New(ui.Options{Format: format, Out: &out, Err: &errb})},
			}
			if err := listRooms(c, upcoming()); err != nil {
				t.Fatalf("listRooms: %v", err)
			}
			got := out.String() + errb.String()
			if strings.Contains(got, "meet.proton.me") || strings.Contains(got, "pwd-") {
				t.Errorf("the listing carries a link:\n%s", got)
			}
			if strings.Contains(got, `"url"`) || strings.Contains(got, "url:") {
				t.Errorf("the listing has a url key:\n%s", got)
			}
			for _, meeting := range []string{"Later", "Sooner", "Now"} {
				if strings.Contains(got, meeting) {
					t.Errorf("the listing of rooms holds the meeting %q", meeting)
				}
			}
			own, lobby := strings.Index(got, "My room"), strings.Index(got, "Lobby")
			if own < 0 || lobby < 0 || own > lobby {
				t.Errorf("want the personal room first, then the others:\n%s", got)
			}
			if format == ui.FormatJSON && !strings.Contains(got, `"rooms"`) {
				t.Errorf("the envelope is not keyed rooms:\n%s", got)
			}
		})
	}
}

// A hint sending somebody to the other collection is a command that runs: an
// update names the flag it cannot do without.
func TestACrossCollectionHintRuns(t *testing.T) {
	for _, c := range []struct {
		in   collection
		typ  meetsvc.MeetingType
		verb string
		want string
	}{
		{meetingsCollection, meetsvc.Permanent, "update", "proton meet rooms update E87W2DAJ60 --title 'Team room'"},
		{roomsCollection, meetsvc.Scheduled, "update", "proton meet meetings update E87W2DAJ60 --title 'Team sync'"},
		{roomsCollection, meetsvc.Scheduled, "get", "proton meet meetings get E87W2DAJ60"},
	} {
		m := &meetsvc.Meeting{Name: "E87W2DAJ60", Title: "Team sync", Type: c.typ}
		var p *errs.Problem
		if err := c.in.admits(m, c.verb); !errors.As(err, &p) || !slices.Contains(p.Hints(), c.want) {
			t.Errorf("%s %s: got %v, want the hint %q", c.in.name, c.verb, err, c.want)
		}
	}
}

// Each collection takes only its own kind, and sends somebody holding the other
// kind to the command that takes it.
func TestEachCollectionRefusesTheOthersKind(t *testing.T) {
	for _, c := range []struct {
		in   collection
		typ  meetsvc.MeetingType
		want string // empty means admitted
	}{
		{meetingsCollection, meetsvc.Instant, ""},
		{meetingsCollection, meetsvc.Scheduled, ""},
		{meetingsCollection, meetsvc.Recurring, ""},
		{meetingsCollection, meetsvc.Personal, "proton meet rooms delete E87W2DAJ60"},
		{meetingsCollection, meetsvc.Permanent, "proton meet rooms delete E87W2DAJ60"},
		{roomsCollection, meetsvc.Personal, ""},
		{roomsCollection, meetsvc.Permanent, ""},
		{roomsCollection, meetsvc.Instant, "proton meet meetings delete E87W2DAJ60"},
		{roomsCollection, meetsvc.Scheduled, "proton meet meetings delete E87W2DAJ60"},
		{roomsCollection, meetsvc.Recurring, "proton meet meetings delete E87W2DAJ60"},
	} {
		t.Run(c.in.name+" "+c.typ.String(), func(t *testing.T) {
			m := &meetsvc.Meeting{Name: "E87W2DAJ60", Title: "Team sync", Type: c.typ}
			err := c.in.admits(m, "delete")
			if c.want == "" {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			var p *errs.Problem
			if err == nil || !errors.As(err, &p) || p.ExitCode() != 1 {
				t.Fatalf("got %v, want a phrased refusal", err)
			}
			if !strings.Contains(strings.Join(p.Hints(), " "), c.want) {
				t.Errorf("hints %v, want %q", p.Hints(), c.want)
			}
			if !strings.Contains(errs.Shown(err), "Team sync") || strings.Contains(errs.Withheld(err), "Team sync") {
				t.Errorf("shown %q, logged %q: the title belongs on screen only", errs.Shown(err), errs.Withheld(err))
			}
		})
	}
}
