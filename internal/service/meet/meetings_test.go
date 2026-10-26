package meet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/roman-16/proton-cli/internal/account/keys"
	pgphelper "github.com/roman-16/proton-cli/internal/crypto/pgp"
	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
)

// meetDoer stands in for Proton: it answers the SRP modulus and the Meet routes
// a test names, and keeps every request it was sent.
type meetDoer struct {
	t       *testing.T
	sent    []proton.Request
	answers map[string]any
}

func (d *meetDoer) Do(context.Context, proton.Request) (*proton.Response, error) {
	return nil, errors.New("meetDoer only decodes")
}

func (d *meetDoer) Decode(_ context.Context, r proton.Request, out any) error {
	d.sent = append(d.sent, r)
	key := r.Method + " " + r.Path
	if key == "GET /core/v4/auth/modulus" {
		modulus, err := os.ReadFile("testdata/modulus.asc")
		if err != nil {
			d.t.Fatalf("modulus fixture: %v", err)
		}
		return answer(map[string]any{"Modulus": string(modulus), "ModulusID": "modulus-id"}, out)
	}
	a, ok := d.answers[key]
	if !ok {
		return errors.New("unexpected request " + key)
	}
	if err, ok := a.(error); ok {
		return err
	}
	return answer(a, out)
}

func answer(v, out any) error {
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// meetRequests are the requests that went to Meet, leaving out the modulus.
func (d *meetDoer) meetRequests() []proton.Request {
	var out []proton.Request
	for _, r := range d.sent {
		if r.Path != "/core/v4/auth/modulus" {
			out = append(out, r)
		}
	}
	return out
}

// testAccount is an account with two user keys and two addresses, the first of
// which cannot send, so that the address a meeting is hosted from has to be
// chosen rather than taken first.
func testAccount(t *testing.T) *keys.Unlocked {
	t.Helper()
	kr := userKeyRing(t)
	second, err := pgphelper.GenerateKey("Test", "test@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := kr.AddKey(second); err != nil {
		t.Fatal(err)
	}
	return &keys.Unlocked{
		UserKR: kr,
		Addresses: []keys.Address{
			{ID: "addr-off", Email: "old@proton.me", Order: 1, Status: 0, Send: 1, Receive: 1},
			{ID: "addr-alias", Email: "alias@proton.me", Order: 3, Status: 1, Send: 1, Receive: 1},
			{ID: "addr-main", Email: "me@proton.me", Order: 2, Status: 1, Send: 1, Receive: 1},
		},
	}
}

func testService(d *meetDoer, u *keys.Unlocked) *Service {
	return New(d, func(context.Context) (*keys.Unlocked, error) { return u, nil })
}

func TestCreateSendsWhatTheWebClientSends(t *testing.T) {
	u := testAccount(t)
	start := time.Date(2026, 10, 26, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	end := start.Add(30 * time.Minute)
	made := map[string]any{"Meeting": map[string]any{
		"ID": "meeting-id", "MeetingLinkName": "6MCF0BSHBR", "AddressID": "addr-main",
		"Type": 2, "StartTime": "1793005200", "EndTime": 1793007000, "Timezone": "Europe/Vienna",
	}}

	cases := []struct {
		name string
		in   NewMeeting
		want map[string]any
	}{
		{
			name: "scheduled, from the meet app",
			in: NewMeeting{
				Title: "Design review", Type: Scheduled, Start: &start, End: &end, Timezone: "Europe/Vienna",
			},
			want: map[string]any{
				"AddressID": "addr-main", "StartTime": "2026-10-26T09:00:00.000Z", "EndTime": "2026-10-26T09:30:00.000Z",
				"RRule": nil, "Timezone": "Europe/Vienna", "CustomPassword": 0.0, "Type": 2.0, "ProtonCalendar": 0.0,
			},
		},
		{
			name: "from a calendar event, with its address",
			in:   NewMeeting{Title: "Design review", Type: Scheduled, AddressID: "addr-alias", FromCalendar: true},
			want: map[string]any{
				"AddressID": "addr-alias", "StartTime": nil, "EndTime": nil,
				"RRule": nil, "Timezone": nil, "CustomPassword": 0.0, "Type": 2.0, "ProtonCalendar": 1.0,
			},
		},
		{
			name: "recurring",
			in: NewMeeting{
				Title: "Stand-up", Type: Recurring, Start: &start, End: &end,
				RRule: "FREQ=WEEKLY;BYDAY=MO", Timezone: "Europe/Vienna",
			},
			want: map[string]any{
				"AddressID": "addr-main", "StartTime": "2026-10-26T09:00:00.000Z", "EndTime": "2026-10-26T09:30:00.000Z",
				"RRule": "FREQ=WEEKLY;BYDAY=MO", "Timezone": "Europe/Vienna", "CustomPassword": 0.0, "Type": 3.0, "ProtonCalendar": 0.0,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &meetDoer{t: t, answers: map[string]any{"POST /meet/v1/meetings": made}}
			m, err := testService(d, u).Create(context.Background(), c.in)
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			sent := d.meetRequests()
			if len(sent) != 1 {
				t.Fatalf("sent %d Meet requests, want 1", len(sent))
			}
			r := sent[0]
			if !r.MeetHost {
				t.Error("the create did not go through the Meet door")
			}
			body := wire(t, r.Body)

			var got []string
			for k := range body {
				got = append(got, k)
			}
			sort.Strings(got)
			want := []string{
				"AddressID", "CustomPassword", "EndTime", "Name", "Password", "ProtonCalendar", "RRule",
				"SRPModulusID", "SRPSalt", "SRPVerifier", "Salt", "SessionKey", "StartTime", "Timezone", "Type",
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("payload keys %v\nwant %v", got, want)
			}
			for k, v := range c.want {
				if !reflect.DeepEqual(body[k], v) {
					t.Errorf("%s = %#v, want %#v", k, body[k], v)
				}
			}
			if body["SRPModulusID"] != "modulus-id" || body["SRPSalt"] == "" || body["SRPVerifier"] == "" {
				t.Errorf("SRP %v / %v / %v", body["SRPModulusID"], body["SRPSalt"], body["SRPVerifier"])
			}

			// The link is the one way in, so everything sent has to open with it.
			name, password, err := ParseLink(m.URL)
			if err != nil {
				t.Fatalf("the returned link does not parse: %v", err)
			}
			if name != "6MCF0BSHBR" || m.Name != name || m.ID != "meeting-id" {
				t.Errorf("link name %q, Name %q, ID %q", name, m.Name, m.ID)
			}
			salt, _ := base64.StdEncoding.DecodeString(body["Salt"].(string))
			locked, _ := base64.StdEncoding.DecodeString(body["SessionKey"].(string))
			sk, err := unlockSessionKey(locked, password, salt)
			if err != nil {
				t.Fatalf("the link's password does not open the session key sent: %v", err)
			}
			if title, err := openTitle(sk, body["Name"].(string)); err != nil || title != c.in.Title {
				t.Errorf("sent title opens to %q, %v", title, err)
			}
			if copied, err := openPassword(u.UserKR, body["Password"].(string)); err != nil || copied != password {
				t.Errorf("host copy opens to %q, %v; want the link's password", copied, err)
			}
			for k, v := range body {
				if v == password {
					t.Errorf("%s carries the link's password in the clear", k)
				}
			}
			if m.Title != c.in.Title || m.SessionKey == nil || string(m.SessionKey.Key) != string(sk.Key) {
				t.Errorf("returned title %q, session key %v", m.Title, m.SessionKey)
			}
			if m.Host != "me@proton.me" || m.Type != Scheduled || m.Timezone != "Europe/Vienna" {
				t.Errorf("host %q, type %v, zone %q", m.Host, m.Type, m.Timezone)
			}
			if m.Start == nil || !m.Start.Equal(start) || m.End == nil || !m.End.Equal(end) {
				t.Errorf("times %v to %v, want the server's %v to %v", m.Start, m.End, start, end)
			}
		})
	}
}

// The host copy is sealed with the primary key, but the web app opens one with
// every user key, so a meeting made before a key rotation still opens.
func TestByLinkRecoversTheLinkFromTheHostCopy(t *testing.T) {
	u := testAccount(t)
	secret, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	older, err := u.UserKR.GetKey(1)
	if err != nil {
		t.Fatal(err)
	}
	olderKR, err := pgp.NewKeyRing(older)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := sealPassword(olderKR, secret.Password)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := secret.lockedSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealTitle(secret.SessionKey, "Design review")
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]any{"Meeting": map[string]any{
		"ID": "meeting-id", "MeetingLinkName": "6MCF0BSHBR", "MeetingName": sealed,
		"Password": copied, "Salt": base64.StdEncoding.EncodeToString(secret.Salt),
		"SessionKey": base64.StdEncoding.EncodeToString(locked), "AddressID": "addr-main",
		"Type": 2, "StartTime": nil, "EndTime": nil, "CalendarID": "cal-id", "CalendarEventID": "event-id",
	}}
	d := &meetDoer{t: t, answers: map[string]any{"GET /meet/v1/meetings/by-link/6MCF0BSHBR": stored}}
	s := testService(d, u)

	m, err := s.ByLink(context.Background(), "6MCF0BSHBR", "")
	if err != nil {
		t.Fatalf("ByLink: %v", err)
	}
	if m.Title != "Design review" || m.URL != Link("6MCF0BSHBR", secret.Password) {
		t.Errorf("title %q, link %q", m.Title, m.URL)
	}
	if m.SessionKey == nil || string(m.SessionKey.Key) != string(secret.SessionKey.Key) {
		t.Error("the session key is not the meeting's")
	}
	if m.Host != "me@proton.me" || m.CalendarID != "cal-id" || m.CalendarEventID != "event-id" || m.Start != nil {
		t.Errorf("host %q, calendar %q/%q, start %v", m.Host, m.CalendarID, m.CalendarEventID, m.Start)
	}
	if r := d.sent[0]; !r.MeetHost {
		t.Error("the lookup did not go through the Meet door")
	}

	// A password the caller already holds is used rather than the copy.
	if m, err := s.ByLink(context.Background(), "6MCF0BSHBR", secret.Password); err != nil || m.Title != "Design review" {
		t.Errorf("with the link's password: %v, %v", m, err)
	}

	// A wrong one from a pasted link is the person's mistake and reads as one.
	_, err = s.ByLink(context.Background(), "6MCF0BSHBR", "AAAAAAAAAAAA")
	var p *errs.Problem
	if !errors.As(err, &p) {
		t.Errorf("a wrong password gave %v, want a phrased problem", err)
	}
}

func TestByLinkOfAMeetingThatIsGoneIsNotFound(t *testing.T) {
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/by-link/6MCF0BSHBR": &proton.APIError{HTTPStatus: 422, Code: 2501},
	}}
	_, err := testService(d, testAccount(t)).ByLink(context.Background(), "6MCF0BSHBR", "")
	var nf *errs.NotFound
	if !errors.As(err, &nf) || nf.Kind != "meeting" || nf.Ref != "6MCF0BSHBR" {
		t.Errorf("got %v, want a meeting not found", err)
	}
}

func TestDeleteAndRenameGoThroughTheMeetDoor(t *testing.T) {
	secret, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	d := &meetDoer{t: t, answers: map[string]any{
		"DELETE /meet/v1/meetings/meeting-id":   map[string]any{},
		"PUT /meet/v1/meetings/meeting-id/name": map[string]any{},
	}}
	s := testService(d, testAccount(t))
	m := &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR", Title: "Old", SessionKey: secret.SessionKey}

	if err := s.Rename(context.Background(), m, "New title"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := s.Delete(context.Background(), "meeting-id"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(d.sent) != 2 {
		t.Fatalf("sent %d requests, want 2", len(d.sent))
	}
	for _, r := range d.sent {
		if !r.MeetHost {
			t.Errorf("%s %s did not go through the Meet door", r.Method, r.Path)
		}
	}
	body := wire(t, d.sent[0].Body)
	if len(body) != 1 {
		t.Errorf("rename sent %v, want only Name", body)
	}
	if title, err := openTitle(secret.SessionKey, body["Name"].(string)); err != nil || title != "New title" {
		t.Errorf("the new name opens to %q, %v", title, err)
	}
	if m.Title != "New title" {
		t.Errorf("the meeting still says %q", m.Title)
	}

	sealed, err := s.SealTitle(m, "For the calendar")
	if err != nil {
		t.Fatal(err)
	}
	if title, err := openTitle(secret.SessionKey, sealed); err != nil || title != "For the calendar" {
		t.Errorf("SealTitle opens to %q, %v", title, err)
	}
}

func TestATypeReadsByName(t *testing.T) {
	names := map[MeetingType]string{
		Instant: "instant", Personal: "personal", Scheduled: "scheduled", Recurring: "recurring", Permanent: "permanent",
	}
	for typ, name := range names {
		if typ.String() != name {
			t.Errorf("%d is %q, want %q", int(typ), typ.String(), name)
		}
	}
	raw, err := json.Marshal(Meeting{Type: Recurring})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["type"] != "recurring" {
		t.Errorf("JSON type %v, want recurring", out["type"])
	}
	if _, ok := out["session_key"]; ok {
		t.Error("the session key reaches JSON")
	}
}

// wire is a request body as it travels: encoded, then read back as JSON.
func wire(t *testing.T, body any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
