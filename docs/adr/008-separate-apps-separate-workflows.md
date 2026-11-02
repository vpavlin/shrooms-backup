# 008. Shrooms, Shrooms Agents and Shrooms Backup are separate apps with separate workflows

**Status:** accepted 2026-11-02, by the owner: "agents, shrooms itself and
backup should be separate apps and separate tools and they should not
combine user workflows."

## Context

Twice in two days the shortest path ran through Shrooms: the Keycard command
went into the `shrooms` CLI first (it already reaches the card), and the
phone's Keycard tap was proposed through the Shrooms Android app (it already
pairs with the card over NFC). Both would have made a backup task a Shrooms
task: a person setting up backups would open the mesh's app, and a change to
one product would ride on the other's release.

## Decision

Each product does its own user workflows with its own tools:

- **Shrooms Backup has its own CLI** (`shrooms-backup`), its own Basecamp
  module, its own Android app, and talks to the card itself: its own pairing
  slot and pairing file, its own NFC on the phone. Code it needs from Shrooms
  is copied (the PC/SC reader), never imported (ADR-002).
- **What it uses of Shrooms is the mesh, as any application does:** the
  daemon's status socket (names, addresses), `shrooms_core`'s status in
  Basecamp to find peers, and the network itself. Never a Shrooms screen, a
  Shrooms command or a hand-off into the Shrooms app to finish a backup task.
- The same holds the other way: Shrooms and Shrooms Agents do not grow
  backup features.

## Consequences

- Some duplication: the reader code, a second pairing on the card (it has
  five slots), an NFC module in the backup app. Accepted.
- A person can install Shrooms Backup's apps without knowing how the Shrooms
  app pairs a card, and each product releases on its own.
