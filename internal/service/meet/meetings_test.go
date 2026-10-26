package meet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"slices"
	"sort"
	"testing"
	"time"

	pgp "github.com/ProtonMail/gopenpgp/v3/crypto"
	"github.com/roman-16/proton-cli/internal/account/keys"
	pgphelper "github.com/roman-16/proton-cli/internal/crypto/pgp"
	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/proton"
	"github.com/roman-16/proton-cli/internal/skip"
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
	if err := s.Delete(context.Background(), m); err != nil {
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

// stored is a meeting as Proton keeps it for the test account: sealed under a
// fresh secret, with the host copy of its password sealed to the primary key.
// It returns the meeting and the link it opens to.
func stored(t *testing.T, u *keys.Unlocked, id, name, title string, typ MeetingType) (map[string]any, string) {
	t.Helper()
	secret, err := newSecret()
	if err != nil {
		t.Fatal(err)
	}
	copied, err := sealPassword(u.UserKR, secret.Password)
	if err != nil {
		t.Fatal(err)
	}
	locked, err := secret.lockedSessionKey()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealTitle(secret.SessionKey, title)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"ID": id, "MeetingLinkName": name, "MeetingName": sealed, "Password": copied,
		"Salt": base64.StdEncoding.EncodeToString(secret.Salt), "SessionKey": base64.StdEncoding.EncodeToString(locked),
		"AddressID": "addr-main", "Type": int(typ), "StartTime": nil, "EndTime": nil, "RRule": nil, "Timezone": nil,
	}, Link(name, secret.Password)
}

func TestUpcomingOpensEveryTitle(t *testing.T) {
	u := testAccount(t)
	review, reviewLink := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	review["StartTime"], review["EndTime"], review["Timezone"] = 1793091600, 1793093400, "Europe/Berlin"
	room, roomLink := stored(t, u, "meeting-2", "PERSONAL01", "My room", Personal)
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{review, room}},
	}}

	got, err := testService(d, u).Upcoming(context.Background())
	if err != nil {
		t.Fatalf("Upcoming: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d meetings, want 2", len(got))
	}
	if got[0].Title != "Design review" || got[0].URL != reviewLink || got[0].Type != Scheduled || got[0].Host != "me@proton.me" {
		t.Errorf("first meeting %+v", got[0])
	}
	if got[0].Start == nil || got[0].Start.Unix() != 1793091600 || got[0].Timezone != "Europe/Berlin" {
		t.Errorf("first meeting starts %v in %q", got[0].Start, got[0].Timezone)
	}
	if got[1].Title != "My room" || got[1].URL != roomLink || got[1].Type != Personal || got[1].Start != nil {
		t.Errorf("second meeting %+v", got[1])
	}
	if r := d.sent[0]; !r.MeetHost {
		t.Error("the listing did not go through the Meet door")
	}
}

func TestAMeetingThatWillNotOpenIsRecorded(t *testing.T) {
	u := testAccount(t)
	good, _ := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	garbled, _ := stored(t, u, "meeting-2", "GARBLED001", "Garbled", Scheduled)
	garbled["Password"] = "not a PGP message"
	uncopied, _ := stored(t, u, "meeting-3", "NOCOPY0001", "No copy", Scheduled)
	uncopied["Password"] = ""
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{garbled, good, uncopied}},
	}}
	ctx, tally := skip.With(context.Background())

	got, err := testService(d, u).Upcoming(ctx)
	if err != nil {
		t.Fatalf("Upcoming: %v", err)
	}
	if len(got) != 1 || got[0].ID != "meeting-1" {
		t.Fatalf("got %+v, want only the meeting that opens", got)
	}
	if tally.Count() != 2 || tally.Kind() != skip.KindMeeting {
		t.Errorf("tally counts %d of kind %q, want 2 meetings", tally.Count(), tally.Kind())
	}
}

func TestUpcomingPassesOnAFailedListing(t *testing.T) {
	refused := &proton.APIError{HTTPStatus: 500, Code: 2000}
	d := &meetDoer{t: t, answers: map[string]any{"GET /meet/v1/meetings/upcoming": refused}}
	if _, err := testService(d, testAccount(t)).Upcoming(context.Background()); !errors.Is(err, refused) {
		t.Errorf("got %v, want the API's refusal", err)
	}
}

func TestResolvePersonal(t *testing.T) {
	u := testAccount(t)
	review, _ := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	room, roomLink := stored(t, u, "meeting-2", "PERSONAL01", "My room", Personal)

	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{review, room}},
	}}
	m, err := testService(d, u).Resolve(context.Background(), "personal")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.ID != "meeting-2" || m.URL != roomLink {
		t.Errorf("resolved %q to %q, want the personal room", m.ID, m.URL)
	}

	d = &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/upcoming": map[string]any{"Meetings": []any{review}},
	}}
	_, err = testService(d, u).Resolve(context.Background(), "personal")
	var nf *errs.NotFound
	if !errors.As(err, &nf) || nf.ExitCode() != 3 {
		t.Fatalf("without a personal room got %v, want a not-found", err)
	}
	if nf.Error() != "No personal room found." {
		t.Errorf("message %q", nf.Error())
	}
	if !slices.Contains(nf.Hints(), "proton meet rooms create --personal") {
		t.Errorf("hints %v, want the command that makes one", nf.Hints())
	}
}

func TestResolveALinkOrItsName(t *testing.T) {
	u := testAccount(t)
	review, link := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/by-link/6MCF0BSHBR": map[string]any{"Meeting": review},
		"GET /meet/v1/meetings/upcoming":           map[string]any{"Meetings": []any{}},
	}}
	s := testService(d, u)
	for _, ref := range []string{"6MCF0BSHBR", link} {
		m, err := s.Resolve(context.Background(), ref)
		if err != nil || m.Title != "Design review" || m.URL != link {
			t.Errorf("Resolve(%q) = %+v, %v", ref, m, err)
		}
	}
	if _, err := s.Resolve(context.Background(), "https://example.com/join"); err == nil {
		t.Error("a link that is not Meet's resolved")
	}
	if len(d.sent) != 4 {
		t.Errorf("sent %d requests, want a lookup and a listing per good reference", len(d.sent))
	}
}

// The lookup by link never says which calendar event a meeting belongs to, and
// the listing does, so a resolved meeting carries what the listing says.
func TestResolveReadsTheCalendarEventFromTheListing(t *testing.T) {
	u := testAccount(t)
	review, _ := stored(t, u, "meeting-1", "6MCF0BSHBR", "Design review", Scheduled)
	listed := map[string]any{}
	for k, v := range review {
		listed[k] = v
	}
	listed["CalendarID"], listed["CalendarEventID"] = "calendar-1", "event-1"
	other, _ := stored(t, u, "meeting-2", "865JZ8JMHG", "Other", Scheduled)
	other["CalendarID"], other["CalendarEventID"] = "calendar-1", "event-2"
	d := &meetDoer{t: t, answers: map[string]any{
		"GET /meet/v1/meetings/by-link/6MCF0BSHBR": map[string]any{"Meeting": review},
		"GET /meet/v1/meetings/upcoming":           map[string]any{"Meetings": []any{other, listed}},
	}}
	m, err := testService(d, u).Resolve(context.Background(), "6MCF0BSHBR")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.CalendarID != "calendar-1" || m.CalendarEventID != "event-1" {
		t.Errorf("resolved meeting belongs to %q/%q, want calendar-1/event-1", m.CalendarID, m.CalendarEventID)
	}

	// A meeting the listing does not hold, one already past, keeps what the
	// lookup said, which is no event.
	d.answers["GET /meet/v1/meetings/upcoming"] = map[string]any{"Meetings": []any{other}}
	m, err = testService(d, u).Resolve(context.Background(), "6MCF0BSHBR")
	if err != nil || m.CalendarEventID != "" {
		t.Errorf("a meeting not listed resolved to event %q, %v", m.CalendarEventID, err)
	}
}

func TestRescheduleSendsTheWebShape(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.FixedZone("CET", 3600))
	end := start.Add(time.Hour)
	moved := map[string]any{"Meeting": map[string]any{
		"ID": "meeting-id", "MeetingLinkName": "6MCF0BSHBR", "Type": 3,
		"StartTime": start.Unix(), "EndTime": end.Unix(), "Timezone": "Europe/Vienna", "RRule": "FREQ=WEEKLY",
	}}

	cases := []struct {
		name  string
		rrule string
		want  map[string]any
	}{
		{"once", "", map[string]any{
			"StartTime": "2026-11-02T09:00:00.000Z", "EndTime": "2026-11-02T10:00:00.000Z",
			"Timezone": "Europe/Vienna", "RRule": nil,
		}},
		{"weekly", "FREQ=WEEKLY", map[string]any{
			"StartTime": "2026-11-02T09:00:00.000Z", "EndTime": "2026-11-02T10:00:00.000Z",
			"Timezone": "Europe/Vienna", "RRule": "FREQ=WEEKLY",
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &meetDoer{t: t, answers: map[string]any{"PUT /meet/v1/meetings/meeting-id/schedule": moved}}
			m := &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR", Title: "Stand-up", Type: Scheduled}
			if err := testService(d, testAccount(t)).Reschedule(context.Background(), m, start, end, c.rrule, "Europe/Vienna"); err != nil {
				t.Fatalf("Reschedule: %v", err)
			}
			if len(d.sent) != 1 || !d.sent[0].MeetHost {
				t.Fatalf("sent %+v, want one request through the Meet door", d.sent)
			}
			if body := wire(t, d.sent[0].Body); !reflect.DeepEqual(body, c.want) {
				t.Errorf("sent %v\nwant %v", body, c.want)
			}
			// What Proton answers is what the meeting now is.
			if m.Start == nil || !m.Start.Equal(start) || m.End == nil || !m.End.Equal(end) ||
				m.RRule != "FREQ=WEEKLY" || m.Timezone != "Europe/Vienna" || m.Type != Recurring || m.Title != "Stand-up" {
				t.Errorf("meeting after the move %+v", m)
			}
		})
	}

	d := &meetDoer{t: t, answers: map[string]any{
		"PUT /meet/v1/meetings/meeting-id/schedule": &proton.APIError{HTTPStatus: 422, Code: 2501},
	}}
	m := &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR"}
	err := testService(d, testAccount(t)).Reschedule(context.Background(), m, start, end, "", "Europe/Vienna")
	var nf *errs.NotFound
	if !errors.As(err, &nf) || nf.Ref != "6MCF0BSHBR" {
		t.Errorf("a meeting that is gone gave %v, want a not-found by its link name", err)
	}
}

// An answer that carries no meeting says nothing about where the meeting now
// is, so it is an error rather than a meeting quietly emptied of its times.
func TestARescheduleAnsweredWithoutAMeetingIsAnError(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	was := start.Add(-time.Hour)
	d := &meetDoer{t: t, answers: map[string]any{"PUT /meet/v1/meetings/meeting-id/schedule": map[string]any{}}}
	m := &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR", Type: Scheduled, Start: &was}
	err := testService(d, testAccount(t)).Reschedule(context.Background(), m, start, start.Add(time.Hour), "", "UTC")
	if err == nil {
		t.Fatal("an answer with no meeting was taken")
	}
	if m.Start != &was || m.Type != Scheduled {
		t.Errorf("the meeting changed on an empty answer: %+v", m)
	}
}

// Proton keeps the zone a meeting was chosen in, and one sent without it would
// be a meeting with no zone, so none is refused before anything is sent.
func TestARescheduleNeedsAZone(t *testing.T) {
	d := &meetDoer{t: t}
	start := time.Now()
	m := &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR", Type: Scheduled}
	if err := testService(d, testAccount(t)).Reschedule(context.Background(), m, start, start, "", ""); err == nil {
		t.Error("a reschedule with no zone was sent")
	}
	if len(d.sent) != 0 {
		t.Errorf("sent %d requests", len(d.sent))
	}
}

// A meeting is known by its link name, which is what the link shows and what
// every command takes, and Proton's own ID is something no command takes.
func TestAMeetingIsWrittenByItsLinkName(t *testing.T) {
	raw, err := json.Marshal(Meeting{ID: "proton-id", Name: "6MCF0BSHBR"})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["id"] != "6MCF0BSHBR" {
		t.Errorf("JSON id %v, want the link name", out["id"])
	}
	if _, ok := out["name"]; ok {
		t.Error("JSON carries the link name twice")
	}
	if containsValue(out, "proton-id") {
		t.Error("Proton's own ID reaches JSON")
	}
}

func containsValue(m map[string]any, want string) bool {
	for _, v := range m {
		if v == want {
			return true
		}
	}
	return false
}

func TestDeleteNamesTheMeetingByItsLink(t *testing.T) {
	d := &meetDoer{t: t, answers: map[string]any{
		"DELETE /meet/v1/meetings/meeting-id": &proton.APIError{HTTPStatus: 422, Code: 2501},
	}}
	s := testService(d, testAccount(t))
	err := s.Delete(context.Background(), &Meeting{ID: "meeting-id", Name: "6MCF0BSHBR"})
	var nf *errs.NotFound
	if !errors.As(err, &nf) || nf.Ref != "6MCF0BSHBR" {
		t.Errorf("got %v, want a not-found by the link name", err)
	}
	// A meeting known only by Proton's ID is named by that.
	err = s.Delete(context.Background(), &Meeting{ID: "meeting-id"})
	if !errors.As(err, &nf) || nf.Ref != "meeting-id" {
		t.Errorf("got %v, want a not-found by the ID", err)
	}
}

// Proton's ID goes into the path as it is, so one that would change the route
// is refused before anything is sent.
func TestAnIDThatWouldChangeTheRouteIsRefused(t *testing.T) {
	d := &meetDoer{t: t}
	s := testService(d, testAccount(t))
	start := time.Now()
	for _, id := range []string{"", "a/b", "../meeting-id", ".", ".."} {
		m := &Meeting{ID: id, Name: "6MCF0BSHBR", SessionKey: nil}
		if err := s.Delete(context.Background(), m); err == nil {
			t.Errorf("Delete accepted ID %q", id)
		}
		if err := s.Reschedule(context.Background(), m, start, start, "", "UTC"); err == nil {
			t.Errorf("Reschedule accepted ID %q", id)
		}
	}
	if len(d.sent) != 0 {
		t.Errorf("sent %d requests for IDs that were refused", len(d.sent))
	}
}
