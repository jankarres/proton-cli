package meet

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/roman-16/proton-cli/internal/account/keys"
	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
	"github.com/roman-16/proton-cli/internal/skip"
)

// What a new meeting declares about itself, beside its type.
const (
	// noCustomPassword is CustomPasswordState.NO_PASSWORD: the link's password is
	// the only one, which is the only state the web app creates.
	noCustomPassword = 0
	// fromProtonCalendar and notFromProtonCalendar are ProtonCalendarState.
	fromProtonCalendar    = 1
	notFromProtonCalendar = 0
)

// isoTime is how the web app writes a meeting's times, Date.toISOString: UTC,
// to the millisecond.
const isoTime = "2006-01-02T15:04:05.000Z"

// createBody is the payload of createMeetingCall as useSaveMeeting fills it.
// WaitingRoom is left out, as the web app leaves it out while its waiting-room
// flag is off, and the times, rule and zone are null when there are none.
type createBody struct {
	Name           string
	Password       string
	Salt           string
	SessionKey     string
	SRPModulusID   string
	SRPSalt        string
	SRPVerifier    string
	AddressID      string
	StartTime      *string
	EndTime        *string
	RRule          *string
	Timezone       *string
	CustomPassword int
	Type           int
	ProtonCalendar int
}

// rawMeeting is a meeting as Proton sends it, Meeting in
// @proton/shared/lib/interfaces/Meet.
type rawMeeting struct {
	ID              string
	MeetingLinkName string
	// MeetingName is the sealed title.
	MeetingName string
	// Password is the account's own copy of the link's password, armoured.
	Password   string
	Salt       string
	SessionKey string
	AddressID  string
	StartTime  unixTime
	EndTime    unixTime
	RRule      string
	Timezone   string
	Type       int
	CalendarID string
	// CalendarEventID names the event a meeting made for the calendar belongs
	// to, once the event is saved.
	CalendarEventID string
}

// unixTime is a meeting's time as Proton answers it: seconds since the epoch,
// which the interface declares a string and the web app reads with Number, so a
// bare number is taken too. Null is no time.
type unixTime struct{ t *time.Time }

func (u *unixTime) UnmarshalJSON(b []byte) error {
	b = bytes.Trim(b, `"`)
	if string(b) == "null" || len(b) == 0 {
		u.t = nil
		return nil
	}
	secs, err := strconv.ParseInt(string(b), 10, 64)
	if err != nil {
		return fmt.Errorf("read a meeting's time: %w", err)
	}
	t := time.Unix(secs, 0).UTC()
	u.t = &t
	return nil
}

// Create makes a meeting the way useSaveMeeting does: a fresh secret, the title
// sealed under its session key, the password copied to the account's primary
// user key, and an SRP verifier for the password so Proton can check a joiner
// without ever holding it.
func (s *Service) Create(ctx context.Context, nm NewMeeting) (*Meeting, error) {
	return s.send(ctx, "/meet/v1/meetings", "create the meeting", nm)
}

// send seals nm under a fresh secret and posts it to path, which is the create
// route or the personal room's rotate route: both take the whole of what makes a
// meeting, and both answer with the meeting as Proton now keeps it.
func (s *Service) send(ctx context.Context, path, doing string, nm NewMeeting) (*Meeting, error) {
	u, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	body, secret, err := s.sealed(ctx, u, nm)
	if err != nil {
		return nil, err
	}
	var r struct{ Meeting rawMeeting }
	if err := s.C.Decode(ctx, proton.Request{
		Method: "POST", Path: path, Body: body, MeetHost: true,
	}, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", doing, err)
	}
	m := meeting(u, r.Meeting)
	m.Title = nm.Title
	m.URL = Link(r.Meeting.MeetingLinkName, secret.Password)
	m.SessionKey = secret.SessionKey
	return m, nil
}

// sealed is the payload that makes nm, as prepareMeetingCryptoData builds it,
// and the secret it was sealed under.
func (s *Service) sealed(ctx context.Context, u *keys.Unlocked, nm NewMeeting) (createBody, Secret, error) {
	primary, err := u.PrimaryUserKey()
	if err != nil {
		return createBody{}, Secret{}, err
	}
	addressID := nm.AddressID
	if addressID == "" {
		if addressID, err = hostAddress(u); err != nil {
			return createBody{}, Secret{}, err
		}
	}

	secret, err := newSecret()
	if err != nil {
		return createBody{}, Secret{}, err
	}
	locked, err := secret.lockedSessionKey()
	if err != nil {
		return createBody{}, Secret{}, err
	}
	title, err := sealTitle(secret.SessionKey, nm.Title)
	if err != nil {
		return createBody{}, Secret{}, err
	}
	copied, err := sealPassword(primary, secret.Password)
	if err != nil {
		return createBody{}, Secret{}, err
	}
	modulus, err := proton.FetchModulus(ctx, s.C)
	if err != nil {
		return createBody{}, Secret{}, err
	}
	verifier, err := modulus.Verifier([]byte(secret.Password))
	if err != nil {
		return createBody{}, Secret{}, err
	}

	calendar := notFromProtonCalendar
	if nm.FromCalendar {
		calendar = fromProtonCalendar
	}
	return createBody{
		Name:           title,
		Password:       copied,
		Salt:           base64.StdEncoding.EncodeToString(secret.Salt),
		SessionKey:     base64.StdEncoding.EncodeToString(locked),
		SRPModulusID:   verifier.ModulusID,
		SRPSalt:        verifier.Salt,
		SRPVerifier:    verifier.Value,
		AddressID:      addressID,
		StartTime:      isoOrNull(nm.Start),
		EndTime:        isoOrNull(nm.End),
		RRule:          orNull(nm.RRule),
		Timezone:       orNull(nm.Timezone),
		CustomPassword: noCustomPassword,
		Type:           int(nm.Type),
		ProtonCalendar: calendar,
	}, secret, nil
}

// ByLink looks a meeting up by its link name and opens it. A password from a
// pasted link opens it directly; without one, the password is read out of the
// account's own copy, as decryptMeetingPassword does with every user key.
func (s *Service) ByLink(ctx context.Context, name, password string) (*Meeting, error) {
	if !isLinkName(name) {
		return nil, errs.Problemf("That is not a Proton Meet link.").
			Hint("pass the whole link, or the ten-character ID after id-")
	}
	var r struct{ Meeting rawMeeting }
	if err := s.C.Decode(ctx, proton.Request{
		Method: "GET", Path: "/meet/v1/meetings/by-link/" + name, MeetHost: true,
	}, &r); err != nil {
		if proton.DoesNotExist(err) {
			return nil, &errs.NotFound{Kind: "meeting", Ref: name}
		}
		return nil, err
	}
	u, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	return open(u, r.Meeting, password)
}

// open turns a meeting as Proton sends it into one the account can read: the
// password from the caller or the account's copy, the session key it unlocks,
// and the title under that.
func open(u *keys.Unlocked, raw rawMeeting, password string) (*Meeting, error) {
	given := password != ""
	if !given {
		if raw.Password == "" {
			return nil, errs.Problemf("Proton keeps no copy of this meeting's link for this account.").
				Hint("pass the whole link, including the part after #pwd-")
		}
		var err error
		if password, err = openPassword(u.UserKR, raw.Password); err != nil {
			return nil, err
		}
	}
	salt, err := base64.StdEncoding.DecodeString(raw.Salt)
	if err != nil {
		return nil, fmt.Errorf("read the meeting's salt: %w", err)
	}
	locked, err := base64.StdEncoding.DecodeString(raw.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("read the meeting's session key: %w", err)
	}
	sk, err := unlockSessionKey(locked, password, salt)
	if err != nil {
		// A password somebody pasted that opens nothing is a link that is not
		// this meeting's any more, or never was. One read out of the account's
		// own copy that opens nothing is a fault, and stays bare.
		if given {
			return nil, errs.Problemf("The password in that Proton Meet link does not open the meeting.").
				Hint("copy the link again, or pass only the ten-character ID to use the account's own copy")
		}
		return nil, err
	}
	title, err := openTitle(sk, raw.MeetingName)
	if err != nil {
		return nil, err
	}
	m := meeting(u, raw)
	m.Title = title
	m.URL = Link(raw.MeetingLinkName, password)
	m.SessionKey = sk
	return m, nil
}

// Delete deletes a meeting. Its link stops working for everyone it was sent to.
func (s *Service) Delete(ctx context.Context, m *Meeting) error {
	if err := checkID(m.ID); err != nil {
		return err
	}
	err := s.C.Decode(ctx, proton.Request{
		Method: "DELETE", Path: "/meet/v1/meetings/" + m.ID, MeetHost: true,
	}, nil)
	if proton.DoesNotExist(err) {
		return gone(m)
	}
	return err
}

// Rename gives a meeting a new title, sealed under its session key as
// updateMeetingNameCall sends it, and records the title on m once Proton has
// taken it.
func (s *Service) Rename(ctx context.Context, m *Meeting, title string) error {
	if err := checkID(m.ID); err != nil {
		return err
	}
	sealed, err := s.SealTitle(m, title)
	if err != nil {
		return err
	}
	err = s.C.Decode(ctx, proton.Request{
		Method: "PUT", Path: "/meet/v1/meetings/" + m.ID + "/name",
		Body: map[string]string{"Name": sealed}, MeetHost: true,
	}, nil)
	if proton.DoesNotExist(err) {
		return gone(m)
	}
	if err != nil {
		return err
	}
	m.Title = title
	return nil
}

// Upcoming is every meeting Proton lists as upcoming for the account, rooms
// included, as getUpcomingMeetingsQuery fetches them for the dashboard, each
// opened through the account's own copy of its password. A meeting that will
// not open is left out and recorded, so the listing can say it is short.
func (s *Service) Upcoming(ctx context.Context) ([]Meeting, error) {
	var r struct{ Meetings []rawMeeting }
	if err := s.C.Decode(ctx, proton.Request{
		Method: "GET", Path: "/meet/v1/meetings/upcoming", MeetHost: true,
	}, &r); err != nil {
		return nil, err
	}
	u, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Meeting, 0, len(r.Meetings))
	for _, raw := range r.Meetings {
		m, err := open(u, raw, "")
		if err != nil {
			skip.Record(ctx, skip.KindMeeting, raw.ID, shut(u, raw), err)
			continue
		}
		out = append(out, *m)
	}
	return out, nil
}

// personalRef is the reference that names the account's personal room, which
// has a link like any other meeting but is the one a person thinks of by kind.
const personalRef = "personal"

// Resolve finds the meeting a reference names: the ten-character ID the link
// shows, the whole link, or personal for the account's personal room.
func (s *Service) Resolve(ctx context.Context, ref string) (*Meeting, error) {
	if ref == personalRef {
		return s.personal(ctx)
	}
	name, password, err := ParseLink(ref)
	if err != nil {
		return nil, err
	}
	m, err := s.ByLink(ctx, name, password)
	if err != nil {
		return nil, err
	}
	if err := s.calendarEvent(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

// calendarEvent records which calendar event a meeting belongs to. The lookup
// by link answers null for both IDs even for a meeting an event carries, and the
// upcoming listing names them (both seen live on 2026-10-25), so the listing is
// read for this one meeting without opening any of the others. A meeting the
// listing does not hold, one already over, keeps what the lookup said.
func (s *Service) calendarEvent(ctx context.Context, m *Meeting) error {
	var r struct{ Meetings []rawMeeting }
	if err := s.C.Decode(ctx, proton.Request{
		Method: "GET", Path: "/meet/v1/meetings/upcoming", MeetHost: true,
	}, &r); err != nil {
		return err
	}
	for _, raw := range r.Meetings {
		if raw.ID == m.ID {
			m.CalendarID, m.CalendarEventID = raw.CalendarID, raw.CalendarEventID
			break
		}
	}
	return nil
}

// CheckRef refuses a reference no meeting could answer to, before anything is
// sent: what Resolve takes is personal, a link, or the ten characters of one.
func CheckRef(ref string) error {
	if ref == personalRef {
		return nil
	}
	_, _, err := ParseLink(ref)
	return err
}

// personal is the account's personal room. Proton keeps one at most and lists
// it among the upcoming meetings, which is where the dashboard finds it too.
func (s *Service) personal(ctx context.Context) (*Meeting, error) {
	all, err := s.Upcoming(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Type == Personal {
			return &all[i], nil
		}
	}
	return nil, noPersonalRoom()
}

// scheduleBody is the payload of updateMeetingScheduleCall: the times as
// Date.toISOString writes them, the zone they were chosen in, and the rule, null
// for a meeting that happens once.
type scheduleBody struct {
	StartTime string
	EndTime   string
	Timezone  string
	RRule     *string
}

// Reschedule moves a meeting to new times, and to a new rule or none, as
// useUpdateMeetingSchedule does. What Proton answers is recorded on m, since the
// server decides what the meeting is once moved.
func (s *Service) Reschedule(ctx context.Context, m *Meeting, start, end time.Time, rrule, timezone string) error {
	if err := checkID(m.ID); err != nil {
		return err
	}
	// The zone is what the times were chosen in, and every caller has one, so a
	// missing one is a fault and not the person's.
	if timezone == "" {
		return fmt.Errorf("reschedule meeting %s: no time zone to send", m.ID)
	}
	var r struct{ Meeting *rawMeeting }
	err := s.C.Decode(ctx, proton.Request{
		Method: "PUT", Path: "/meet/v1/meetings/" + m.ID + "/schedule",
		Body: scheduleBody{
			StartTime: start.UTC().Format(isoTime),
			EndTime:   end.UTC().Format(isoTime),
			Timezone:  timezone,
			RRule:     orNull(rrule),
		},
		MeetHost: true,
	}, &r)
	if proton.DoesNotExist(err) {
		return gone(m)
	}
	if err != nil {
		return err
	}
	if r.Meeting == nil {
		return fmt.Errorf("reschedule meeting %s: Proton's answer carries no meeting", m.ID)
	}
	m.Type = MeetingType(r.Meeting.Type)
	m.Start, m.End = r.Meeting.StartTime.t, r.Meeting.EndTime.t
	m.RRule, m.Timezone = r.Meeting.RRule, r.Meeting.Timezone
	return nil
}

// checkID refuses a meeting ID that would change the route it is put into.
// Proton's IDs are URL-safe base64 and never carry a slash, so one that does
// came from somewhere it should not have. A lone dot or two would be read as a
// step through the path by whatever cleans it up on the way.
func checkID(id string) error {
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/?#") {
		return fmt.Errorf("refuse meeting ID %q: not one Proton hands out", id)
	}
	return nil
}

// gone is the answer for a meeting Proton no longer has, named by the link name
// the person knows it by, or by Proton's ID when that is all there is.
func gone(m *Meeting) error {
	ref := m.Name
	if ref == "" {
		ref = m.ID
	}
	return &errs.NotFound{Kind: "meeting", Ref: ref}
}

// shut names why a listed meeting would not open, for the tally: no copy of its
// password for this account, a copy that is not a PGP message, or one sealed to
// a key that would not open it.
func shut(u *keys.Unlocked, raw rawMeeting) skip.Reason {
	if raw.Password == "" {
		return skip.NoKey
	}
	msg, err := pgp.NewPGPMessageFromArmored(raw.Password)
	if err != nil {
		return skip.Malformed
	}
	return u.Shut(msg)
}

// SealTitle is a title sealed under the meeting's session key, which is what a
// calendar event carries as MeetEncryptedTitle so the meeting's title follows
// the event's.
func (s *Service) SealTitle(m *Meeting, title string) (string, error) {
	return sealTitle(m.SessionKey, title)
}

// meeting is what a meeting says about itself without being opened.
func meeting(u *keys.Unlocked, raw rawMeeting) *Meeting {
	m := &Meeting{
		ID:              raw.ID,
		Name:            raw.MeetingLinkName,
		Type:            MeetingType(raw.Type),
		Start:           raw.StartTime.t,
		End:             raw.EndTime.t,
		Timezone:        raw.Timezone,
		RRule:           raw.RRule,
		CalendarID:      raw.CalendarID,
		CalendarEventID: raw.CalendarEventID,
	}
	for _, a := range u.Addresses {
		if a.ID == raw.AddressID {
			m.Host = a.Email
			break
		}
	}
	return m
}

// hostAddress is the address a meeting is hosted from when the caller names
// none: the first of the account's active addresses in Proton's order, which is
// activeAddresses[0] in useGetMeetingDependencies.
func hostAddress(u *keys.Unlocked) (string, error) {
	var first *keys.Address
	for i, a := range u.Addresses {
		if a.CanSend() && (first == nil || a.Order < first.Order) {
			first = &u.Addresses[i]
		}
	}
	if first == nil {
		return "", errs.Problemf("This account has no active address to host a meeting from.")
	}
	return first.ID, nil
}

func isoOrNull(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(isoTime)
	return &s
}

func orNull(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
