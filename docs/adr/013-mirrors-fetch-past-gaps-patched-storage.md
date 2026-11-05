# 013. Mirrors fetch past what they cannot find yet; storage patched for many manifests

**Status:** accepted 2026-11-05; the daemon half built, the storage patches
on their way (jimmy-crib/vpavlin).

## Context

k11's first backup, 9,500 objects and 150 GB, never reached scribe: every
fetch failed with "Failed to fetch manifest … Key doesn't exist", and after
30 minutes of nothing the run gave up (ADR-012). jimmy-crib/vpavlin found why
in logos-storage and the libp2p it vendors:

- A manifest is fetched only from providers the DHT names
  (`ManifestProtocol.fetchManifest`), never from a peer already connected.
- A node keeps only its 500 most recently provided keys
  (`DefaultProvidedKeyCapacity`); storage builds its DHT with the defaults.
  So a node holding 9,500 manifests answers for about 500 of them at a time,
  whichever its 30-minute sweep reached last.
- Provider records are kept in memory only, on both sides.

Our daemon made it worse: a mirror stopped at the first object it could not
fetch and started over in another order, so it fetched nothing; and a node
restarted whenever a peer's SPR string changed, which it does on every
restart of that peer (its sequence number).

## Decision

- **A mirror fetches eight objects at a time and goes past what it cannot
  find yet,** trying those again on the next pass; it gives up only when
  nothing has arrived for 30 minutes, and says meanwhile how many it is
  still looking for.
- **A node restarts for a peer's new SPR only when the peer or its
  addresses changed** (`sprIdentity`); the newest SPR is kept for its next
  start either way.
- **The storage node's output goes into the daemon's log**, line by line.
- **Our logos-storage carries two patches** (CI builds it, ADR-010):
  provided-key and provider-record capacities of 20,000; and a manifest no
  provider names is asked of the connected peers, which serve only what is
  advertised.

## Consequences

- With the patches, a source's whole backup can be found however many
  objects it has, and a mirror connected to it fetches without the DHT.
- We build a patched logos-storage until upstream takes the patches.
