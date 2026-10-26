# proton meet meetings

Meetings people join by a link.

Every command under `proton meet meetings`, with the arguments and flags it takes. For these commands in use, see [the meet guide](README.md).

Holds `create`, `delete`, `get`, `list` and `update`.

## `create`

Create a meeting and print its link.

Without --start the meeting is instant: it has no time, and the link works until you delete it. With --start it is scheduled, and --rrule makes it repeat. --start takes a day and a time, as 2026-11-02T10:00, read in your zone.

Without --end or --duration a scheduled meeting lasts 30 minutes.

```
proton meet meetings create
```

```bash
proton meet meetings create --title 'Team sync'
proton meet meetings create --title 'Team sync' --start 2026-11-02T10:00 --duration 1h
proton meet meetings create --title 'Team sync' --start 2026-11-02T10:00 --rrule 'FREQ=WEEKLY;BYDAY=MO'
```

| Flag | Description |
| --- | --- |
| `--duration string` | Set how long it lasts (e.g. 15m, 1h, 1h30m) |
| `--end string` | Set the end: a day and a time |
| `--rrule string` | Set the recurrence rule, e.g. FREQ=WEEKLY;COUNT=10 |
| `--start string` | Set the start: a day and a time (2026-11-02T10:00, or full RFC 3339) |
| `--title string` | Set the title |

## `delete`

Delete a meeting.

Its link stops working for everyone you sent it to. A meeting that belongs to a calendar event is deleted all the same, and the event keeps a link that no longer works.

```
proton meet meetings delete REF
```

```bash
proton meet meetings delete 6MCF0BSHBR
proton meet meetings delete 6MCF0BSHBR --dry-run
```

## `get`

Show one meeting, link and all.

REF is the ID in the link after id-, such as 6MCF0BSHBR, or the whole link. The link appears here and when the meeting is made, never in a listing.

```
proton meet meetings get REF
```

```bash
proton meet meetings get 6MCF0BSHBR
proton meet meetings get 6MCF0BSHBR --output json
```

## `list`

List your upcoming meetings, soonest first.

Instant meetings are not listed, because Proton leaves them out of the list. `meetings get` still finds one by its ID or link. The links are not shown: `meetings get` shows one.

```
proton meet meetings list
```

```bash
proton meet meetings list
proton meet meetings list --output json
```

## `update`

Rename a meeting, or move it to another time.

Anything you do not mention is left alone. A meeting you move keeps its length unless --end or --duration says otherwise, and --rrule '' makes it happen once. The new times are read in your zone.

Only a scheduled or recurring meeting can be moved. One that belongs to a calendar event moves with the event: change the event instead.

```
proton meet meetings update REF
```

```bash
proton meet meetings update 6MCF0BSHBR --title 'Design review'
proton meet meetings update 6MCF0BSHBR --start 2026-11-03T14:00
proton meet meetings update 6MCF0BSHBR --start 2026-11-03T14:00 --duration 45m --rrule ''
```

| Flag | Description |
| --- | --- |
| `--duration string` | Replace how long it lasts (e.g. 15m, 1h, 1h30m) |
| `--end string` | Replace the end: a day and a time |
| `--rrule string` | Replace the recurrence rule, e.g. FREQ=WEEKLY;COUNT=10 |
| `--start string` | Replace the start: a day and a time (2026-11-02T10:00, or full RFC 3339) |
| `--title string` | Replace the title |

---

Every command also takes the [flags that work everywhere](../about/commands.md#flags-that-work-on-every-command).
