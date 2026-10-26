package calendar

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/ical"
	"github.com/roman-16/proton-cli/internal/proton"
	"github.com/roman-16/proton-cli/internal/service/meet"
)

// An event's Proton Meet link is a meeting made beside it. The meeting itself
// is the Meet service's business, and this package never calls a Meet endpoint:
// it asks for a meeting, writes what the web client writes onto the event, and
// gives the meeting back if the event cannot be stored.

// Meetings is what the calendar needs of the Meet service.
type Meetings interface {
	Create(context.Context, meet.NewMeeting) (*meet.Meeting, error)
	Delete(context.Context, string) error
	ByLink(context.Context, string, string) (*meet.Meeting, error)
	SealTitle(*meet.Meeting, string) (string, error)
}

// SetMeetings hands the service the Meet service an event's link comes from.
func (s *Service) SetMeetings(m Meetings) { s.meetings = m }

// joinSeparator fences the join block the web client appends to a description,
// SEPARATOR_PROTON_EVENTS in packages/calendar-video-conferencing.
const joinSeparator = "~-~-~-~-~-~-~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~%~!~-~-~-~-~-~-~"

// joinBlock matches a fenced block with the newline in front of it, as
// videoConfInformationPattern does.
var joinBlock = regexp.MustCompile(`\n?` + regexp.QuoteMeta(joinSeparator) + `[\s\S]*?` + regexp.QuoteMeta(joinSeparator))

// addJoinLine appends the join block, which is how a client that does not read
// the conference properties still shows the link. It is the Proton Meet branch
// of addVideoConfInfoToDescription.
//
// Without a link there is nothing to join, and the description stays as it is.
func addJoinLine(desc, url string) string {
	if url == "" {
		return desc
	}
	return desc + "\n" + joinSeparator + "\nJoin Proton Meet: " + url + "\n" + joinSeparator
}

// stripJoinLine takes every join block out of a description again, as
// removeVideoConfInfoFromDescription does.
func stripJoinLine(desc string) string { return joinBlock.ReplaceAllString(desc, "") }

// meetingFor makes the meeting behind a new event's link and writes it onto the
// event, the way useProtonMeetIntegration does when the web client saves one.
//
// The meeting is hosted from the calendar's own address and made without
// times of its own: the web client sends none, and Proton takes them from the
// event once it is stored.
func (s *Service) meetingFor(ctx context.Context, ck *calKeys, body *eventBody) (*meet.Meeting, error) {
	if s.meetings == nil {
		return nil, fmt.Errorf("calendar: no Meet service to make a link with")
	}
	m, err := s.meetings.Create(ctx, meet.NewMeeting{
		Title: body.model.Summary, Type: meet.Scheduled, AddressID: ck.addressID, FromCalendar: true,
	})
	if err != nil {
		return nil, err
	}
	sealed, err := s.meetings.SealTitle(m, body.model.Summary)
	if err != nil {
		return nil, s.giveBack(ctx, m, err)
	}
	body.model = withMeeting(body.model, m, ck.email)
	body.meetEncryptedTitle = sealed
	return m, nil
}

// withMeeting writes a meeting onto an event: the conference properties, hosted
// from the calendar's own address, and the join block.
//
// A description that still carries a join block, such as a dead one from an
// earlier link, is cleaned first, so the event ends up with the new block alone.
func withMeeting(v ical.VEvent, m *meet.Meeting, host string) ical.VEvent {
	v.ConferenceID = m.Name
	v.ConferenceURL = m.URL
	v.ConferenceHost = host
	v.ConferenceProvider = ical.ConferenceProviderMeet
	v.Description = addJoinLine(stripJoinLine(v.Description), m.URL)
	return v
}

// withConferenceOf writes the conference another event carries onto v, join
// block included, as withMeeting does for a meeting just made.
func withConferenceOf(v, from ical.VEvent) ical.VEvent {
	v.ConferenceID = from.ConferenceID
	v.ConferenceURL = from.ConferenceURL
	v.ConferenceHost = from.ConferenceHost
	v.ConferenceProvider = from.ConferenceProvider
	v.Description = addJoinLine(stripJoinLine(v.Description), from.ConferenceURL)
	return v
}

// withoutMeeting takes an event's link away: the conference properties and the
// join block. The meeting behind it is endMeeting's business.
func withoutMeeting(v ical.VEvent) ical.VEvent {
	v.ConferenceID, v.ConferenceURL, v.ConferenceHost, v.ConferenceProvider = "", "", "", ""
	v.Description = stripJoinLine(v.Description)
	return v
}

// hasConference says whether an event carries a video conference of any kind,
// and hasMeetLink whether that conference is a Proton Meet one.
func hasConference(v ical.VEvent) bool { return v.ConferenceID != "" || v.ConferenceURL != "" }

func hasMeetLink(v ical.VEvent) bool {
	return hasConference(v) && v.ConferenceProvider == ical.ConferenceProviderMeet
}

// judgeMeet refuses a patch that asks for a link the event already has, or
// takes away one it does not have, before anything is made or written.
//
// An event the account was invited to is refused either: its link and the
// meeting behind it are the organizer's, and the web client offers the link
// row only on events whose shared data the account can edit
// (canEditSharedEventData). isOrganizer is the stored record's flag.
//
// An event with another service's conference is refused a Meet link as well:
// the two would share the event's one conference, and the other meeting would
// be left behind with nothing pointing at it.
func (p EventPatch) judgeMeet(v ical.VEvent, isOrganizer int) error {
	switch {
	case p.Meet == nil:
		return nil
	case isOrganizer != 1:
		return errs.Problemf("Only the organizer can change this event's Proton Meet link.").
			Hint("ask the organizer to add or remove it")
	case *p.Meet && hasMeetLink(v):
		return errs.Problemf("This event already has a Proton Meet link.").
			Hint("--no-meet removes it")
	case *p.Meet && hasConference(v):
		return errs.Problemf("This event already has a video call link from another service.").
			Hint("remove that link in the Proton Calendar web app first, then --meet adds one")
	case !*p.Meet && !hasMeetLink(v):
		return errs.Problemf("This event has no Proton Meet link to remove.").
			Hint("--meet gives it one")
	}
	return nil
}

// meetChange settles what a write does to the meeting behind an event's link.
// before is the event as it is stored, and body the event about to be written
// in its place.
//
// A patch asking for a link makes the meeting now, and hands it back so that a
// write which fails can delete it again. A patch that renames an event with a
// link carries the new title to the meeting, as useProtonMeetIntegration does
// when the title changes in the web client's form.
func (s *Service) meetChange(ctx context.Context, ck *calKeys, p EventPatch, before ical.VEvent, body *eventBody) (*meet.Meeting, error) {
	if p.Meet != nil && *p.Meet {
		return s.meetingFor(ctx, ck, body)
	}
	if p.Title != nil && *p.Title != before.Summary && hasMeetLink(body.model) {
		body.meetEncryptedTitle = s.retitledMeeting(ctx, body.model)
	}
	return nil, nil
}

// Why a renamed event's meeting kept its old title, from a closed set because
// it is written to the log as it stands.
const (
	titleNoMeetService = "no Meet service"
	titleNoPassword    = "link without a usable password"
	titleMeetingGone   = "meeting gone"
	titleUnreadable    = "meeting unreadable"
)

// retitledMeeting seals an event's new title under its meeting's session key.
// The key comes from the meeting, opened with the password in the event's own
// link, which is what decryptSessionKey is given in the web client.
//
// A meeting that cannot be opened does not stop the event being renamed: the
// title is the event's, and the meeting keeping its old one is a smaller loss
// than a refused edit. The warning says so, and an empty result leaves the
// meeting's title as it was.
func (s *Service) retitledMeeting(ctx context.Context, v ical.VEvent) string {
	sealed, reason, code := s.sealForMeeting(ctx, v)
	if reason != "" {
		// The rename is kept, so the warning is the whole of the loss, and the
		// person reads it on the screen. The log holds the reason and Proton's
		// code alone, since the link, its name and any error naming them are the
		// meeting's secret.
		slog.WarnContext(ctx, "The event was renamed, but its Proton Meet meeting kept its old title.",
			"reason", reason, "code", code)
	}
	return sealed
}

func (s *Service) sealForMeeting(ctx context.Context, v ical.VEvent) (sealed, reason string, code int) {
	if s.meetings == nil {
		return "", titleNoMeetService, 0
	}
	name, password, err := meet.ParseLink(v.ConferenceURL)
	if err != nil || password == "" {
		return "", titleNoPassword, 0
	}
	m, err := s.meetings.ByLink(ctx, name, password)
	var notFound *errs.NotFound
	var apiErr *proton.APIError
	switch {
	case errors.As(err, &notFound):
		return "", titleMeetingGone, 0
	case errors.As(err, &apiErr):
		return "", titleUnreadable, apiErr.Code
	case err != nil:
		return "", titleUnreadable, 0
	}
	if sealed, err = s.meetings.SealTitle(m, v.Summary); err != nil {
		return "", titleUnreadable, 0
	}
	return sealed, "", 0
}

// MeetingLeftBehind is an event write that failed after its meeting was made,
// where deleting the meeting again failed as well. It reads as the write's own
// error, and carries the meeting's ID, and the name its link opens with so the
// person can recognise it and delete it by hand.
type MeetingLeftBehind struct {
	ID      string
	Name    string
	Cleanup error
	Err     error
}

func (e *MeetingLeftBehind) Error() string { return e.Err.Error() }
func (e *MeetingLeftBehind) Unwrap() error { return e.Err }

// giveBack deletes a meeting whose event was never stored, so the failure leaves
// nothing behind. The deletion runs even when the failure was the command being
// cancelled, because that is exactly when the meeting would be forgotten.
//
// A write that made no meeting has nothing to give back, and its error is
// returned as it is.
func (s *Service) giveBack(ctx context.Context, m *meet.Meeting, err error) error {
	if m == nil {
		return err
	}
	if cleanup := s.meetings.Delete(context.WithoutCancel(ctx), m.ID); cleanup != nil {
		return &MeetingLeftBehind{ID: m.ID, Name: m.Name, Cleanup: cleanup, Err: err}
	}
	return err
}

// endMeeting deletes the meeting behind the link a whole event has just lost,
// once the event without it is stored. v is the event as it was, link included.
//
// Proton deletes such a meeting on its own only some of the time: in a live run
// a series' recurring meeting was still there long after the series had lost
// its last link, while a single event's was gone. So the meeting is looked up by
// the link, and deleted if it is still there. Found gone either way, it counts as
// deleted. The deletion runs even when the command was cancelled, because the
// event no longer names the meeting and nothing else would.
//
// A meeting that stays does not undo the event's change, which is already
// stored: the result names it instead.
func (s *Service) endMeeting(ctx context.Context, v ical.VEvent, res *EventResult) {
	ctx = context.WithoutCancel(ctx)
	name, password, err := meet.ParseLink(v.ConferenceURL)
	if err != nil {
		name = v.ConferenceID
	}
	err = s.deleteByLink(ctx, name, password, err)
	var notFound *errs.NotFound
	if err == nil || errors.As(err, &notFound) {
		return
	}
	// The meeting staying is on the screen as a warning naming it, so this is
	// recorded and not counted. The log holds Proton's code alone, since the
	// link's name is the meeting's secret.
	var apiErr *proton.APIError
	code := 0
	if errors.As(err, &apiErr) {
		code = apiErr.Code
	}
	slog.DebugContext(ctx, "the meeting outlived the link its event lost", "code", code)
	res.MeetingKept = &MeetingKept{Name: name, Err: err}
}

// deleteByLink looks a meeting up by its link and deletes it. unreadable is why
// the link could not be read, which is as far as it gets.
func (s *Service) deleteByLink(ctx context.Context, name, password string, unreadable error) error {
	switch {
	case unreadable != nil:
		return unreadable
	case s.meetings == nil:
		return errors.New("calendar: no Meet service to delete the meeting with")
	}
	m, err := s.meetings.ByLink(ctx, name, password)
	if err != nil {
		return err
	}
	return s.meetings.Delete(ctx, m.ID)
}

// MeetingKept is a meeting that outlived the link a whole event lost: the
// event's change is stored, and the meeting could not be deleted after it. Name
// is the name its link opens with, which the person can recognise and delete it
// by, and Err why it stayed.
type MeetingKept struct {
	Name string
	Err  error
}
