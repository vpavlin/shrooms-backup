# 001. Shrooms Backup: a daemon of its own, restic over Logos Storage, mirrors chosen per machine

**Status:** accepted 2026-11-01 by the owner ("a general shrooms backup
solution… definitely do a separate daemon"; restic: "try it! if it is
successful start building"). The spike works (below); being built.
Recorded first as shrooms ADR-077 and moved here with the code (2026-11-01); it
supersedes shrooms ADR-052.

## Context

Shrooms ADR-052 (2026-10-13) planned backups on three private Logos Storage nodes and
was never built. Re-checked on 2026-11-01 with jimmy-crib/vpavlin against
current Logos Storage:

- `--block-ttl 0` expires a block at once rather than never; the default is
  30 days. 052 as written would have lost every backup within seconds.
- Storage keeps nothing beyond the copies we place: the marketplace, proofs
  and erasure coding are gone from its code.
- Node-to-node fetch needs #1540 (merged 2026-10-30, unreleased) and #1575
  (open): without the second, about 0.13 MB/s; with it, 4–5 MiB/s.
- Encrypted archives do not deduplicate: 052's retention was about 40 full
  copies per node.

The owner then asked for more than fleet backups: a backup any Shrooms user
can set up from Basecamp. Pick what to keep (folders, app data, config),
pick which machines keep copies (scribe and proteus, say), upload to the local
node and have the others fetch it, with deltas and retention.

## Decision

**A daemon of its own, `shrooms-backup`,** on every machine that backs up or
keeps copies, like `shrooms-agent`: it needs root for some paths (the mesh
identity in `/etc/shrooms`) and does heavy I/O, neither of which belongs in
the mesh daemon. It serves its API on the machine's mesh addresses and
announces itself, so the apps find every machine that runs it.

**restic is the engine.** It already chunks files by content, keeps each chunk
once, encrypts, snapshots, forgets and prunes, and checks. restic talks to a
backend over a small HTTP protocol (REST v2), and every object it writes,
except its locks, is immutable and named by its content. `shrooms-backup` is
that backend, and keeps each object in Logos Storage as a CID; its only state
is the map from restic's names to CIDs, and the locks.

**Roles, chosen per machine:**

- a **source** backs itself up: what to keep (folders, presets for app data
  and config), when, and to which mirrors;
- a **mirror** keeps copies for other machines: it announces its free space
  and whether it is always on, and lends its Logos Storage node.

A run uploads to the source's own node, then asks each chosen mirror's daemon
to fetch the new CIDs and the map. It counts only when every mirror holds
every object. A restore reads from any mirror; a mirror serves its copy from
its own node.

**Logos Storage, private, over the mesh.** Each daemon runs its node on its
mesh address (`--listen-ip`, `--nat=extip:` that address), bootstrapped only
from the other daemons' nodes (their SPRs, exchanged over the API), so blocks
never leave our machines; the REST API stays on loopback (it has no
authentication). Blocks are kept for 100 years (`--block-ttl 3153600000`):
retention is restic's, never the store's.

**Keys:** a repository per source, its password derived from the owner's
Keycard (BIP85, an index per machine) and held root-only on that machine.
The card is needed at setup and at restore. A source can read only its own
backups; a mirror only ever holds ciphertext.

**Deleting:** only the source's restic prunes its repository, and a mirror
keeps what a prune removed for 30 days before deleting it from its node, so a
compromised source cannot take its history with it.

**Apps:** a Basecamp module first (sources, presets, mirrors, status,
restore). The phone later: Android lets an app back up only folders the user
picks, and is no use as a mirror.

## The spike (2026-11-01)

Laptop and proteus, each with its own private node on its office-mesh
address, bootstrapped from each other; restic 0.19.1 against a spike server
storing every object as a CID (`~/diag/backup-spike`):

- The Shrooms repository (2.3 GB, 14,436 files) backed up in 11 s into
  1.1 GB in Logos Storage (80 objects).
- One file changed: the next run added 12 KiB in 1.5 s.
- Mirrored to proteus (its node fetching all 80 CIDs, the pr1540 build on
  both, without #1575): 1.17 GB in 10m44s, 1.8 MB/s, after a slow first
  two minutes.
- With the laptop's node stopped, proteus restored that snapshot from its own
  node alone: 2.3 GB, 14,436 files, in 26 s, byte-identical to the source
  (the only differences were files changed after the snapshot). A small
  snapshot restored on both machines gave identical trees.
- Logos Storage ignores HTTP Range; the backend caches whole objects and
  serves restic's partial pack reads from the cache.

## Consequences

- Backups work today; their speed to mirrors waits for #1575 in a release
  (and a pinned build until then).
- Another daemon on every machine that takes part, and a node beside it.
- Logos Storage carries real data for us, privately; nothing on the public
  network keeps it.

## What would change our mind

- If mirrors cannot be kept in step reliably, restic's own `rest-server` on
  the mirrors, with the same daemon, discovery and keys.
- A storage network that keeps data (dispersal, proofs): mirrors could then
  include it, as shrooms ADR-052 hoped.

## Its own repository (2026-11-01)

Moved out of the Shrooms repository before it merged ("should we consider
moving the backup out of Shrooms Repo?" — the owner, who then created this
one). It uses nothing of Shrooms' code: only the daemon's status over its
control socket (this machine's name, its mesh addresses, who each peer is),
which is an interface for applications. Its own CI, releases and
contributors, without the delivery library, the Android build or the
mesh's tests.

## Presets and the first fleet (2026-11-01)

**Presets** name what a source keeps, so a machine's config is a list of
names: `shrooms` (identity, state; not core dumps), `agents` (sessions,
settings, schedules, model keys; not uploads, conversations or pi's caches),
`agent-history` (the conversations), `foray`, `keys` (SSH, GPG, tokens,
signing keystores, repository and CA keys), `services` (hand-written units,
`.env` files). `shrooms-backup plan` shows what they come to on a machine.
Measured: everything but agent-history is 0.7–5 MB a machine (jimmy-crib
0.86 MB, holding every APK signing key and the mesh CA); agent-history is 37
MB to 1.5 GB.

**Mirrors:** proteus and scribe in the office, and the VPS (Hetzner,
Falkenstein) as the copy outside it, installed with `scripts/install.sh`
from the published image. With two office mirrors a lost disk or machine,
a mistaken delete and a hostile source are covered; the office itself (fire,
theft, flood) was not, and is now by the VPS. A machine at home would make a
third place.

## Settings from the apps, and the Basecamp module (2026-11-01)

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
