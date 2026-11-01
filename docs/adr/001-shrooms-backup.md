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
