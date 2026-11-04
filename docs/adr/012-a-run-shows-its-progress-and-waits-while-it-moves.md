# 012. A run shows its progress, and waits for a mirror as long as it moves

**Status:** accepted 2026-11-04 by the owner ("Can we have a progressbar for
the backup? Do we know how much still needs to be uploaded?"; "sounds good");
built.

## Context

k11's first backup is about 150 GB. Nothing said how far a run was: the apps
showed "Backing up now" for hours. And a source waited at most 6 hours for
each mirror, as a mirror waited 6 hours for its fetch: at the 4-5 MiB/s
mirroring runs at (ADR-001), 150 GB takes 9-10 hours, so the first run would
have been reported failed while it was going fine.

## Decision

- **`info` carries the run under way** (`progress`): first restic reading
  the files and storing what is new in this machine's node (files and bytes
  done of found, and restic's own time estimate), then each mirror's share
  of the run, in bytes.
- **restic scans before it reads** (no more `--no-scan`), which is what gives
  it a total, and prints its progress every 2 s (`RESTIC_PROGRESS_FPS`). Its
  progress lines are taken out of the output kept for the summary.
- **A mirror says what it still lacks in bytes** (`missing_bytes`), after
  each object it fetches: the node's fetch returns when the object is here,
  so the first pass is where the time goes. Until it has looked, it counts
  the whole run as missing, never less than the truth.
- **No fixed waits.** A source gives up on a mirror, and a mirror on a fetch,
  only when nothing has moved for 30 minutes (`Stalled`).
- **Both apps draw a bar a phase**, from one wording (mobile/src/model.ts
  `progressBars`; the Basecamp view says the same), and `shrooms-backup
  status` prints it.

## Consequences

- A long first run is shown as it goes, and counts once the mirrors hold it,
  however long that takes.
- Scanning reads the metadata of everything backed up, beside the backup.
- A mirror that stops for 30 minutes fails the run as before; it goes on
  fetching in the background, and the next run finds it further on.
