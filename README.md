# pz-backup

Back up Project Zomboid saves. Every run writes a new archive — nothing you
already have is ever overwritten — and each one records whether the game was
writing to the save while the copy ran.

A Zomboid run can end in a single frame, and some mods end it on purpose: a
scripted `player:Kill()` with no warning and no confirmation. By the time you
know you wanted a backup, the save is already overwritten. So this is built to
be run often and carelessly, on the theory that a backup tool you have to think
about is one you will not use.

## Install

```
go install github.com/Legion112/pz-backup/cmd/pz-backup@latest
```

Or from a clone:

```
go build -o pz-backup ./cmd/pz-backup
```

Go 1.27 or newer. One dependency, [klauspost/compress](https://github.com/klauspost/compress),
for pure-Go zstd.

## Use

```
pz-backup backup                     # the save the game last had open
pz-backup backup --save MySave       # by name, or Mode/Name
pz-backup backup --all               # every save
pz-backup backup --note "before the escort mission"

pz-backup list                       # saves the game knows about
pz-backup list --backups             # archives already taken
pz-backup verify <archive>           # read it back, check it against its manifest
pz-backup restore <archive>          # unpack; refuses to overwrite anything
```

Saves are read from `~/Zomboid` and archives land in `~/zomboid-backups`.
Override with `--zomboid` / `--out`, or `PZ_ZOMBOID_DIR` / `PZ_BACKUP_DIR`.

```
$ pz-backup backup
Apocalypse/MySave -> /home/you/zomboid-backups/Apocalypse_MySave_2026-10-10_10-53-57.tar.zst
  7631 files, 52.4 MiB  ->  7.0 MiB archive
  clean: nothing changed while copying
```

## What it promises

**It will not overwrite a backup.** Archive names carry a timestamp, a
collision gets a counter, and the file is created with `O_EXCL` — the open
fails rather than truncating something that is already there. That is a kernel
guarantee, not a check-then-write race. `restore` holds the same line: an entry
whose path already exists is an error, and nothing is touched.

**It tells you when a copy is suspect.** You will usually be backing up with
the game running, and a save copied mid-write can be broken in ways that only
surface the day you try to load it. This cannot lock the game out of its own
files, so instead it measures. Every file's size and modification time is
recorded before and after the copy; anything that moved is named in the
manifest and on stdout. SQLite journals get the same treatment — Zomboid keeps
`players.db` and `vehicles.db` in rollback-journal mode and leaves a zero-length
journal between transactions, so bytes in one mean a write was in flight.

```
  SUSPECT: 3 file(s) changed while copying: map_p.bin, players.db and 1 more
  the game was writing to this save. Quit to the main menu and run again
  for a copy you can rely on; this one is kept either way.
```

The archive is kept regardless — a questionable backup beats none. Use
`--strict` to exit non-zero when that happens, for scripts and pre-launch hooks.

## What a backup looks like

Two files per run:

| | |
|---|---|
| `<Mode>_<Save>_<timestamp>.tar.zst` | the save, as a plain tar inside a zstd frame |
| `<Mode>_<Save>_<timestamp>.manifest.json` | what was copied, and how much to trust it |

The archive is ordinary tar+zstd, so `tar --zstd -xf` unpacks it on any machine.
Recovering a save never requires this tool, which is the point — a backup format
you need a specific binary to read is a second thing that can go missing.

The manifest is written last and only on success. An archive with no manifest
beside it is an interrupted run.

```json
{
  "save": "Apocalypse/MySave",
  "game_version": "42.21.0 4a0e9546ec",
  "archive_sha256": "92005f76…",
  "files": 7631,
  "bytes": 54991170,
  "quiescent": true,
  "note": "before the escort mission"
}
```

`verify` reads every byte back and checks the count and size against that
manifest, which is how you find a truncated or bit-rotted archive on a day when
it does not matter yet.

## Restoring

```
pz-backup restore Apocalypse_MySave_2026-10-10_10-53-57.tar.zst
```

It unpacks into `~/Zomboid/Saves` and stops if the save directory is already
there. Move the existing one aside first, or unpack somewhere harmless with
`--into /tmp/check` and copy across only what you need.

## Layout

| | |
|---|---|
| `cmd/pz-backup` | the CLI |
| `internal/zomboid` | finding the Zomboid directory, enumerating saves, `latestSave.ini` |
| `internal/archive` | tar+zstd: create, verify, extract |
| `internal/backup` | one backup: quiescence check, naming, manifest |

```
go test ./...
```

## Scope

Single-player and local saves. It copies a save directory as it finds it; it
does not understand the save format, edit characters, or talk to a dedicated
server.

## Licence

MIT.
