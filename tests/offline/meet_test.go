package offline

import "testing"

func meetings(args ...string) []string {
	return append([]string{"--zone", "Europe/Vienna", "meet", "meetings"}, args...)
}

func TestAMeetingNeedsATitle(t *testing.T) {
	refuses(t, 1, meetings("create"), "A meeting needs a title.", "--title")
	refuses(t, 1, meetings("create", "--title", " "), "A meeting needs a title.")
	refuses(t, 1, meetings("update", "--title", "", "6MCF0BSHBR"), "A meeting needs a title.")
}

func TestWhenAMeetingRunsIsJudgedFromTheCommandLine(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--start", "2026-11-02T10:00", "--duration", "1h", "--end", "2026-11-02T11:00"},
			"--end and --duration contradict each other."},
		{[]string{"--rrule", "FREQ=WEEKLY"}, "--rrule needs --start."},
		{[]string{"--duration", "1h"}, "--duration needs --start."},
		{[]string{"--end", "2026-11-02T11:00"}, "--end needs --start."},
		{[]string{"--start", "2026-11-02"}, "--start names a day but not a time of day."},
		{[]string{"--start", "tomorrow"}, "--start:"},
		{[]string{"--start", "2026-11-02T10:00", "--end", "2026-11-02"}, "--end names a day but not a time of day."},
		{[]string{"--start", "2026-11-02T10:00", "--duration", "soon"}, "--duration:"},
	} {
		refuses(t, 1, meetings(append([]string{"create", "--title", "Team sync"}, c.args...)...), c.want)
	}
	refuses(t, 1, meetings("update", "--duration", "1h", "--end", "2026-11-02T11:00", "6MCF0BSHBR"),
		"contradict each other.")
}

func TestChangingAMeetingNeedsSomethingToChange(t *testing.T) {
	refuses(t, 1, meetings("update", "6MCF0BSHBR"), "Nothing to change.")
}

func TestAMeetingIsNamedByItsLink(t *testing.T) {
	for _, verb := range []string{"get", "delete"} {
		refuses(t, 1, meetings(verb, "https://meet.proton.me/join/id-abc"), "That is not a Proton Meet link.")
		refuses(t, 1, meetings(verb, "https://example.com/join/id-6MCF0BSHBR"), "That is not a Proton Meet link.")
		refuses(t, 1, meetings(verb, "https://meet.proton.me/join/id-6MCF0BSHBR#pwd-short"),
			"The password in that Proton Meet link is missing or cut off.")
	}
	refuses(t, 1, meetings("update", "--title", "Team sync", "https://meet.proton.me/join/id-abc"),
		"That is not a Proton Meet link.")
}

func rooms(args ...string) []string {
	return append([]string{"meet", "rooms"}, args...)
}

// personal names a room, so the meetings collection refuses it before asking
// Proton, and says which command takes it.
func TestThePersonalRoomIsNotAMeeting(t *testing.T) {
	refuses(t, 1, meetings("get", "personal"), "Your personal room is a room, not a meeting.", "proton meet rooms get personal")
	refuses(t, 1, meetings("delete", "personal"), "proton meet rooms delete personal")
	refuses(t, 1, meetings("update", "--title", "Team sync", "personal"), "proton meet rooms update personal")
}

func TestARoomNeedsATitleOrIsThePersonalOne(t *testing.T) {
	refuses(t, 1, rooms("create"), "A room needs a title.", "--personal")
	refuses(t, 1, rooms("create", "--title", " "), "A room needs a title.")
	refuses(t, 1, rooms("create", "--title", "Team room", "--personal"), "contradict each other.")
}

func TestRenamingARoomNeedsATitle(t *testing.T) {
	refuses(t, 1, rooms("update", "personal"), "Nothing to change.", "--title")
	refuses(t, 1, rooms("update", "--title", "", "personal"), "A room needs a title.")
	refuses(t, 1, rooms("update", "--title", "Team room", "https://meet.proton.me/join/id-abc"),
		"That is not a Proton Meet link.")
}

func TestOnlyThePersonalRoomGetsANewLink(t *testing.T) {
	for _, ref := range []string{"E87W2DAJ60", "https://meet.proton.me/join/id-E87W2DAJ60#pwd-K0NXa1b2c3d4", "Personal"} {
		refuses(t, 1, rooms("rotate", ref), "Only your personal room can get a new link.",
			"proton meet rooms rotate personal")
	}
}
