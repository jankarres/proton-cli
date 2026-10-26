// Package meet provides Proton Meet operations: the meetings an account holds,
// and the crypto that makes one somebody can join with nothing but its link.
//
// A meeting is locked by the password in its link rather than by any key of the
// account's, because the people joining it hold no key of the account's. That
// password opens the meeting's session key, the session key opens its title,
// and the account keeps a copy of the password sealed to its own key so that a
// meeting it made can be shared again without the link having been kept. Every
// step mirrors packages/meet/utils/cryptoUtils.ts in WebClients, which is named
// beside each Go twin below, so what is written here is what the web app reads.
package meet

import (
	"fmt"
	"time"

	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/roman-16/proton-cli/internal/account/keys"
	"github.com/roman-16/proton-cli/internal/proton"
)

// Service holds the client every Meet request goes through and the account
// keys a meeting's password copy is sealed to and opened with.
type Service struct {
	C    proton.Doer
	keys keys.Get
}

func New(c proton.Doer, k keys.Get) *Service { return &Service{C: c, keys: k} }

// MeetingType is what kind of meeting Proton keeps a meeting as, MeetingType in
// @proton/shared/lib/interfaces/Meet.
type MeetingType int

const (
	Instant   MeetingType = 0 // MeetingType.INSTANT
	Personal  MeetingType = 1 // MeetingType.PERSONAL
	Scheduled MeetingType = 2 // MeetingType.SCHEDULED
	Recurring MeetingType = 3 // MeetingType.RECURRING
	Permanent MeetingType = 4 // MeetingType.PERMANENT
)

func (t MeetingType) String() string {
	switch t {
	case Instant:
		return "instant"
	case Personal:
		return "personal"
	case Scheduled:
		return "scheduled"
	case Recurring:
		return "recurring"
	case Permanent:
		return "permanent"
	}
	return fmt.Sprintf("type %d", int(t))
}

// MarshalText writes a type by its name, which is how every enum reaches the
// CLI's JSON. What is sent to Proton is the number, written by hand.
func (t MeetingType) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// NewMeeting is what a meeting is made from.
type NewMeeting struct {
	Title string
	Type  MeetingType
	// AddressID is the address the meeting is hosted from. Empty means the
	// account's first active address, as the web app picks.
	AddressID string
	// Start and End are the scheduled times, absent for a meeting with none.
	Start, End *time.Time
	RRule      string
	Timezone   string
	// FromCalendar says the meeting is made for a calendar event, which Proton
	// keeps in step with the event once it is saved.
	FromCalendar bool
}

// Meeting is one meeting as the account sees it, with its title opened and its
// link rebuilt.
type Meeting struct {
	// ID is Proton's ID of the meeting, which the routes that change it take and
	// no command does, so it stays out of what the CLI writes.
	ID string `json:"-"`
	// Name is the link name, the ten characters after id- that the link and the
	// join page show. It is what a person knows the meeting by and what every
	// command takes, so it is the meeting's id wherever the CLI writes one.
	Name  string `json:"id"`
	Title string `json:"title"`
	// URL is the link, password included.
	URL             string          `json:"url"`
	Host            string          `json:"host,omitempty"`
	Type            MeetingType     `json:"type"`
	Start           *time.Time      `json:"start,omitempty"`
	End             *time.Time      `json:"end,omitempty"`
	Timezone        string          `json:"timezone,omitempty"`
	RRule           string          `json:"rrule,omitempty"`
	CalendarID      string          `json:"calendar_id,omitempty"`
	CalendarEventID string          `json:"calendar_event_id,omitempty"`
	SessionKey      *pgp.SessionKey `json:"-"`
}
