package meet

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"time"

	"github.com/roman-16/proton-cli/internal/account/keys"
	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
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
	u, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	primary, err := u.PrimaryUserKey()
	if err != nil {
		return nil, err
	}
	addressID := nm.AddressID
	if addressID == "" {
		if addressID, err = hostAddress(u); err != nil {
			return nil, err
		}
	}

	secret, err := newSecret()
	if err != nil {
		return nil, err
	}
	locked, err := secret.lockedSessionKey()
	if err != nil {
		return nil, err
	}
	title, err := sealTitle(secret.SessionKey, nm.Title)
	if err != nil {
		return nil, err
	}
	copied, err := sealPassword(primary, secret.Password)
	if err != nil {
		return nil, err
	}
	modulus, err := proton.FetchModulus(ctx, s.C)
	if err != nil {
		return nil, err
	}
	verifier, err := modulus.Verifier([]byte(secret.Password))
	if err != nil {
		return nil, err
	}

	calendar := notFromProtonCalendar
	if nm.FromCalendar {
		calendar = fromProtonCalendar
	}
	body := createBody{
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
	}
	var r struct{ Meeting rawMeeting }
	if err := s.C.Decode(ctx, proton.Request{
		Method: "POST", Path: "/meet/v1/meetings", Body: body, MeetHost: true,
	}, &r); err != nil {
		return nil, fmt.Errorf("create the meeting: %w", err)
	}
	m := meeting(u, r.Meeting)
	m.Title = nm.Title
	m.URL = Link(r.Meeting.MeetingLinkName, secret.Password)
	m.SessionKey = secret.SessionKey
	return m, nil
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

// Delete deletes a meeting by Proton's ID of it. Its link stops working for
// everyone it was sent to.
func (s *Service) Delete(ctx context.Context, id string) error {
	err := s.C.Decode(ctx, proton.Request{
		Method: "DELETE", Path: "/meet/v1/meetings/" + id, MeetHost: true,
	}, nil)
	if proton.DoesNotExist(err) {
		return &errs.NotFound{Kind: "meeting", Ref: id}
	}
	return err
}

// Rename gives a meeting a new title, sealed under its session key as
// updateMeetingNameCall sends it, and records the title on m once Proton has
// taken it.
func (s *Service) Rename(ctx context.Context, m *Meeting, title string) error {
	sealed, err := s.SealTitle(m, title)
	if err != nil {
		return err
	}
	err = s.C.Decode(ctx, proton.Request{
		Method: "PUT", Path: "/meet/v1/meetings/" + m.ID + "/name",
		Body: map[string]string{"Name": sealed}, MeetHost: true,
	}, nil)
	if proton.DoesNotExist(err) {
		return &errs.NotFound{Kind: "meeting", Ref: m.Name}
	}
	if err != nil {
		return err
	}
	m.Title = title
	return nil
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
