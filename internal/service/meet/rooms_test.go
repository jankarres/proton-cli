package meet

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
)

func TestCreatePersonalRefusesASecond(t *testing.T) {
	u := testAccount(t)
	room, _ := stored(t, u, "meeting-2", "PERSONAL01", "My room", Personal)
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{room}},
	}}
	_, err := testService(d, u).CreatePersonal(context.Background())
	var p *errs.Problem
	if !errors.As(err, &p) || p.ExitCode() != 1 {
		t.Fatalf("got %v, want a phrased refusal", err)
	}
	if !strings.Contains(err.Error(), "already has a personal room") ||
		!slices.Contains(p.Hints(), "proton meet rooms get personal") {
		t.Errorf("refusal %q, hints %v", err.Error(), p.Hints())
	}
	for _, r := range d.sent {
		if r.Method != "GET" {
			t.Errorf("a refused second room still sent %s %s", r.Method, r.Path)
		}
	}
}

func TestCreatePersonalMakesTheWebAppsRoom(t *testing.T) {
	u := testAccount(t)
	review, _ := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{review}},
		"POST /meet/v1/meetings": map[string]any{"Meeting": map[string]any{
			"ID": "room-id", "MeetingLinkName": "E87W2DAJ60", "AddressID": "addr-main", "Type": 1,
		}},
	}}
	m, err := testService(d, u).CreatePersonal(context.Background())
	if err != nil {
		t.Fatalf("CreatePersonal: %v", err)
	}
	sent := d.meetRequests()
	body := wire(t, sent[len(sent)-1].Body)
	if body["Type"] != 1.0 || body["ProtonCalendar"] != 0.0 || body["StartTime"] != nil {
		t.Errorf("sent type %v, calendar %v, start %v", body["Type"], body["ProtonCalendar"], body["StartTime"])
	}
	if m.Title != "Personal meeting room" || m.Type != Personal || m.Name != "E87W2DAJ60" {
		t.Errorf("made %+v", m)
	}
}

// Replacing the personal room's link is a new meeting in every part but the
// room: a new password, salt and session key, sent whole to the rotate route,
// and the link Proton's answer names opens with it.
func TestRotateSendsAFreshSecret(t *testing.T) {
	u := testAccount(t)
	d := &meetDoer{t: t, answers: map[string]any{
		"POST /meet/v1/meetings/personal/rotate": map[string]any{"Meeting": map[string]any{
			"ID": "room-id", "MeetingLinkName": "NEWLINK001", "AddressID": "addr-main", "Type": 1,
		}},
	}}
	s := testService(d, u)
	first, err := s.RotatePersonal(context.Background())
	if err != nil {
		t.Fatalf("RotatePersonal: %v", err)
	}
	second, err := s.RotatePersonal(context.Background())
	if err != nil {
		t.Fatalf("RotatePersonal: %v", err)
	}

	sent := d.meetRequests()
	if len(sent) != 2 {
		t.Fatalf("sent %d Meet requests, want 2", len(sent))
	}
	r := sent[0]
	if r.Method != "POST" || r.Path != "/meet/v1/meetings/personal/rotate" || !r.MeetHost {
		t.Errorf("sent %s %s (Meet door %v)", r.Method, r.Path, r.MeetHost)
	}
	body := wire(t, r.Body)
	want := map[string]any{
		"AddressID": "addr-main", "StartTime": nil, "EndTime": nil, "RRule": nil, "Timezone": nil,
		"CustomPassword": 0.0, "Type": 1.0, "ProtonCalendar": 0.0,
	}
	for k, v := range want {
		if !reflect.DeepEqual(body[k], v) {
			t.Errorf("%s = %#v, want %#v", k, body[k], v)
		}
	}

	name, password, err := ParseLink(first.URL)
	if err != nil || name != "NEWLINK001" {
		t.Fatalf("the new link %q reads as %q, %v", first.URL, name, err)
	}
	salt, _ := base64.StdEncoding.DecodeString(body["Salt"].(string))
	locked, _ := base64.StdEncoding.DecodeString(body["SessionKey"].(string))
	sk, err := unlockSessionKey(locked, password, salt)
	if err != nil {
		t.Fatalf("the new link's password does not open the session key sent: %v", err)
	}
	if title, err := openTitle(sk, body["Name"].(string)); err != nil || title != "Personal meeting room" {
		t.Errorf("sent title opens to %q, %v", title, err)
	}
	if copied, err := openPassword(u.UserKR, body["Password"].(string)); err != nil || copied != password {
		t.Errorf("host copy opens to %q, %v; want the new link's password", copied, err)
	}

	again := wire(t, sent[1].Body)
	for _, k := range []string{"Salt", "SessionKey", "SRPVerifier", "Password"} {
		if again[k] == body[k] {
			t.Errorf("two rotations sent the same %s", k)
		}
	}
	if first.URL == second.URL {
		t.Error("two rotations gave the same link")
	}
}

func TestRotateWithin24HoursIsPhrased(t *testing.T) {
	// What Proton answered on 2026-10-25, a few hours after the room was made.
	refused := &proton.APIError{
		HTTPStatus: 422, Code: 2028,
		Message: "You can only rotate your personal meeting link once every 24 hours",
	}
	d := &meetDoer{t: t, answers: map[string]any{"POST /meet/v1/meetings/personal/rotate": refused}}
	_, err := testService(d, testAccount(t)).RotatePersonal(context.Background())
	var p *errs.Problem
	if !errors.As(err, &p) {
		t.Fatalf("got %v, want a phrased refusal", err)
	}
	if p.ExitCode() != 1 || !strings.Contains(err.Error(), "once every 24 hours") {
		t.Errorf("exit %d, %q", p.ExitCode(), err.Error())
	}

	// The control: the same code for another limit is passed on as Proton's, so
	// it keeps the exit code a rate limit has.
	other := &proton.APIError{
		HTTPStatus: 422, Code: 2028, Message: "You have recently created too many meetings, please try again later.",
	}
	d = &meetDoer{t: t, answers: map[string]any{"POST /meet/v1/meetings/personal/rotate": other}}
	_, err = testService(d, testAccount(t)).RotatePersonal(context.Background())
	if !errors.Is(err, other) || errors.As(err, &p) {
		t.Errorf("another refusal came back as %v", err)
	}
}

func TestRotateWithoutAPersonalRoomSaysHowToMakeOne(t *testing.T) {
	gone := &proton.APIError{HTTPStatus: 422, Code: 2501, Message: "Meeting does not exist"}
	d := &meetDoer{t: t, answers: map[string]any{"POST /meet/v1/meetings/personal/rotate": gone}}
	_, err := testService(d, testAccount(t)).RotatePersonal(context.Background())
	var nf *errs.NotFound
	if !errors.As(err, &nf) || !slices.Contains(nf.Hints(), "proton meet rooms create --personal") {
		t.Errorf("got %v, want a not-found that names rooms create --personal", err)
	}
}
