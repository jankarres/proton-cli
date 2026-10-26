package meet

import (
	"cmp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/roman-16/proton-cli/internal/cli/kit"
	"github.com/roman-16/proton-cli/internal/errs"
	meetsvc "github.com/roman-16/proton-cli/internal/service/meet"
	"github.com/roman-16/proton-cli/internal/ui"
)

// personal is the reference that names the account's personal room.
const personal = "personal"

// roomWarning is linkWarning for a room, said wherever a room's link is shown.
const roomWarning = "Anyone with this link can join the room until it is deleted."

func roomsCmd() *cobra.Command {
	c := &cobra.Command{Use: "rooms", Short: "Rooms that stay open, and your personal room"}
	c.AddCommand(roomsCreateCmd(), roomsDeleteCmd(), roomsGetCmd(), roomsListCmd(), roomsRotateCmd(), roomsUpdateCmd())
	return c
}

// isRoom says whether a meeting belongs among the rooms: the personal room and
// the rooms that stay open, as the dashboard's My rooms tab shows them.
func isRoom(t meetsvc.MeetingType) bool {
	return t == meetsvc.Personal || t == meetsvc.Permanent
}

// collection is one of the two collections the meetings an account holds are
// split into, with what a command needs to send somebody to the other one.
type collection struct {
	name    string
	noun    string
	holds   func(meetsvc.MeetingType) bool
	isOther string
	// updateFlag is a flag its update needs to have something to change, so
	// a hint sending somebody there is a command that runs.
	updateFlag string
}

var (
	meetingsCollection = collection{
		name: "meetings", noun: "meeting", holds: isMeeting, isOther: "room", updateFlag: "--title 'Team sync'",
	}
	roomsCollection = collection{
		name: "rooms", noun: "room", holds: isRoom, isOther: "meeting", updateFlag: "--title 'Team room'",
	}
)

// command is the line that runs verb on ref in this collection, with the flag
// an update cannot do without.
func (k collection) command(verb, ref string) string {
	line := kit.Program + " meet " + k.name + " " + verb + " " + ref
	if verb == "update" {
		line += " " + k.updateFlag
	}
	return line
}

// other is the collection a meeting this one refuses belongs to.
func (k collection) other() collection {
	if k.name == meetingsCollection.name {
		return roomsCollection
	}
	return meetingsCollection
}

// resolveIn finds the meeting the command's reference names and refuses one
// that belongs to the other collection, with the command that takes it.
func resolveIn(c *kit.Invocation, k collection) (*meetsvc.Meeting, error) {
	m, err := c.App.Meet.Resolve(c.Ctx, c.Args[0])
	if err != nil {
		return nil, err
	}
	if err := k.admits(m, c.Cmd.Name()); err != nil {
		return nil, err
	}
	return m, nil
}

// admits refuses a meeting this collection does not hold.
func (k collection) admits(m *meetsvc.Meeting, verb string) error {
	if k.holds(m.Type) {
		return nil
	}
	return errs.Naming(m.Title, kit.Fail("\"%s\" is a %s, not a %s.", m.Title, k.isOther, k.noun).
		Hint(k.other().command(verb, m.Name)))
}

func roomColumns() []ui.Column[meetingRow] {
	return []ui.Column[meetingRow]{
		{Header: "ID", ID: true, Cell: func(r meetingRow) string { return r.ID }},
		{Header: "TITLE", Flex: true, Cell: func(r meetingRow) string { return r.Title }},
		{Header: "TYPE", Cell: func(r meetingRow) string { return r.Type.String() }},
	}
}

func roomFields(m *meetsvc.Meeting) []ui.Field {
	return []ui.Field{
		{Label: "Link", Value: m.URL},
		{Label: "ID", Value: m.Name, ID: true},
		{Label: "Title", Value: m.Title},
		{Label: "Type", Value: m.Type.String()},
		{Label: "Host", Value: m.Host},
	}
}

// showRoom writes a room with its link, and the warning that goes with it.
func showRoom(c *kit.Invocation, m *meetsvc.Meeting) error {
	c.Warn("%s", roomWarning)
	return kit.Show(c, ui.RecordSpec{Object: m, Fields: roomFields(m)})
}

func roomsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List your rooms",
		Long: "List your rooms, the personal room first.\n\n" +
			"The links are not shown: `rooms get` shows one.",
		RunE: kit.Run(nil, func(c *kit.Invocation) error {
			all, err := c.App.Meet.Upcoming(c.Ctx)
			if err != nil {
				return err
			}
			return listRooms(c, all)
		}),
	}
}

// listRooms answers with the rooms among everything upcoming, the personal room
// first and the others by title.
func listRooms(c *kit.Invocation, all []meetsvc.Meeting) error {
	rows := make([]meetingRow, 0, len(all))
	for _, m := range all {
		if isRoom(m.Type) {
			rows = append(rows, rowOf(m))
		}
	}
	slices.SortStableFunc(rows, func(a, b meetingRow) int {
		if ap, bp := a.Type == meetsvc.Personal, b.Type == meetsvc.Personal; ap != bp {
			if ap {
				return -1
			}
			return 1
		}
		return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
	return kit.List(c, ui.TableSpec[meetingRow]{
		Noun: "rooms", Columns: roomColumns(), Total: ui.Unknown, Page: ui.Unpaged,
	}, rows)
}

func roomsGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get REF",
		Short: "Show one room, link and all",
		Long: "Show one room, link and all.\n\n" +
			"REF is the ID in the link after id-, such as E87W2DAJ60, the whole link, or\n" +
			"personal for your personal room. The link appears here and when the room is\n" +
			"made, never in a listing.",
		RunE: kit.Run([]kit.Step{kit.StepExpand, judgeRef}, func(c *kit.Invocation) error {
			m, err := resolveIn(c, roomsCollection)
			if err != nil {
				return err
			}
			return showRoom(c, m)
		}),
	}
}

func roomsCreateCmd() *cobra.Command {
	var title string
	var own bool
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a room and print its link",
		Long: "Create a room and print its link.\n\n" +
			"A room has no time: its link works until you delete it. --personal creates\n" +
			"your personal room instead, which you reach as personal. You can have one.",
		RunE: kit.Run([]kit.Step{func(c *kit.Invocation) error {
			if !own && strings.TrimSpace(title) == "" {
				return kit.Fail("A room needs a title.").Hint("--title 'Team room'", "--personal")
			}
			return nil
		}}, func(c *kit.Invocation) error {
			spec := ui.ResultSpec{Action: ui.Created, Kind: "rooms", Count: 1, Name: title, AnswerFollows: true}
			if own {
				spec.Kind, spec.Name = "", "your personal room"
			}
			var made *meetsvc.Meeting
			if err := kit.Mutate(c, spec, func() error {
				var err error
				if own {
					made, err = c.App.Meet.CreatePersonal(c.Ctx)
				} else {
					made, err = c.App.Meet.Create(c.Ctx, meetsvc.NewMeeting{Title: title, Type: meetsvc.Permanent})
				}
				return err
			}); err != nil {
				return err
			}
			if made == nil || c.App.DryRun {
				return nil
			}
			return showRoom(c, made)
		}),
	}
	c.Flags().StringVar(&title, "title", "", "Set the title")
	c.Flags().BoolVar(&own, "personal", false, "Create your personal room")
	kit.Exclusive(c, "title", "personal")
	return c
}

func roomsUpdateCmd() *cobra.Command {
	var title string
	c := &cobra.Command{
		Use:   "update REF",
		Short: "Rename a room",
		Long: "Rename a room.\n\n" +
			"REF is the room's ID, its whole link, or personal for your personal room. The\n" +
			"link stays as it is.",
		RunE: kit.Run([]kit.Step{func(c *kit.Invocation) error {
			if !c.Changed("title") {
				return kit.Fail("Nothing to change.").Hint("--title 'Team room'")
			}
			if strings.TrimSpace(title) == "" {
				return kit.Fail("A room needs a title.").Hint("--title 'Team room'")
			}
			return nil
		}, kit.StepExpand, judgeRef}, func(c *kit.Invocation) error {
			m, err := resolveIn(c, roomsCollection)
			if err != nil {
				return err
			}
			return kit.Mutate(c, ui.ResultSpec{
				Action: ui.Updated, Kind: "rooms", Count: 1, Name: title, IDs: []string{m.Name},
			}, func() error {
				return c.App.Meet.Rename(c.Ctx, m, title)
			})
		}),
	}
	c.Flags().StringVar(&title, "title", "", "Replace the title")
	return c
}

func roomsDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete REF",
		Short: "Delete a room",
		Long: "Delete a room.\n\n" +
			"Its link stops working for everyone you sent it to. Pass personal to delete\n" +
			"your personal room, and `rooms create --personal` to make a new one.",
		RunE: kit.Run([]kit.Step{kit.StepExpand, judgeRef}, func(c *kit.Invocation) error {
			m, err := resolveIn(c, roomsCollection)
			if err != nil {
				return err
			}
			return kit.Mutate(c, ui.ResultSpec{
				Action: ui.Deleted, Kind: "rooms", Count: 1, Name: m.Title, IDs: []string{m.Name},
			}, func() error {
				return c.App.Meet.Delete(c.Ctx, m)
			})
		}),
	}
}

func roomsRotateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate REF",
		Short: "Give your personal room a new link",
		Long: "Give your personal room a new link, and print it.\n\n" +
			"REF is personal: no other room can get a new link. The old link stops\n" +
			"working for everyone you sent it to. The room is named Personal meeting room\n" +
			"again. Its link can be replaced once every 24 hours.",
		RunE: kit.Run([]kit.Step{func(c *kit.Invocation) error {
			if c.Args[0] != personal {
				return kit.Fail("Only your personal room can get a new link.").
					Hint(roomsCollection.command("rotate", personal))
			}
			return nil
		}}, func(c *kit.Invocation) error {
			m, err := c.App.Meet.Resolve(c.Ctx, personal)
			if err != nil {
				return err
			}
			var rotated *meetsvc.Meeting
			if err := kit.Mutate(c, ui.ResultSpec{
				Action: ui.Rotated, Count: 1, Name: "the link of your personal room", IDs: []string{m.Name},
				AnswerFollows: true,
			}, func() error {
				var err error
				rotated, err = c.App.Meet.RotatePersonal(c.Ctx)
				return err
			}); err != nil {
				return err
			}
			if rotated == nil || c.App.DryRun {
				return nil
			}
			return showRoom(c, rotated)
		}),
	}
}
