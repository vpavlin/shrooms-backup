# 004. Settings come from the apps; Shrooms Backup in Basecamp

**Status:** accepted 2026-11-01; Basecamp core 0.1.1, view 0.1.3 on lan03. Split out of ADR-001, where it was first recorded as a dated section.

**The daemon takes its settings from the apps:** `PUT /v1/config`, owners
only (this machine on loopback, or a device in `owners`), with what the
installer does not own: owners, always-on, the mirror role and quota, and
the source (presets, paths, excludes, times, mirrors, retention). It is
checked like the file, then written over the file whole. Owners, always-on
and the source's settings apply at once (the schedule wakes to new times);
the mirror role, the quota, and becoming or ceasing to be a source belong
to the node and the repository opened at start, so the daemon exits after
answering and systemd starts it again (`Restart=always`). Settings are named
by the mesh, like every other owner call: no password, no token.

**Runs say what they did:** restic runs with `--json`, and each run keeps its
summary (files read, new and changed; bytes read; bytes added) and the
repository's size after it. `/v1/info` says whether a run is going, when the
next is, each held copy's progress and size, and whether the caller is an
owner, so an app can show settings read-only to anybody else.

**Shrooms Backup in Basecamp** (`basecamp/`): `shrooms_backup_core`, which
does the HTTP a view may not (loopback 7395 for this machine, mesh addresses
on 7394 for the others, only the state and settings paths: never a
repository), and the `shrooms_backup` view: Overview (each source's last
run, next run and where its copies are, as the mirrors report them; each
mirror's storage and holdings), Metrics (a machine's runs as bars: bytes
added, repository size, duration; a failed run red) and Settings. It finds
the daemons by asking every peer `shrooms_core` knows. Plain QtQuick in the
Shrooms palette, for the Shrooms view's reason: the bundled design system
differs between Basecamp versions. Built for amd64 and arm64 in one lgx.
