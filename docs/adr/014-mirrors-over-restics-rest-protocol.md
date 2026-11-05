# 014. Mirrors over restic's REST protocol; Logos Storage a tier of its own

**Status:** accepted 2026-11-05 by the owner ("sounds good, let's do this");
built. Supersedes how
ADR-001 moves and keeps backups between a source and its mirrors; ADR-001's
reasons for restic, keys and holds stand.

## Context

k11's first backup, 150 GB, showed what the design costs at that size:

- **The source keeps a second copy.** A run stages everything it uploads in
  the source's own storage node, deleted only once every mirror holds it:
  158.8 GB on k11, next to the 150 GB it backs up.
- **Logos Storage cuts it into what it is worst at.** restic's ~16 MB packs
  became 9,500 datasets: a manifest, a tree and 64 KB blocks each, 2.4
  million blocks. A mirror finds each manifest through the DHT, where a node
  answers for its 500 newest only (ADR-013).
- **It is slow.** Node to node measured 4-5 MiB/s: 9-10 hours for k11, on a
  mesh link that carries several times that.

And between a person's own machines, the storage network adds nothing that
restic and the mesh do not already give:

- restic names every object by the SHA-256 of its content, checks it on
  read, keeps each chunk once and encrypts it. Content addressing,
  integrity, deduplication and encryption are restic's.
- The mesh says who is calling and keeps everything off the internet. It
  also says where each machine is, so there is nothing to discover.

Logos Storage's own value is the other case: data held by nodes you do not
know in advance, found by CID from anywhere. That is backups exchanged with
other people (the owner wants to show it with friends, on a stream), not
copies among your own machines.

## Decision

### Between a source and its mirrors: restic's REST protocol, pushed

- **A mirror is a restic REST server** for each source it keeps, at
  `/v1/repo/<source>/` (already served for restores). It stores the
  repository as a plain directory in restic's own layout
  (`mirrors/<mesh>/<source>/`): a mirror's copy is an ordinary restic
  repository, readable with restic alone, without our daemon or any node.
- **The source pushes.** restic writes to the source's daemon on loopback, as
  now. The daemon passes each object on to every mirror's REST endpoint over
  the mesh, in the order restic wrote them, one queue a mirror. A mirror
  therefore never holds a snapshot whose data it lacks. The source keeps an
  object, in a spool, until every mirror has it, then deletes it. The spool
  holds what is in flight, not the whole backup.
- **Who may write:** a source writes only its own repository, named by the
  mesh (ADR-011). A mirror checks each object against its name: restic
  names data, index and snapshot files by the SHA-256 of their content.
- **Nothing is lost by deleting.** A delete (restic's forget and prune)
  moves the object aside; it is purged after 30 days, as holds are now. A
  compromised source cannot take its history with it: restic's append-only
  idea, kept.
- **The quota is the mirror's,** checked on every write over all its meshes
  (507 when full).
- **A run counts** when every mirror holds every object of the snapshot, as
  now; progress is bytes each mirror holds (ADR-012).
- **A mirror that was away catches up.** It lists what it holds (the REST
  protocol's listings), and the source sends what it lacks. If the source no
  longer has an object, it reads it from another mirror, as it may read its
  own copies. A spool that would grow past a limit drops the slow mirror
  from the run; that mirror catches up on the next one.
- **Bigger packs:** restic runs with `--pack-size 64` (MB), a quarter of the
  objects for the same data.

### Logos Storage: a tier of its own

- **A source may name a Logos Storage target among its mirrors.** The
  daemon publishes the source's encrypted objects to Logos Storage nodes and
  keeps their CIDs; nothing on those nodes can be read without the source's
  key. It is for backups with other people, and for showing Logos Storage,
  over a shared mesh first; on the public network once reachability allows.
- It is built after the direct mirrors, in an ADR of its own, on the
  patched logos-storage (provided-key capacity, and manifests fetched from
  connected peers: ADR-013).

## Migration

- **Mirrors:** copies kept in a storage node are written out once into the
  directory layout, from the node, in the background. The node is stopped
  when nothing is left in it and no Logos Storage tier needs it.
- **Sources:** the staging copy in the node is dropped once every mirror
  holds the repository in the new layout, sent from the staging copy where
  it still exists (k11) or as a new full run.
- The apps need nothing new: progress, holds and settings keep their shape.

## Consequences

- A source needs room for what is in flight, not a second copy of its
  backup.
- Copies move at the mesh link's speed, with no DHT, manifests or blocks.
- A mirror's copy outlives this software: plain restic reads it.
- Logos Storage is used where its properties matter, and shown there,
  rather than as a slower filesystem between machines that know each other.
- One more component, the spool and per-mirror queues, replaces the map and
  version handshake for mirrors. The map stays for the Logos Storage tier.

## As built (2026-11-05)

- **Mirror:** `Store` (store.go), a restic repository per copy in
  `mirrors/<mesh>/<source>/repo/`, checking hashes, holding deletions, with
  the quota over every copy. A source writes its own copy at
  `/v1/repo/<source>/`; owners read it.
- **Source:** `SourceRepo` (source.go): a catalog log of the repository, a
  spool (`SpoolMax` 8 GB before restic's writes wait), and a sender a mirror
  that lists the copy, then streams what it lacks in order and deletes what
  the catalog dropped. Senders run all the time, not only in a run.
- **Older sources still push the old way** (a map, fetched through the node);
  what they send lands in the same store, so mirrors can be updated first.
  A new source refuses a mirror from before ADR-014 with a message saying so:
  **update mirrors before sources.**
- **Measured:** two containers, real restic and real daemons, 1.2 GB: the copy
  is 18 packs of 64 MB, `restic check` clean on the mirror's directory, the
  source's spool 144 KB after; a second run of 477 MiB, mirrored included,
  took 4 s.
