---
title: Why freed space doesn't show up
description: Why macOS can report less free space than lu-cleaner freed (Trash, APFS local snapshots, hardlinks and clones, open files, purgeable space, recreated caches, swap) and how to check and fix each cause.
sidebar:
  order: 7
---

You cleaned 20 GB, but Finder or `df` shows much less. Usually nothing went wrong. On macOS, deleting a file and getting its blocks back are two separate events, and several things can sit between them.

The fastest way to find out which one applies:

```bash
lu-cleaner doctor
```

## Estimated vs. measured

After a clean, lu-cleaner prints two numbers:

```text frame="terminal" title="lu-cleaner clean --yes --smart"
✓ 42 items cleaned · 18.4 GB freed (estimated)
  Disk free: 21.3 GB → 27.4 GB (6.10 GB measured)
  Less space than expected? Run 'lu-cleaner doctor' (snapshots, Trash, open files…).
```

| Number | What it is |
|---|---|
| **Estimated** | The sum, over the items that were removed for good, of what deleting each one should free: its allocated size on disk, minus the data it shares with files outside it through hardlinks or APFS clones (see [below](#hardlinks-and-apfs-clones)). Moves to the Trash are not part of it. |
| **Measured** | How much the volume's free space grew between the start and the end of the run, as reported by the filesystem (`statfs`, the same number `df` shows). |

The two rarely match exactly. The measured number also includes everything else that happened on the disk during the run: a build writing files, a download, swap growing. It can even be negative. When the estimate is above 1 GB and the measured gain is less than half of it, lu-cleaner suggests running `doctor`. The interactive picker shows the same two lines ("Estimated freed" and "Measured freed") in its summary.

With `--json`, the summary contains `estimated_freed`, `trashed` (bytes moved to the Trash), `measured_freed`, `disk_before` and `disk_after` (in bytes). See [JSON output](/lu-cleaner/reference/json-output/). A dry run deletes nothing, so it measures nothing. `lu-cleaner history` keeps the estimate of each past item in its `FREED` column.

## `lu-cleaner doctor`

`doctor` checks every common cause in one pass and never deletes anything:

```text frame="terminal" title="lu-cleaner doctor"
Disk
  █████████████████████████░░░░░ 84% used · 160 GB free of 995 GB

APFS local snapshots (2)
  com.apple.TimeMachine.2026-09-27-101010.local
  com.apple.TimeMachine.2026-09-28-111512.local
  They pin the blocks of deleted files: that space comes back only when they
  expire (about 24h) or are thinned. To reclaim it now (lu-cleaner never runs these):
    tmutil thinlocalsnapshots / 999999999999 4
    sudo tmutil deletelocalsnapshots 2026-09-27-101010

Trash
  3.20 GB in 1 204 files — empty it to free the space

Running apps that block cleaning
  Simulator  booted simulators and runtimes cannot be deleted
  Codex      an agent may be working in a worktree

Space by category
  CATEGORY            SIZE  ITEMS  RECOMMENDED
  …
```

| Section | What it tells you |
|---|---|
| **Disk** | Used percentage and real free space of the volume that holds your home folder. |
| **APFS local snapshots** | Time Machine local snapshots that keep deleted blocks alive, with the commands to remove them. |
| **Trash** | How much `~/.Trash` holds. Reading it needs Full Disk Access (see below). |
| **Running apps that block cleaning** | Xcode, Simulator, Android emulator, Android Studio, Cursor, VS Code, Docker, Codex, Claude: what each one keeps busy. |
| **Space by category** | A short scan summary with what smart select would free. Skip it with `--no-scan`. |
| **Why isn't my space freed?** | A reminder of the causes below. |

`lu-cleaner doctor --json` returns the same report for scripts. See the [`doctor` reference](/lu-cleaner/reference/commands/doctor/).

The rest of this page covers each cause, from the most common to the least.

## The Trash

Files moved to the Trash still use disk space until the Trash is emptied. This applies to anything you drag to the Trash in Finder, to lu-cleaner's Trash mode (`--trash`, `use_trash = true`, or the `t` key in the picker), and to Xcode archives, which lu-cleaner always moves to the Trash. lu-cleaner counts these moves apart from what it freed:

```text frame="terminal" title="lu-cleaner clean --yes --smart --trash"
✓ 12 items cleaned · 4.20 GB moved to the Trash (estimated)
  Disk free: 21.3 GB → 21.3 GB (0 B measured)
  Space is only freed once the Trash is emptied.
```

Trash mode never deletes anything permanently: worktree removals and commands (`simctl`, `docker`, `brew`…) are skipped, and so are items already in the Trash.

To empty the Trash, use Finder, or let lu-cleaner do it. The Trash is an item of the `system` category. Emptying it cannot be undone, so smart select never recommends it:

```bash
lu-cleaner clean -c system          # select "Trash (N items)"
lu-cleaner clean --yes -k trash --dry-run
```

:::note[Full Disk Access]
macOS protects `~/.Trash`. Without Full Disk Access for your terminal, lu-cleaner cannot list or measure it. It then shows "Trash (size unknown — needs Full Disk Access)", and cleaning that item asks Finder to empty the Trash (Finder may ask you for the Automation permission). To grant access, open **System Settings › Privacy & Security › Full Disk Access**, enable your terminal app, then restart it.

Without Full Disk Access, lu-cleaner also never reads inside other apps' containers (`~/Library/Containers/<app>`, `~/Library/Group Containers/<group>`): macOS would block the scan on a permission prompt. Items there are reported as needing Full Disk Access, and the Docker Desktop and OrbStack disk images are not listed.
:::

## APFS local snapshots

When Time Machine is on, macOS takes local snapshots of the startup disk, roughly every hour. A snapshot keeps every block that existed when it was taken. If you delete a file afterwards, its blocks stay allocated until every snapshot that references them is gone. This is the main reason the measured gain can be close to zero right after a big cleanup.

Snapshots expire on their own after about 24 hours, and macOS thins them when it runs low on space. To reclaim the space now:

```bash
# list them
tmutil listlocalsnapshots /

# ask macOS to thin them as much as possible (no sudo)
tmutil thinlocalsnapshots / 999999999999 4

# or delete one by its date (the part after com.apple.TimeMachine.)
sudo tmutil deletelocalsnapshots 2026-09-27-101010
```

lu-cleaner lists snapshots in `doctor` and as a report-only "Time Machine local snapshots" item in the `system` category. It never runs these commands. Deleting a snapshot removes a local restore point, and `deletelocalsnapshots` needs administrator rights, so that decision stays yours. Backups on your Time Machine disk are not affected.

## Hardlinks and APFS clones

Several tools share file contents between folders instead of copying them:

| Tool | Mechanism |
|---|---|
| **pnpm** | Links packages from its store (`~/Library/pnpm/store`) into each `node_modules`, as hardlinks or APFS clones. |
| **bun** | Clones files from its install cache into `node_modules` on APFS. |
| **Yarn Berry** with `nmMode: hardlinks-global` | Hardlinks files from its global store. |
| **Finder, `cp -c`, copied simulators** | APFS clones (copy-on-write). |

Deleting one side frees only the blocks nobody else uses. If you delete a pnpm project's `node_modules`, the files are still in the store. If you prune the store, the files are still in the projects that use them.

lu-cleaner measures both mechanisms and computes what deleting an item really frees:

- a **hardlinked** file counts once, and counts for nothing when another link lives outside the item;
- an **APFS clone** counts only its private blocks, unless every file sharing its data is inside the item.

When the result is less than the size, the item says so ("Shared with other files (hardlinks or APFS clones of the pnpm store): only 120 MB is really freed"), the size shows a `*`, the JSON output has a `reclaim` field, and totals and the estimate use the real figure.

It is still an estimate. Data shared between two items that are measured separately, such as the bun cache and a `node_modules` installed from it, is freed only when both are deleted. To actually get the space back, remove both sides: the `node_modules` folders of old projects, then the unreferenced packages in the store (`pnpm store prune`, which lu-cleaner offers as a <span class="risk safe">safe</span> command item). See [JS toolchain](/lu-cleaner/guides/js-toolchain/).

## Files still open

A process that has a file open keeps its blocks allocated after the file is deleted, until it closes the file or exits. Typical culprits on a developer machine are the Simulator, Xcode, the Gradle daemon, Docker, a dev server, and an AI agent still running in a worktree.

```bash
lsof +L1                  # deleted files that are still open, with the process holding them
./gradlew --stop          # stop Gradle daemons (in an Android project)
xcrun simctl shutdown all # shut down booted simulators
```

Quitting the app, or restarting, releases the space. lu-cleaner avoids most of these cases before they happen. It skips a folder in which a process is working or from which it runs a program, and a SQLite database that a process has open. Items that belong to a running app wait until you quit it. `doctor` lists the running apps that block a cleanup.

## Purgeable space: Finder vs. `df`

Finder's "Available" figure includes **purgeable** space: local snapshots, iCloud files that can be downloaded again, some caches. macOS frees purgeable space only when something needs it. Finder can therefore show much more room than you can actually use, and its number can move without any deletion.

lu-cleaner, like `df`, reports the space that is really free (`f_bavail`, the blocks available to your user):

```bash
df -h ~
```

Compare lu-cleaner's numbers with `df`, not with Finder. There is nothing to "clean" in purgeable space: you can only speed things up by thinning snapshots (see above).

## Apps recreate their caches

A <span class="risk safe">safe</span> item is a cache that its tool rebuilds automatically. That also means the space comes back as soon as the tool runs again: Xcode rebuilds DerivedData on the next build, Metro its cache on the next bundle, Cursor and other Electron apps their caches on the next launch.

That is expected. Removing a cache you use every day only buys you a slower next start. Smart select focuses on stale items for this reason: <span class="risk moderate">moderate</span> items are recommended only after `stale_after` (14 days by default) without use. To target only what you have not touched for a while:

```bash
lu-cleaner clean --yes --smart --older-than 30d --dry-run
```

## Swap and staged updates

APFS volumes share the free space of their container. Swap files on the VM volume (`/System/Volumes/VM`) grow under memory pressure (simulators, Android emulators, Electron apps, AI agents), and they take free space from your data volume too.

```bash
sysctl vm.swapusage
```

lu-cleaner reports swap above 1 GB as "Swap files (VM volume)", with the <span class="risk never">never</span> risk: never delete these files. Quit memory-hungry apps or restart to shrink swap. macOS updates that are downloaded but not installed yet are also reported ("Staged macOS updates"). Install or skip the update to get that space back.

## Docker's disk image

Docker Desktop stores images, containers and volumes in a single disk image (`Docker.raw`) that does not shrink on its own. Deleting files inside a container does not give space back to macOS. Prune from Docker, then restart it:

```bash
docker system prune
docker builder prune
```

## Items on an external volume

Some items live on another disk: an Android SDK or a model store moved to an external SSD and symlinked back, or a project on `/Volumes/…`. Deleting them would not free any space on your internal disk, so lu-cleaner reports them as "on external volume — no internal gain" and never cleans them.

## Quick reference

| Symptom | Likely cause | What to do |
|---|---|---|
| Measured gain close to zero right after a big clean | APFS local snapshots | Wait about 24 h, or `tmutil thinlocalsnapshots / 999999999999 4` |
| lu-cleaner said "moved to the Trash" | Trash mode, or Xcode archives | Empty the Trash |
| Removed `node_modules` but freed little | pnpm / bun / Yarn Berry sharing | Also prune the store (`pnpm store prune`) |
| Space comes back after quitting an app | Open files | Quit the app, `./gradlew --stop`, shut down simulators |
| Finder shows more free space than lu-cleaner | Purgeable space | Trust `df -h ~` |
| Free space shrinks again the next day | Caches recreated, swap | Clean stale items only (`--older-than`), restart to reset swap |
| Docker still uses a lot | `Docker.raw` never shrinks | `docker system prune`, restart Docker |

## Related

- [Safety model](/lu-cleaner/concepts/safety/)
- [How it works](/lu-cleaner/concepts/how-it-works/)
- [`lu-cleaner doctor` reference](/lu-cleaner/reference/commands/doctor/)
- [FAQ](/lu-cleaner/about/faq/)
