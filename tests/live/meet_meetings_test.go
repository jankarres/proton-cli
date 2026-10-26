package live

import (
	"strings"
	"testing"
	"time"
)

// Proton limits how many meetings an account may make in a while, so this file
// makes two: one instant and one scheduled.

func TestMeetMeetingsAnInstantMeetingHasALinkAndNoTime(t *testing.T) {
	title := testID() + "-instant"
	made := runJSON(t, "meet", "meetings", "create", "--title", title)
	id, _ := made["id"].(string)
	if id == "" {
		t.Fatalf("`meetings create` answered without an id: %v", made)
	}
	cleanupRun(t, "Delete meeting: proton meet meetings delete "+id, "meet", "meetings", "delete", id)
	link, _ := made["url"].(string)
	if !strings.HasPrefix(link, "https://meet.proton.me/join/id-"+id+"#pwd-") {
		t.Errorf("`meetings create` gave the link %q, want one for %s", link, id)
	}
	if made["type"] != "instant" || made["title"] != title || made["start"] != nil {
		t.Errorf("`meetings create` without --start made %v", made)
	}

	if got := runJSON(t, "meet", "meetings", "get", id); got["url"] != link || got["title"] != title {
		t.Errorf("`meetings get %s` answered %v, %v; want the link and title create gave", id, got["url"], got["title"])
	}
	if got := runJSON(t, "meet", "meetings", "get", link); got["id"] != id {
		t.Errorf("`meetings get` by the whole link answered %v, want %s", got["id"], id)
	}

	_, stderr, code := run(t, "meet", "meetings", "update", "--start", "2026-11-02T10:00", id)
	if code != 1 || !strings.Contains(stderr, "is an instant meeting") {
		t.Errorf("moving an instant meeting: exit %d, %s; want it refused", code, strings.TrimSpace(stderr))
	}

	renamed := title + "-renamed"
	runOK(t, "meet", "meetings", "update", "--title", renamed, id)
	if got := runJSON(t, "meet", "meetings", "get", id); got["title"] != renamed || got["url"] != link {
		t.Errorf("after the rename `meetings get` answered %v, %v", got["title"], got["url"])
	}

	runOK(t, "meet", "meetings", "delete", id)
	if _, stderr, code := run(t, "meet", "meetings", "get", id); code != 3 {
		t.Errorf("`meetings get` after delete: exit %d, want 3: %s", code, strings.TrimSpace(stderr))
	}
}

func TestMeetMeetingsAScheduledMeetingMovesAndIsListedWithoutItsLink(t *testing.T) {
	title := testID() + "-scheduled"
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	made := runJSON(t, "meet", "meetings", "create", "--title", title,
		"--start", start.Format(time.RFC3339), "--duration", "30m")
	id, _ := made["id"].(string)
	if id == "" {
		t.Fatalf("`meetings create` answered without an id: %v", made)
	}
	cleanupRun(t, "Delete meeting: proton meet meetings delete "+id, "meet", "meetings", "delete", id)
	if made["type"] != "scheduled" {
		t.Errorf("`meetings create --start` made a %v meeting, want scheduled", made["type"])
	}
	assertSpan(t, made, start, 30*time.Minute)

	var listed map[string]interface{}
	for _, row := range runJSONArray(t, "meet", "meetings", "list") {
		m, _ := row.(map[string]interface{})
		if _, found := m["url"]; found {
			t.Errorf("a meetings listing carries a url key: %v", m)
		}
		if m["id"] == id {
			listed = m
		}
	}
	if listed == nil {
		t.Fatal("the meeting this test made is not in the listing")
	}
	for _, format := range []string{"text", "yaml"} {
		out := runOK(t, "--output", format, "meet", "meetings", "list")
		assertNotContains(t, out, "meet.proton.me")
		assertNotContains(t, out, "url:")
	}

	moved := start.Add(time.Hour)
	runOK(t, "meet", "meetings", "update", "--start", moved.Format(time.RFC3339), "--duration", "1h", id)
	got := runJSON(t, "meet", "meetings", "get", id)
	assertSpan(t, got, moved, time.Hour)
	if got["url"] != made["url"] {
		t.Error("moving a meeting changed its link")
	}
}

// assertSpan checks that a meeting as JSON starts at start and lasts length.
func assertSpan(t *testing.T, m map[string]interface{}, start time.Time, length time.Duration) {
	t.Helper()
	from, err1 := time.Parse(time.RFC3339, stringOf(m["start"]))
	to, err2 := time.Parse(time.RFC3339, stringOf(m["end"]))
	if err1 != nil || err2 != nil {
		t.Fatalf("meeting times %v to %v do not read as times", m["start"], m["end"])
	}
	if !from.Equal(start) || to.Sub(from) != length {
		t.Errorf("meeting runs %s to %s, want %s for %s", from, to, start, length)
	}
}

func stringOf(v interface{}) string {
	s, _ := v.(string)
	return s
}
