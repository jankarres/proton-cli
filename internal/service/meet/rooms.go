package meet

import (
	"context"
	"errors"
	"strings"

	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
)

// personalTitle is what the personal room is called when it is made and every
// time its link is replaced, personalMeetingName in the web app's useMeetingList.
const personalTitle = "Personal meeting room"

// Proton refuses to replace the personal room's link a second time within a day
// with the code it uses for every rate limit, API_CUSTOM_ERROR_CODES.BANNED, and
// the words "You can only rotate your personal meeting link once every 24 hours"
// (both seen live on 2026-10-25). The code alone also means too many meetings
// made, so the words are what tell this refusal apart.
const (
	rateLimitedCode = 2028
	rotateTooSoon   = "once every 24 hours"
)

// CreatePersonal makes the account's personal room, the one room a person
// reaches by kind rather than by name, as useMeetingList's setupPersonalMeeting
// makes it on the dashboard's first visit. An account has one at most, so a
// second is refused before anything is written.
func (s *Service) CreatePersonal(ctx context.Context) (*Meeting, error) {
	_, err := s.personal(ctx)
	var nf *errs.NotFound
	switch {
	case err == nil:
		return nil, errs.Problemf("This account already has a personal room.").
			Hint("proton meet rooms get personal", "proton meet rooms rotate personal")
	case !errors.As(err, &nf):
		return nil, err
	}
	return s.Create(ctx, NewMeeting{Title: personalTitle, Type: Personal})
}

// RotatePersonal gives the personal room a new link, as
// useRotatePersonalMeetingLink does: the whole of a new meeting, sealed under a
// fresh secret, sent to the rotate route, which keeps the room and replaces what
// opens it. The old link stops working for everyone it was sent to.
func (s *Service) RotatePersonal(ctx context.Context) (*Meeting, error) {
	m, err := s.send(ctx, "/meet/v1/meetings/personal/rotate", "give the personal room a new link",
		NewMeeting{Title: personalTitle, Type: Personal})
	var apiErr *proton.APIError
	if errors.As(err, &apiErr) && apiErr.Code == rateLimitedCode && strings.Contains(apiErr.Message, rotateTooSoon) {
		return nil, errs.Problemf("Your personal room's link can be replaced once every 24 hours.").
			Hint("proton meet rooms get personal")
	}
	if proton.DoesNotExist(err) {
		return nil, noPersonalRoom()
	}
	return m, err
}

// noPersonalRoom is the answer for an account without a personal room, with the
// command that makes one.
func noPersonalRoom() error {
	return &errs.NotFound{Kind: "personal room", Try: []string{"proton meet rooms create --personal"}}
}
