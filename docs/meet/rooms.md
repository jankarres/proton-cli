# proton meet rooms

Rooms that stay open, and your personal room.

Every command under `proton meet rooms`, with the arguments and flags it takes. For these commands in use, see [the meet guide](README.md).

Holds `create`, `delete`, `get`, `list`, `rotate` and `update`.

## `create`

Create a room and print its link.

A room has no time: its link works until you delete it. --personal creates your personal room instead, which you reach as personal. You can have one.

```
proton meet rooms create
```

```bash
proton meet rooms create --title 'Team room'
proton meet rooms create --personal
```

| Flag | Description |
| --- | --- |
| `--personal` | Create your personal room |
| `--title string` | Set the title |

## `delete`

Delete a room.

Its link stops working for everyone you sent it to. Pass personal to delete your personal room, and `rooms create --personal` to make a new one.

```
proton meet rooms delete REF
```

```bash
proton meet rooms delete E87W2DAJ60
proton meet rooms delete personal --dry-run
```

## `get`

Show one room, link and all.

REF is the ID in the link after id-, such as E87W2DAJ60, the whole link, or personal for your personal room. The link appears here and when the room is made, never in a listing.

```
proton meet rooms get REF
```

```bash
proton meet rooms get personal
proton meet rooms get E87W2DAJ60 --output json
```

## `list`

List your rooms, the personal room first.

The links are not shown: `rooms get` shows one.

```
proton meet rooms list
```

```bash
proton meet rooms list
proton meet rooms list --output json
```

## `rotate`

Give your personal room a new link, and print it.

REF is personal: no other room can get a new link. The old link stops working for everyone you sent it to. The room is named Personal meeting room again. Its link can be replaced once every 24 hours.

```
proton meet rooms rotate REF
```

```bash
proton meet rooms rotate personal
proton meet rooms rotate personal --yes
```

## `update`

Rename a room.

REF is the room's ID, its whole link, or personal for your personal room. The link stays as it is.

```
proton meet rooms update REF
```

```bash
proton meet rooms update E87W2DAJ60 --title 'Team room'
proton meet rooms update personal --title 'Office hours'
```

| Flag | Description |
| --- | --- |
| `--title string` | Replace the title |

---

Every command also takes the [flags that work everywhere](../about/commands.md#flags-that-work-on-every-command).
