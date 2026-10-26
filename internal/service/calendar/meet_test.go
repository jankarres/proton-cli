package calendar

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/roman-16/proton-cli/internal/errs"
	"github.com/roman-16/proton-cli/internal/ical"
	"github.com/roman-16/proton-cli/internal/proton"
	"github.com/roman-16/proton-cli/internal/service/meet"
)

// fakeMeetings stands in for the Meet service: it hands out one meeting and
// records what it was asked to make, to look up and to delete. A gone service
// answers a lookup the way Proton answers for a meeting that was deleted.
type fakeMeetings struct {
	asked   []meet.NewMeeting
	deleted []string
	opened  []string
	gone    bool
	// refuse is what deleting a meeting answers, after the attempt is recorded.
	refuse error
}

const fakeMeetURL = "https://meet.proton.me/join/id-abcdefghij#pwd-Abc123Def456"

func (f *fakeMeetings) Create(_ context.Context, nm meet.NewMeeting) (*meet.Meeting, error) {
	f.asked = append(f.asked, nm)
	return &meet.Meeting{ID: "meeting1", Name: "abcdefghij", Title: nm.Title, URL: fakeMeetURL}, nil
}

func (f *fakeMeetings) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return f.refuse
}

func (f *fakeMeetings) ByLink(_ context.Context, name, password string) (*meet.Meeting, error) {
	f.opened = append(f.opened, name+"#"+password)
	if f.gone {
		return nil, &errs.NotFound{Kind: "meeting", Ref: name}
	}
	return &meet.Meeting{ID: "meeting1", Name: name, URL: fakeMeetURL}, nil
}

func (f *fakeMeetings) SealTitle(_ *meet.Meeting, title string) (string, error) {
	return "sealed:" + title, nil
}

// syncDoer answers the sync endpoint, keeping the batch it was sent, or refusing
// it with refuse.
type syncDoer struct {
	sent   map[string]any
	refuse error
}

func (d *syncDoer) Do(context.Context, proton.Request) (*proton.Response, error) { return nil, nil }

func (d *syncDoer) Decode(_ context.Context, r proton.Request, out any) error {
	if r.Method != "PUT" || r.Path != "/calendar/v1/cal1/events/sync" {
		return errors.New("unexpected request " + r.Method + " " + r.Path)
	}
	if d.refuse != nil {
		return d.refuse
	}
	raw, err := json.Marshal(r.Body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &d.sent); err != nil {
		return err
	}
	return json.Unmarshal([]byte(`{"Responses":[{"Index":0,"Response":{"Code":1000,"Event":{"ID":"event1"}}}]}`), out)
}

func meetService(t *testing.T, d proton.Doer, m Meetings) (*Service, *calKeys) {
	t.Helper()
	s := New(d, testKeys(nil))
	s.SetMeetings(m)
	ck := bookingKeys(t)
	ck.addressID = "address1"
	if _, err := s.unlocked.Do("cal1", func() (*calKeys, error) { return ck, nil }); err != nil {
		t.Fatal(err)
	}
	return s, ck
}

func meetInput() EventInput {
	return EventInput{
		Title: "Design review", Description: "Agenda first",
		Start:    ical.Timed(time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC), "UTC"),
		Duration: 30 * time.Minute, Meet: true,
	}
}

func TestCreateWithMeetWritesTheConference(t *testing.T) {
	d := &syncDoer{}
	m := &fakeMeetings{}
	s, ck := meetService(t, d, m)
	ctx := context.Background()

	res, err := s.EventCreate(ctx, "cal1", meetInput())
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "event1" || res.MeetURL != fakeMeetURL {
		t.Errorf("result = %+v", res)
	}
	if len(m.asked) != 1 {
		t.Fatalf("meetings asked for = %+v", m.asked)
	}
	asked := m.asked[0]
	if !asked.FromCalendar || asked.Type != meet.Scheduled || asked.AddressID != "address1" ||
		asked.Title != "Design review" || asked.Timezone != "" || asked.Start != nil || asked.End != nil {
		t.Errorf("new meeting = %+v", asked)
	}

	event := d.sent["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)
	conference, _ := event["VideoConferencingData"].(map[string]any)
	if conference["MeetEncryptedTitle"] != "sealed:Design review" {
		t.Errorf("VideoConferencingData = %+v", event["VideoConferencingData"])
	}
	cards, _ := json.Marshal(event["SharedEventContent"])
	var raw rawEvent
	if err := json.Unmarshal(cards, &raw.SharedEvents); err != nil {
		t.Fatal(err)
	}
	raw.SharedKeyPacket, _ = event["SharedKeyPacket"].(string)
	got := s.decrypt(ctx, ck, raw)
	if got.readErr != nil {
		t.Fatal(got.readErr)
	}
	v := got.model
	if v.ConferenceID != "abcdefghij" || v.ConferenceURL != fakeMeetURL ||
		v.ConferenceHost != "me@proton.me" || v.ConferenceProvider != ical.ConferenceProviderMeet {
		t.Errorf("conference = %q %q %q %q", v.ConferenceID, v.ConferenceURL, v.ConferenceHost, v.ConferenceProvider)
	}
	if v.Description != addJoinLine("Agenda first", fakeMeetURL) ||
		!strings.HasSuffix(v.Description, "Join Proton Meet: "+fakeMeetURL+"\n"+joinSeparator) {
		t.Errorf("description = %q", v.Description)
	}
}

func TestAFailedEventWriteDeletesItsMeeting(t *testing.T) {
	refused := errors.New("the calendar refused the event")
	m := &fakeMeetings{}
	s, _ := meetService(t, &syncDoer{refuse: refused}, m)

	_, err := s.EventCreate(context.Background(), "cal1", meetInput())
	if !errors.Is(err, refused) {
		t.Errorf("err = %v, want the sync's own error", err)
	}
	var left *MeetingLeftBehind
	if errors.As(err, &left) {
		t.Errorf("a meeting that was deleted is reported as left behind: %+v", left)
	}
	if len(m.deleted) != 1 || m.deleted[0] != "meeting1" {
		t.Errorf("deleted = %v", m.deleted)
	}
}

// undeletable is a Meet service whose meetings cannot be deleted again.
type undeletable struct{ fakeMeetings }

func (undeletable) Delete(context.Context, string) error { return errors.New("meet is down") }

func TestAMeetingThatCannotBeDeletedIsNamed(t *testing.T) {
	refused := errors.New("the calendar refused the event")
	s, _ := meetService(t, &syncDoer{refuse: refused}, &undeletable{})

	_, err := s.EventCreate(context.Background(), "cal1", meetInput())
	var left *MeetingLeftBehind
	if !errors.As(err, &left) || left.ID != "meeting1" || left.Name != "abcdefghij" || left.Cleanup == nil {
		t.Fatalf("err = %v, want the meeting named", err)
	}
	if !errors.Is(err, refused) || err.Error() != refused.Error() {
		t.Errorf("err = %v, want the sync's own error", err)
	}
}

func TestJoinLineRoundTrips(t *testing.T) {
	for _, desc := range []string{"", "Agenda first", "Line one\nline two\n"} {
		joined := addJoinLine(desc, fakeMeetURL)
		if !strings.Contains(joined, "\nJoin Proton Meet: "+fakeMeetURL+"\n") {
			t.Errorf("addJoinLine(%q) = %q", desc, joined)
		}
		if got := stripJoinLine(joined); got != desc {
			t.Errorf("stripJoinLine(addJoinLine(%q)) = %q", desc, got)
		}
	}
}

// ── changing an event's link ──

// storeDoer holds one stored event: it answers a read of it and of its series,
// and keeps every batch the sync endpoint is sent. A refused batch is kept too,
// so a test can see that it was the write that failed.
type storeDoer struct {
	event map[string]any
	// overrides are the series' separately edited occurrences, handed out with
	// it when the series is read.
	overrides []map[string]any
	batches   []map[string]any
	refuse    error
}

func (d *storeDoer) Do(context.Context, proton.Request) (*proton.Response, error) { return nil, nil }

func (d *storeDoer) Decode(_ context.Context, r proton.Request, out any) error {
	var answer any
	switch {
	case r.Method == "GET" && r.Path == "/calendar/v1/cal1/events/event1":
		answer = map[string]any{"Event": d.event}
	case r.Method == "GET" && r.Path == "/calendar/v1/cal1/events":
		answer = map[string]any{"Events": []any{d.event}}
	case r.Method == "GET" && r.Path == "/calendar/v1/events":
		events := []any{d.event}
		for _, o := range d.overrides {
			events = append(events, o)
		}
		answer = map[string]any{"Events": events}
	case r.Method == "PUT" && r.Path == "/calendar/v1/cal1/events/sync":
		raw, err := json.Marshal(r.Body)
		if err != nil {
			return err
		}
		var batch map[string]any
		if err := json.Unmarshal(raw, &batch); err != nil {
			return err
		}
		d.batches = append(d.batches, batch)
		if d.refuse != nil {
			return d.refuse
		}
		var responses []any
		for i := range batch["Events"].([]any) {
			responses = append(responses, map[string]any{
				"Index": i, "Response": map[string]any{"Code": 1000, "Event": map[string]any{"ID": "new" + strconv.Itoa(i)}},
			})
		}
		answer = map[string]any{"Responses": responses}
	default:
		return errors.New("unexpected request " + r.Method + " " + r.Path)
	}
	raw, err := json.Marshal(answer)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// store puts an event into the doer the way Proton keeps it: its cards sealed
// under the calendar's key.
func (d *storeDoer) store(t *testing.T, ck *calKeys, v ical.VEvent) {
	t.Helper()
	d.event = sealed(t, ck, "event1", v)
}

// storeOverride adds a separately edited occurrence of the stored series.
func (d *storeDoer) storeOverride(t *testing.T, ck *calKeys, id string, v ical.VEvent) {
	t.Helper()
	d.overrides = append(d.overrides, sealed(t, ck, id, v))
}

func sealed(t *testing.T, ck *calKeys, id string, v ical.VEvent) map[string]any {
	t.Helper()
	op, _, err := ck.createOp(eventBody{model: v})
	if err != nil {
		t.Fatal(err)
	}
	event := op["Event"].(map[string]any)
	raw, err := json.Marshal(map[string]any{
		"ID": id, "CalendarID": "cal1", "UID": v.UID, "IsOrganizer": 1,
		"SharedKeyPacket": event["SharedKeyPacket"], "SharedEvents": event["SharedEventContent"],
		"AttendeesEvents": event["AttendeesEventContent"],
	})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// written is what the last batch wrote: each event it carried, read back the way
// Proton would hand it out, beside the payload it was sent as.
func (d *storeDoer) written(t *testing.T, s *Service, ck *calKeys) ([]ical.VEvent, []map[string]any) {
	t.Helper()
	if len(d.batches) == 0 {
		t.Fatal("nothing was written")
	}
	var models []ical.VEvent
	var payloads []map[string]any
	for _, op := range d.batches[len(d.batches)-1]["Events"].([]any) {
		event, ok := op.(map[string]any)["Event"].(map[string]any)
		if !ok {
			continue
		}
		cards, _ := json.Marshal(event["SharedEventContent"])
		var raw rawEvent
		if err := json.Unmarshal(cards, &raw.SharedEvents); err != nil {
			t.Fatal(err)
		}
		raw.SharedKeyPacket, _ = event["SharedKeyPacket"].(string)
		if raw.SharedKeyPacket == "" {
			raw.SharedKeyPacket = d.keyPacketOf(op.(map[string]any)["ID"])
		}
		got := s.decrypt(context.Background(), ck, raw)
		if got.readErr != nil {
			t.Fatal(got.readErr)
		}
		models = append(models, got.model)
		payloads = append(payloads, event)
	}
	return models, payloads
}

// keyPacketOf is the key packet of the stored record an update names: an
// override's own, and the series' otherwise.
func (d *storeDoer) keyPacketOf(id any) string {
	for _, o := range d.overrides {
		if o["ID"] == id {
			packet, _ := o["SharedKeyPacket"].(string)
			return packet
		}
	}
	packet, _ := d.event["SharedKeyPacket"].(string)
	return packet
}

// plainEvent is an event with no link, as a person writes it.
func plainEvent() ical.VEvent {
	start := time.Date(2026, 10, 30, 11, 0, 0, 0, time.UTC)
	return ical.VEvent{
		UID: "uid-1", Summary: "Design review", Description: "Agenda first",
		Start: ical.Timed(start, "UTC"), End: ical.Timed(start.Add(30*time.Minute), "UTC"),
	}
}

// linkedEvent is the same event carrying a meeting, as the web client writes it.
func linkedEvent() ical.VEvent {
	return withMeeting(plainEvent(), &meet.Meeting{Name: "abcdefghij", URL: fakeMeetURL}, "me@proton.me")
}

func updating(t *testing.T, v ical.VEvent, m Meetings) (*Service, *calKeys, *storeDoer) {
	t.Helper()
	d := &storeDoer{}
	s, ck := meetService(t, d, m)
	d.store(t, ck, v)
	return s, ck, d
}

func pointer[T any](v T) *T { return &v }

func meetingTitle(event map[string]any) any {
	conference, _ := event["VideoConferencingData"].(map[string]any)
	return conference["MeetEncryptedTitle"]
}

func TestUpdateMeetAddsALink(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, plainEvent(), m)

	res, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	if res.MeetURL != fakeMeetURL {
		t.Errorf("result = %+v", res)
	}
	if len(m.asked) != 1 {
		t.Fatalf("meetings asked for = %+v", m.asked)
	}
	asked := m.asked[0]
	if !asked.FromCalendar || asked.Type != meet.Scheduled || asked.AddressID != "address1" ||
		asked.Title != "Design review" || asked.Timezone != "" {
		t.Errorf("new meeting = %+v", asked)
	}
	models, payloads := d.written(t, s, ck)
	v := models[0]
	if v.ConferenceID != "abcdefghij" || v.ConferenceURL != fakeMeetURL ||
		v.ConferenceHost != "me@proton.me" || v.ConferenceProvider != ical.ConferenceProviderMeet {
		t.Errorf("conference = %q %q %q %q", v.ConferenceID, v.ConferenceURL, v.ConferenceHost, v.ConferenceProvider)
	}
	if v.Description != addJoinLine("Agenda first", fakeMeetURL) {
		t.Errorf("description = %q", v.Description)
	}
	if meetingTitle(payloads[0]) != "sealed:Design review" {
		t.Errorf("VideoConferencingData = %+v", payloads[0]["VideoConferencingData"])
	}
}

func TestAFailedUpdateDeletesTheMeetingItMade(t *testing.T) {
	refused := errors.New("the calendar refused the event")
	m := &fakeMeetings{}
	s, _, d := updating(t, plainEvent(), m)
	d.refuse = refused

	_, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(true)})
	if !errors.Is(err, refused) {
		t.Errorf("err = %v, want the sync's own error", err)
	}
	if len(m.deleted) != 1 || m.deleted[0] != "meeting1" {
		t.Errorf("deleted = %v", m.deleted)
	}
}

func TestUpdateMeetOnAnEventWithOneRefuses(t *testing.T) {
	m := &fakeMeetings{}
	s, _, d := updating(t, linkedEvent(), m)

	_, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(true)})
	var problem *errs.Problem
	if !errors.As(err, &problem) || !strings.Contains(strings.Join(problem.Hints(), " "), "--no-meet") {
		t.Fatalf("err = %v, want a phrased refusal naming --no-meet", err)
	}
	if len(d.batches) != 0 || len(m.asked) != 0 {
		t.Errorf("a refused change still wrote %d batches and asked for %d meetings", len(d.batches), len(m.asked))
	}
}

func TestUpdateNoMeetRemovesEverything(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedEvent(), m)

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)}); err != nil {
		t.Fatal(err)
	}
	models, payloads := d.written(t, s, ck)
	v := models[0]
	if v.ConferenceID != "" || v.ConferenceURL != "" || v.ConferenceHost != "" || v.ConferenceProvider != "" {
		t.Errorf("conference left behind: %q %q %q %q", v.ConferenceID, v.ConferenceURL, v.ConferenceHost, v.ConferenceProvider)
	}
	if v.Description != "Agenda first" {
		t.Errorf("description = %q", v.Description)
	}
	if _, ok := payloads[0]["VideoConferencingData"]; ok {
		t.Errorf("VideoConferencingData = %+v", payloads[0]["VideoConferencingData"])
	}
	if len(m.asked) != 0 {
		t.Errorf("a meeting was made: %+v", m.asked)
	}
}

// Proton deletes the meeting of an event that no longer names it only some of
// the time: a series' recurring meeting outlived its last link in a live run. So
// once a whole event's loss of its link is stored, the meeting is looked up by
// the link the event had and deleted if it is still there.
func TestAWholeEventLosingItsLinkDeletesAMeetingProtonKept(t *testing.T) {
	for name, v := range map[string]func(*testing.T) ical.VEvent{
		"single": func(*testing.T) ical.VEvent { return linkedEvent() },
		"series": func(t *testing.T) ical.VEvent { return linkedSeries(t, true) },
	} {
		t.Run(name, func(t *testing.T) {
			m := &fakeMeetings{}
			s, _, d := updating(t, v(t), m)

			res, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)})
			if err != nil {
				t.Fatal(err)
			}
			if len(d.batches) != 1 {
				t.Fatalf("wrote %d batches", len(d.batches))
			}
			if len(m.opened) != 1 || m.opened[0] != "abcdefghij#Abc123Def456" {
				t.Errorf("meetings looked up = %v, want the event's old link", m.opened)
			}
			if len(m.deleted) != 1 || m.deleted[0] != "meeting1" {
				t.Errorf("deleted = %v, want the meeting the link named", m.deleted)
			}
			if res.MeetingKept != nil {
				t.Errorf("result = %+v, want the meeting deleted", res)
			}
		})
	}
}

// A meeting Proton already deleted, found gone by the lookup or by the
// deletion, is deleted as far as the person is concerned.
func TestAMeetingAlreadyGoneCountsAsDeleted(t *testing.T) {
	for name, m := range map[string]*fakeMeetings{
		"lookup":   {gone: true},
		"deletion": {refuse: &errs.NotFound{Kind: "meeting", Ref: "meeting1"}},
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := updating(t, linkedEvent(), m)

			res, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)})
			if err != nil {
				t.Fatal(err)
			}
			if m.gone && len(m.deleted) != 0 {
				t.Errorf("a meeting found gone was deleted again: %v", m.deleted)
			}
			if res.MeetingKept != nil {
				t.Errorf("result = %+v, want the meeting deleted", res)
			}
		})
	}
}

// A meeting that cannot be deleted does not undo the event's change, which is
// already stored. The result names the meeting by its link instead, so the
// person can be told what is left.
func TestAMeetingThatOutlivesItsLinkIsNamed(t *testing.T) {
	down := errors.New("meet is down")
	m := &fakeMeetings{refuse: down}
	s, ck, d := updating(t, linkedSeries(t, true), m)

	res, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)})
	if err != nil {
		t.Fatalf("err = %v, want the change to stand", err)
	}
	if models, _ := d.written(t, s, ck); hasConference(models[0]) {
		t.Errorf("the series kept its link")
	}
	if res.MeetingKept == nil || res.MeetingKept.Name != "abcdefghij" ||
		!errors.Is(res.MeetingKept.Err, down) {
		t.Errorf("result = %+v, want the meeting named with why it stayed", res)
	}
}

// An event whose change was refused still names its meeting, so nothing is
// looked up or deleted.
func TestALinkRemovalThatFailsToSaveLeavesTheMeeting(t *testing.T) {
	m := &fakeMeetings{}
	s, _, d := updating(t, linkedEvent(), m)
	d.refuse = errors.New("the calendar refused the event")

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)}); err == nil {
		t.Fatal("a refused change succeeded")
	}
	if len(m.opened)+len(m.deleted) != 0 {
		t.Errorf("the meeting was touched: %+v", m)
	}
}

func TestUpdateNoMeetOnAnEventWithoutOneRefuses(t *testing.T) {
	m := &fakeMeetings{}
	s, _, d := updating(t, plainEvent(), m)

	_, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)})
	var problem *errs.Problem
	if !errors.As(err, &problem) {
		t.Fatalf("err = %v, want a phrased refusal", err)
	}
	if len(d.batches) != 0 {
		t.Errorf("a refused change still wrote %d batches", len(d.batches))
	}
}

func TestARetitledEventRetitlesItsMeeting(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedEvent(), m)

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Title: pointer("Retro")}); err != nil {
		t.Fatal(err)
	}
	if len(m.opened) != 1 || m.opened[0] != "abcdefghij#Abc123Def456" {
		t.Errorf("meetings opened = %v, want the event's own link", m.opened)
	}
	models, payloads := d.written(t, s, ck)
	if meetingTitle(payloads[0]) != "sealed:Retro" {
		t.Errorf("VideoConferencingData = %+v", payloads[0]["VideoConferencingData"])
	}
	if models[0].ConferenceURL != fakeMeetURL || models[0].Summary != "Retro" {
		t.Errorf("event = %q %q", models[0].Summary, models[0].ConferenceURL)
	}
}

func TestAnEditThatKeepsTheTitleLeavesTheMeetingAlone(t *testing.T) {
	m := &fakeMeetings{}
	s, _, d := updating(t, linkedEvent(), m)

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1",
		EventPatch{Title: pointer("Design review"), Location: pointer("Room 3")}); err != nil {
		t.Fatal(err)
	}
	if len(m.opened) != 0 {
		t.Errorf("meetings opened = %v", m.opened)
	}
	if _, ok := d.batches[0]["Events"].([]any)[0].(map[string]any)["Event"].(map[string]any)["VideoConferencingData"]; ok {
		t.Error("an unchanged title was sent to the meeting")
	}
}

func TestARetitleWhoseMeetingIsGoneStillSavesAndSaysSo(t *testing.T) {
	var records strings.Builder
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&records, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	m := &fakeMeetings{gone: true}
	s, ck, d := updating(t, linkedEvent(), m)

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Title: pointer("Retro")}); err != nil {
		t.Fatal(err)
	}
	models, payloads := d.written(t, s, ck)
	if models[0].Summary != "Retro" {
		t.Errorf("title = %q", models[0].Summary)
	}
	if _, ok := payloads[0]["VideoConferencingData"]; ok {
		t.Errorf("VideoConferencingData = %+v", payloads[0]["VideoConferencingData"])
	}
	log := records.String()
	if !strings.Contains(log, "level=WARN") || !strings.Contains(log, "kept its old title") ||
		!strings.Contains(log, "reason=") {
		t.Errorf("the miss was not recorded: %s", log)
	}
	if strings.Contains(log, "abcdefghij") || strings.Contains(log, "Abc123Def456") {
		t.Errorf("the link reached the log: %s", log)
	}
}

// A split writes the series up to the occurrence back unchanged and the rest as
// a new series, and both keep the link, as the web client's FUTURE save does:
// deleteFutureRecurrence only shortens the original's rule, and
// createFutureRecurrence copies the edited event, conference included. A new
// title travels with the rest, which is the half the edit was made on.
func TestASeriesKeepsItsLinkWhenSplit(t *testing.T) {
	readingIn(t, time.UTC)
	v := linkedEvent()
	v.RRule = "FREQ=DAILY;COUNT=5"
	m := &fakeMeetings{}
	s, ck, d := updating(t, v, m)

	if _, err := s.SeriesSplit(context.Background(), "cal1", "event1", "2026-11-01T11:00",
		EventPatch{Title: pointer("Retro")}); err != nil {
		t.Fatal(err)
	}
	models, payloads := d.written(t, s, ck)
	if len(models) != 2 {
		t.Fatalf("wrote %d events, want the two halves", len(models))
	}
	for i, half := range models {
		if half.ConferenceID != "abcdefghij" || half.ConferenceURL != fakeMeetURL ||
			half.Description != addJoinLine("Agenda first", fakeMeetURL) {
			t.Errorf("half %d lost its link: %q %q %q", i, half.ConferenceID, half.ConferenceURL, half.Description)
		}
	}
	if models[0].Summary != "Design review" || meetingTitle(payloads[0]) != nil {
		t.Errorf("the first half = %q, %v", models[0].Summary, meetingTitle(payloads[0]))
	}
	if models[1].Summary != "Retro" || meetingTitle(payloads[1]) != "sealed:Retro" {
		t.Errorf("the rest = %q, %v", models[1].Summary, meetingTitle(payloads[1]))
	}
}

// linkedSeries is a daily series carrying a meeting, read in UTC so that an
// occurrence reference written here names the instant it says.
func linkedSeries(t *testing.T, linked bool) ical.VEvent {
	t.Helper()
	readingIn(t, time.UTC)
	v := plainEvent()
	if linked {
		v = linkedEvent()
	}
	v.RRule = "FREQ=DAILY;COUNT=5"
	return v
}

// One occurrence can gain or lose a link of its own: the web client's event
// form offers the link row whatever the scope (EventForm.tsx renders
// RowVideoConference for any event whose shared data can be edited), and its
// SINGLE save writes the form's event, conference and all, as the replacement
// for that occurrence alone (getSaveRecurringEventActions.ts, createSingleRecurrence
// and updateSingleRecurrence). The series is written back as it was.

func TestAnOccurrenceCanLoseTheLinkTheSeriesKeeps(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedSeries(t, true), m)

	res, err := s.OccurrenceUpdate(context.Background(), "cal1", "event1", "2026-11-01T11:00", EventPatch{Meet: pointer(false)})
	if err != nil {
		t.Fatal(err)
	}
	if res.MeetURL != "" {
		t.Errorf("result = %+v", res)
	}
	models, payloads := d.written(t, s, ck)
	if len(models) != 2 {
		t.Fatalf("wrote %d events, want the series and the occurrence", len(models))
	}
	series, occurrence := models[0], models[1]
	if series.ConferenceURL != fakeMeetURL || series.Description != addJoinLine("Agenda first", fakeMeetURL) {
		t.Errorf("the series lost its link: %q %q", series.ConferenceURL, series.Description)
	}
	if occurrence.RecurrenceID == nil || hasConference(occurrence) || occurrence.Description != "Agenda first" {
		t.Errorf("the occurrence = %v %q %q", occurrence.RecurrenceID, occurrence.ConferenceURL, occurrence.Description)
	}
	if meetingTitle(payloads[1]) != nil || len(m.asked)+len(m.opened)+len(m.deleted) != 0 {
		t.Errorf("the meeting was touched: %v, %+v", meetingTitle(payloads[1]), m)
	}
}

func TestAnOccurrenceCanGainALinkOfItsOwn(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedSeries(t, false), m)

	res, err := s.OccurrenceUpdate(context.Background(), "cal1", "event1", "2026-11-01T11:00", EventPatch{Meet: pointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	if res.MeetURL != fakeMeetURL || len(m.asked) != 1 {
		t.Errorf("result = %+v, meetings asked for = %+v", res, m.asked)
	}
	models, payloads := d.written(t, s, ck)
	if hasConference(models[0]) {
		t.Errorf("the series was given the link: %q", models[0].ConferenceURL)
	}
	if models[1].ConferenceURL != fakeMeetURL || meetingTitle(payloads[1]) != "sealed:Design review" {
		t.Errorf("the occurrence = %q, %v", models[1].ConferenceURL, meetingTitle(payloads[1]))
	}
}

func TestAnOccurrenceWithTheSeriesLinkRefusesAnother(t *testing.T) {
	m := &fakeMeetings{}
	s, _, d := updating(t, linkedSeries(t, true), m)

	_, err := s.OccurrenceUpdate(context.Background(), "cal1", "event1", "2026-11-01T11:00", EventPatch{Meet: pointer(true)})
	var problem *errs.Problem
	if !errors.As(err, &problem) || len(d.batches) != 0 || len(m.asked) != 0 {
		t.Errorf("err = %v, batches = %d, meetings asked for = %d", err, len(d.batches), len(m.asked))
	}
}

// A retitled occurrence carries the new title to the meeting it shares with the
// series, as the web client does: useProtonMeetIntegration seals any change of
// the form's title, modelToVideoConferenceProperties puts it on the event, and
// the SINGLE save keeps it (updateSingleRecurrence and createSingleRecurrence
// drop only the rule and the exclusions).
func TestARetitledOccurrenceRetitlesTheMeetingLikeTheWebClient(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedSeries(t, true), m)

	if _, err := s.OccurrenceUpdate(context.Background(), "cal1", "event1", "2026-11-01T11:00",
		EventPatch{Title: pointer("Retro")}); err != nil {
		t.Fatal(err)
	}
	models, payloads := d.written(t, s, ck)
	if models[1].Summary != "Retro" || meetingTitle(payloads[1]) != "sealed:Retro" {
		t.Errorf("the occurrence = %q, %v", models[1].Summary, meetingTitle(payloads[1]))
	}
	if meetingTitle(payloads[0]) != nil {
		t.Errorf("the series carried a title for the meeting: %v", meetingTitle(payloads[0]))
	}
}

// Taking the link away from the rest of a series leaves it on the first half,
// which still names the meeting.
func TestASplitWithoutTheLinkKeepsItOnTheFirstHalf(t *testing.T) {
	m := &fakeMeetings{}
	s, ck, d := updating(t, linkedSeries(t, true), m)

	if _, err := s.SeriesSplit(context.Background(), "cal1", "event1", "2026-11-01T11:00",
		EventPatch{Meet: pointer(false)}); err != nil {
		t.Fatal(err)
	}
	models, _ := d.written(t, s, ck)
	if models[0].ConferenceURL != fakeMeetURL {
		t.Errorf("the first half lost its link")
	}
	if hasConference(models[1]) || models[1].Description != "Agenda first" {
		t.Errorf("the rest kept its link: %q %q", models[1].ConferenceURL, models[1].Description)
	}
	if len(m.asked)+len(m.opened)+len(m.deleted) != 0 {
		t.Errorf("the meeting was touched: %+v", m)
	}
}

// ── reading an event's link ──

func TestGetShowsTheMeetLink(t *testing.T) {
	s, _, _ := updating(t, linkedEvent(), &fakeMeetings{})

	ev, err := s.EventGet(context.Background(), "cal1", "event1", "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.MeetURL != fakeMeetURL {
		t.Errorf("MeetURL = %q, want %q", ev.MeetURL, fakeMeetURL)
	}
	if ev.Description != "Agenda first" {
		t.Errorf("description = %q, want the person's text without the join block", ev.Description)
	}
}

func TestListNeverCarriesTheMeetLink(t *testing.T) {
	s, ck, d := updating(t, linkedEvent(), &fakeMeetings{})
	ctx := context.Background()
	raw, err := s.rawEvent(ctx, "cal1", "event1")
	if err != nil {
		t.Fatal(err)
	}
	_ = d
	e := s.decrypt(ctx, ck, raw)
	if e.readErr != nil {
		t.Fatal(e.readErr)
	}
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	rows := expand([]stored{e}, ical.Days(day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)))
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].MeetURL != "" || strings.Contains(rows[0].Description, "meet.proton.me") ||
		strings.Contains(rows[0].Description, joinSeparator) {
		t.Errorf("a listing row carries the link: %+v", rows[0])
	}
	if rows[0].Description != "Agenda first" {
		t.Errorf("description = %q", rows[0].Description)
	}
}

// An event carrying the join block without the conference properties, the
// state an older edit left behind, shows its description as stored to events
// get alone. A listing and a search, including one answered from an index an
// older binary built, never carry the block, because no listing carries a link.
func TestAJoinLineWithoutPropertiesShowsOnlyInGet(t *testing.T) {
	readingIn(t, time.UTC)
	v := plainEvent()
	v.Description = addJoinLine("Agenda first", fakeMeetURL)
	s, ck, _ := updating(t, v, &fakeMeetings{})
	ctx := context.Background()

	ev, err := s.EventGet(ctx, "cal1", "event1", "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.Description != v.Description || ev.MeetURL != "" {
		t.Errorf("get: description = %q, MeetURL = %q, want the stored description and no link", ev.Description, ev.MeetURL)
	}

	raw, err := s.rawEvent(ctx, "cal1", "event1")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	listed := expand([]stored{s.decrypt(ctx, ck, raw)}, ical.Days(day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)))
	found, err := s.EventsSearch(ctx, []string{"cal1"}, SearchWindow(), "agenda")
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || len(found) != 1 {
		t.Fatalf("listed %d, found %d, want one each", len(listed), len(found))
	}
	for name, row := range map[string]Event{"list": listed[0], "search": found[0]} {
		if row.Description != "Agenda first" || strings.Contains(row.Description, "meet.proton.me") {
			t.Errorf("%s: description = %q, want the text without the join block", name, row.Description)
		}
	}
}

// ── a description edit keeps the join line ──

func joinBlocks(desc string) int { return len(joinBlock.FindAllString(desc, -1)) }

func TestADescriptionEditKeepsTheJoinLine(t *testing.T) {
	s, ck, d := updating(t, linkedEvent(), &fakeMeetings{})

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Description: pointer("New agenda")}); err != nil {
		t.Fatal(err)
	}
	models, _ := d.written(t, s, ck)
	v := models[0]
	if v.Description != addJoinLine("New agenda", fakeMeetURL) || joinBlocks(v.Description) != 1 {
		t.Errorf("description = %q, want the new text and one join block", v.Description)
	}
	if v.ConferenceURL != fakeMeetURL {
		t.Errorf("conference lost: %q", v.ConferenceURL)
	}
}

func TestEveryUpdatePathLeavesTheRightNumberOfJoinBlocks(t *testing.T) {
	cases := []struct {
		name   string
		linked bool
		patch  EventPatch
		blocks int
	}{
		{"title only", true, EventPatch{Title: pointer("Renamed")}, 1},
		{"description that already carries a block", true, EventPatch{Description: pointer(addJoinLine("Again", fakeMeetURL))}, 1},
		{"description with --no-meet", true, EventPatch{Description: pointer("Plain"), Meet: pointer(false)}, 0},
		{"no-meet", true, EventPatch{Meet: pointer(false)}, 0},
		{"meet on a plain event", false, EventPatch{Meet: pointer(true)}, 1},
		{"description with --meet", false, EventPatch{Description: pointer("Fresh"), Meet: pointer(true)}, 1},
		{"description on a plain event", false, EventPatch{Description: pointer("Fresh")}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := plainEvent()
			if tc.linked {
				v = linkedEvent()
			}
			s, ck, d := updating(t, v, &fakeMeetings{})
			if _, err := s.EventUpdate(context.Background(), "cal1", "event1", tc.patch); err != nil {
				t.Fatal(err)
			}
			models, _ := d.written(t, s, ck)
			if got := joinBlocks(models[0].Description); got != tc.blocks {
				t.Errorf("join blocks = %d, want %d in %q", got, tc.blocks, models[0].Description)
			}
		})
	}
}

// ── a whole-series link change reaches the edited occurrences ──

// editedOccurrence is the series' third occurrence, edited on its own: it has a
// title of its own and otherwise carries what the series carried.
func editedOccurrence(t *testing.T, series ical.VEvent) ical.VEvent {
	t.Helper()
	v := series
	v.Summary = "Design review, moved room"
	return v.AsOverride(series, ical.Timed(time.Date(2026, 11, 1, 11, 0, 0, 0, time.UTC), "UTC"))
}

// A link added to or taken from a whole series is carried to the occurrences
// edited on their own, in the same batch, as the web client's "all events" save
// merges the changed conference properties into every single edit
// (getUpdateSingleEditMergeVevent and handleConferenceDataInMergedVeventIfNeeded
// in getSaveEventActionsHelpers.ts). Their own edits stay.
func TestASeriesLinkChangeReachesItsEditedOccurrences(t *testing.T) {
	for _, linked := range []bool{true, false} {
		t.Run(map[bool]string{true: "no-meet", false: "meet"}[linked], func(t *testing.T) {
			m := &fakeMeetings{}
			series := linkedSeries(t, linked)
			s, ck, d := updating(t, series, m)
			d.storeOverride(t, ck, "override1", editedOccurrence(t, series))

			if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(!linked)}); err != nil {
				t.Fatal(err)
			}
			models, _ := d.written(t, s, ck)
			if len(models) != 2 {
				t.Fatalf("wrote %d events, want the series and its edited occurrence", len(models))
			}
			occurrence := models[1]
			if occurrence.RecurrenceID == nil || occurrence.Summary != "Design review, moved room" {
				t.Errorf("the occurrence lost its own edit: %v %q", occurrence.RecurrenceID, occurrence.Summary)
			}
			if hasMeetLink(occurrence) == linked || joinBlocks(occurrence.Description) != map[bool]int{true: 0, false: 1}[linked] {
				t.Errorf("the occurrence = %q %q, want it to follow the series", occurrence.ConferenceURL, occurrence.Description)
			}
			if !linked && (occurrence.ConferenceURL != fakeMeetURL || occurrence.ConferenceProvider != ical.ConferenceProviderMeet) {
				t.Errorf("the occurrence carries %q %q, want the series' meeting", occurrence.ConferenceURL, occurrence.ConferenceProvider)
			}
			if occurrence.Sequence < models[0].Sequence {
				t.Errorf("the occurrence's sequence %d is older than the series' %d", occurrence.Sequence, models[0].Sequence)
			}
		})
	}
}

// An edited occurrence that already went without the link is left out of the
// batch when the series loses its own: nothing about it changes.
func TestASeriesLosingItsLinkLeavesAnOccurrenceWithoutOneAlone(t *testing.T) {
	series := linkedSeries(t, true)
	s, ck, d := updating(t, series, &fakeMeetings{})
	d.storeOverride(t, ck, "override1", withoutMeeting(editedOccurrence(t, series)))

	if _, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(false)}); err != nil {
		t.Fatal(err)
	}
	if models, _ := d.written(t, s, ck); len(models) != 1 {
		t.Errorf("wrote %d events, want the series alone", len(models))
	}
}

// A description still carrying the join block of an earlier, dead link ends up
// with the new link's block alone.
func TestANewLinkReplacesADeadJoinBlock(t *testing.T) {
	v := plainEvent()
	v.Description = addJoinLine("Agenda first", "https://meet.proton.me/join/id-deadlinkxx#pwd-Old123Old456")
	v = withMeeting(v, &meet.Meeting{Name: "abcdefghij", URL: fakeMeetURL}, "me@proton.me")
	if v.Description != addJoinLine("Agenda first", fakeMeetURL) || joinBlocks(v.Description) != 1 {
		t.Errorf("description = %q, want the text and the new block alone", v.Description)
	}
}

func TestNoJoinBlockWithoutALink(t *testing.T) {
	if got := addJoinLine("Agenda first", ""); got != "Agenda first" {
		t.Errorf("addJoinLine without a link = %q", got)
	}
}

// An event carrying another service's call is refused a Proton Meet link, with a
// hint saying how to get one.
func TestAnotherServicesLinkRefusesMeetWithAHint(t *testing.T) {
	v := plainEvent()
	v.ConferenceID, v.ConferenceURL, v.ConferenceProvider = "123456789", "https://zoom.us/j/123456789", "1"
	m := &fakeMeetings{}
	s, _, d := updating(t, v, m)

	_, err := s.EventUpdate(context.Background(), "cal1", "event1", EventPatch{Meet: pointer(true)})
	var problem *errs.Problem
	if !errors.As(err, &problem) || !strings.Contains(problem.Error(), "another service") {
		t.Fatalf("err = %v, want the other-service refusal", err)
	}
	if hints := strings.Join(problem.Hints(), " "); !strings.Contains(hints, "--meet") {
		t.Errorf("hints = %q, want one naming --meet", hints)
	}
	if len(d.batches) != 0 || len(m.asked) != 0 {
		t.Errorf("a refused change still wrote %d batches and asked for %d meetings", len(d.batches), len(m.asked))
	}
}

// An event the account was invited to belongs to its organizer, and so does its
// meeting: the web client offers the link row only on events whose shared data
// the account can edit (canEditSharedEventData). Every path that takes --meet or
// --no-meet refuses on one before anything is made, looked up, deleted or
// written.
func TestAnInvitationsLinkIsLeftToItsOrganizer(t *testing.T) {
	for _, tt := range []struct {
		name   string
		update func(*Service, EventPatch) error
	}{
		{"whole event", func(s *Service, p EventPatch) error {
			_, err := s.EventUpdate(context.Background(), "cal1", "event1", p)
			return err
		}},
		{"occurrence", func(s *Service, p EventPatch) error {
			_, err := s.OccurrenceUpdate(context.Background(), "cal1", "event1", "2026-11-01T11:00", p)
			return err
		}},
		{"onwards", func(s *Service, p EventPatch) error {
			_, err := s.SeriesSplit(context.Background(), "cal1", "event1", "2026-11-01T11:00", p)
			return err
		}},
	} {
		for _, add := range []bool{true, false} {
			t.Run(tt.name+map[bool]string{true: " --meet", false: " --no-meet"}[add], func(t *testing.T) {
				m := &fakeMeetings{}
				s, _, d := updating(t, linkedSeries(t, !add), m)
				d.event["IsOrganizer"] = 0

				err := tt.update(s, EventPatch{Meet: pointer(add)})
				var problem *errs.Problem
				if !errors.As(err, &problem) || len(problem.Hints()) == 0 {
					t.Fatalf("err = %v, want a phrased refusal with a hint", err)
				}
				if strings.Contains(err.Error(), "Design review") {
					t.Errorf("err = %q names the event", err)
				}
				if len(d.batches)+len(m.asked)+len(m.opened)+len(m.deleted) != 0 {
					t.Errorf("a refused change still wrote %d batches and touched the meeting: %+v", len(d.batches), m)
				}
			})
		}
	}
}

// The change mail is worked out after the meeting was dealt with, and failing it
// does not hide what became of the meeting: the result comes back beside the
// error, so a kept meeting can still be named.
func TestAFailedChangeMailStillNamesAKeptMeeting(t *testing.T) {
	v := linkedEvent()
	v.Organizer = "me@proton.me"
	v.Attendees = []ical.Attendee{{Email: "guest@example.com", PartStat: "NEEDS-ACTION"}}
	m := &fakeMeetings{refuse: errors.New("meet is down")}
	s, _, _ := updating(t, v, m)

	res, err := s.EventUpdate(context.Background(), "cal1", "event1",
		EventPatch{Meet: pointer(false), Duration: pointer(time.Hour)})
	if err == nil {
		t.Fatal("want the change mail's error")
	}
	if res == nil || res.MeetingKept == nil || res.MeetingKept.Name != "abcdefghij" {
		t.Errorf("result = %+v, want the kept meeting named beside the error", res)
	}
}
