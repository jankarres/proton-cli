package live

import (
	"strings"
	"testing"
)

// Proton limits how many meetings an account may make in a while, and a room
// counts as one, so this file makes a single room. The personal room is read and
// given a new link, never deleted: making it again counts against the same limit.

func TestMeetRoomsARoomHasALinkAndIsListedWithoutIt(t *testing.T) {
	title := testID() + "-room"
	made := runJSON(t, "meet", "rooms", "create", "--title", title)
	id, _ := made["id"].(string)
	if id == "" {
		t.Fatalf("`rooms create` answered without an id: %v", made)
	}
	cleanupRun(t, "Delete room: proton meet rooms delete "+id, "meet", "rooms", "delete", id)
	link, _ := made["url"].(string)
	if !strings.HasPrefix(link, "https://meet.proton.me/join/id-"+id+"#pwd-") {
		t.Errorf("`rooms create` gave the link %q, want one for %s", link, id)
	}
	if made["type"] != "permanent" || made["title"] != title || made["start"] != nil {
		t.Errorf("`rooms create` made %v", made)
	}

	var listed map[string]interface{}
	for _, row := range runJSONArray(t, "meet", "rooms", "list") {
		m, _ := row.(map[string]interface{})
		if _, found := m["url"]; found {
			t.Errorf("a rooms listing carries a url key: %v", m)
		}
		if m["id"] == id {
			listed = m
		}
	}
	if listed == nil {
		t.Fatal("the room this test made is not in the listing")
	}
	for _, format := range []string{"text", "yaml"} {
		out := runOK(t, "--output", format, "meet", "rooms", "list")
		assertNotContains(t, out, "meet.proton.me")
		assertNotContains(t, out, "url:")
	}
	for _, row := range runJSONArray(t, "meet", "meetings", "list") {
		if m, _ := row.(map[string]interface{}); m["id"] == id {
			t.Error("the room is listed among the meetings")
		}
	}

	_, stderr, code := run(t, "meet", "meetings", "delete", "--yes", id)
	if code != 1 || !strings.Contains(stderr, "proton meet rooms delete "+id) {
		t.Errorf("`meetings delete` of a room: exit %d, %s; want it refused", code, strings.TrimSpace(stderr))
	}

	renamed := title + "-renamed"
	runOK(t, "meet", "rooms", "update", "--title", renamed, id)
	if got := runJSON(t, "meet", "rooms", "get", id); got["title"] != renamed || got["url"] != link {
		t.Errorf("after the rename `rooms get` answered %v, %v", got["title"], got["url"])
	}

	runOK(t, "meet", "rooms", "delete", id)
	if _, stderr, code := run(t, "meet", "rooms", "get", id); code != 3 {
		t.Errorf("`rooms get` after delete: exit %d, want 3: %s", code, strings.TrimSpace(stderr))
	}
}

// The personal room is reached as personal, only from the rooms, and its link
// can be replaced once a day. A run within a day of the last new link sees the
// refusal instead, which is the other half of the contract.
func TestMeetRoomsThePersonalRoomGetsANewLink(t *testing.T) {
	_, stderr, code := run(t, "meet", "rooms", "get", "personal")
	if code == 3 {
		t.Skipf("the account has no personal room: %s", strings.TrimSpace(stderr))
	}
	room := runJSON(t, "meet", "rooms", "get", "personal")
	if room["type"] != "personal" {
		t.Fatalf("`rooms get personal` answered a %v room", room["type"])
	}
	id, _ := room["id"].(string)

	if _, stderr, code := run(t, "meet", "meetings", "get", "personal"); code != 1 ||
		!strings.Contains(stderr, "proton meet rooms get personal") {
		t.Errorf("`meetings get personal`: exit %d, %s; want it refused", code, strings.TrimSpace(stderr))
	}
	if _, stderr, code := run(t, "meet", "rooms", "create", "--personal"); code != 1 ||
		!strings.Contains(stderr, "already has a personal room") {
		t.Errorf("a second personal room: exit %d, %s; want it refused", code, strings.TrimSpace(stderr))
	}

	stdout, stderr, code := run(t, "--output", "json", "meet", "rooms", "rotate", "--yes", "personal")
	switch {
	case code == 1 && strings.Contains(stderr, "once every 24 hours"):
		return
	case code != 0:
		t.Fatalf("`rooms rotate personal`: exit %d\nstderr: %s", code, strings.TrimSpace(stderr))
	}
	rotated := parseJSONObject(t, stdout)
	if rotated["url"] == room["url"] || rotated["type"] != "personal" {
		t.Errorf("`rooms rotate` answered %v, want a new link for the personal room", rotated)
	}
	if got := runJSON(t, "meet", "rooms", "get", "personal"); got["url"] != rotated["url"] {
		t.Errorf("`rooms get personal` after the rotation answered %v, want %v", got["url"], rotated["url"])
	}
	if old, _ := room["url"].(string); old != "" && rotated["id"] != id {
		if _, _, code := run(t, "meet", "rooms", "get", old); code != 3 && code != 1 {
			t.Errorf("the old link still opens the room after the rotation (exit %d)", code)
		}
	}
}
