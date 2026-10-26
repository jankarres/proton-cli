# Meet

Video meetings in Proton Meet: create one and share its link, see what is coming up, move or rename a meeting, and delete it. Rooms stay open with the same link, and your personal room is the one you reach as `personal`.

This page is what people actually do. For every command and flag, see the reference: [meetings](meetings.md), [rooms](rooms.md).

## Create a meeting

```bash
proton meet meetings create --title 'Team sync'
proton meet meetings create --title 'Team sync' --start 2026-11-02T10:00 --duration 1h
```

Without `--start` the meeting is instant. It has no time, and its link works until you delete it.

With `--start` the meeting is scheduled. The time is read in your zone, and the meeting lasts 30 minutes unless `--end` or `--duration` says otherwise. `--rrule` makes it repeat:

```bash
proton meet meetings create --title 'Team sync' --start 2026-11-02T10:00 --rrule 'FREQ=WEEKLY;BYDAY=MO'
```

`create` prints the meeting with its link. Anyone with the link can join until you delete the meeting.

To put a link on a calendar event instead, see [Add a video call to an event](../calendar/README.md#add-a-video-call-to-an-event).

## See your meetings

```console
$ proton meet meetings list
ID          TITLE          START             TYPE
──────────  ─────────────  ────────────────  ─────────
6MCF0BSHBR  CLI Meet Test  2026-10-27 10:00  scheduled
865JZ8JMHG  Task5 Meet     2026-10-30 11:00  scheduled
2 meetings.
```

`list` shows your upcoming meetings, soonest first. Instant meetings are not in it, because Proton leaves them out, so keep the ID that `create` prints. `list` never shows a link. `get` shows one meeting with its link:

```bash
proton meet meetings get 6MCF0BSHBR
```

A meeting's ID is the part of its link after `id-`. Pass that ID or the whole link.

## Rename or move a meeting

```bash
proton meet meetings update 6MCF0BSHBR --title 'Design review'
proton meet meetings update 6MCF0BSHBR --start 2026-11-03T14:00
```

A meeting you move keeps its length unless you pass `--end` or `--duration`. `--rrule ''` makes a recurring meeting happen once.

Only a scheduled or recurring meeting can be moved. A meeting that belongs to a calendar event moves with the event, so change the event with `proton calendar events update`.

## Delete a meeting

```bash
proton meet meetings delete 6MCF0BSHBR
```

The link stops working for everyone you sent it to. `delete` asks before it acts, as [Dry runs and confirmations](../using/confirmations.md) explains.

A meeting that belongs to a calendar event is deleted all the same. The event keeps a link that no longer works.

## Create a room

```bash
proton meet rooms create --title 'Team room'
proton meet rooms list
```

A room has no time. Its link works until you delete the room. `list` never shows a link, and `proton meet rooms get` shows one room with its link.

Rooms and meetings are kept apart. A `meetings` command refuses a room and names the `rooms` command to use instead, and the other way round.

## Use your personal room

```console
$ proton meet rooms get personal
! Anyone with this link can join the room until it is deleted.
Link:   https://meet.proton.me/join/id-CSG6M2BY7G#pwd-…
ID:     CSG6M2BY7G
Title:  Personal meeting room
Type:   personal
Host:   you@proton.me
```

`personal` stands for your personal room wherever a room's ID goes. You can have one. When you have none, create it:

```bash
proton meet rooms create --personal
```

`proton meet rooms delete personal` deletes it.

## Give your personal room a new link

```bash
proton meet rooms rotate personal
```

The old link stops working for everyone you sent it to. `rotate` asks before it acts. The room is named Personal meeting room again. The link can be replaced once every 24 hours, as [Troubleshooting](../help/troubleshooting.md#your-personal-rooms-link-can-be-replaced-once-every-24-hours) explains.
