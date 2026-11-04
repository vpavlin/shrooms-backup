# 011. Backups on several meshes: a node per mesh, names qualified by mesh

**Status:** accepted 2026-11-04 by the owner ("no, build it properly:) We
need multi mesh"); built.

## Context

The daemon ran on one mesh (ADR-001, ADR-009): its storage node, its API and
its firewall rule on that mesh's address, and devices known by their bare
name. scribe was a mirror on the office mesh. k11 is on the home mesh only,
with a 150 GB backup to keep, and scribe joined the home mesh for it; but
scribe's backup daemon still answered on office alone, so k11 had no mirror.
Moving scribe to home would have taken it from the office machines.

The same limit stands in the way of backups with friends: a mesh of your own
machines and a mesh you share with a friend, on one mirror.

logos-storage listens on one address and advertises one (`--listen-ip`,
`--nat=extip:`), so one node cannot serve two meshes.

## Decision

- **`meshes`, a list; the first is the primary.** `mesh` alone, as configs
  were written before, is a list of one. The installer takes `--mesh A,B`.
- **A storage node per mesh,** on this machine's address there, in
  `node-<mesh>/`, on ports 8590/8591 for the primary and the next two for
  each mesh after it. Each mesh's nodes are a DHT of their own: a friend's
  mesh never connects to the office's nodes.
- **The API on every mesh's address.** A caller is the device the mesh says
  is at its address, on the mesh it called on; an address of another mesh,
  at this one's, is refused.
- **Names are qualified by mesh**, in this machine's own labels: bare on the
  primary, `name@mesh` elsewhere. A device of the same name on two meshes has
  two copies (`nothing`, `nothing@home`). **A bare owner is the device on the
  primary only**: on a friend's mesh anyone may be called "laptop". An owner
  on another mesh is named `name@mesh`.
- **A source backs up over one mesh**, `source.mesh` (the primary when not
  set; the installer picks the first mesh its mirrors are all on). Its
  mirrors must be on it: they fetch from its node there. A source asking a
  mirror for its own copy by its own name gets the copy of the mesh it asked
  on, since it cannot know the mirror's label for it.
- **One limit for the machine, over every mesh.** Each node may take the
  whole quota; the daemon refuses a copy that would take the sum over it,
  before fetching anything (`507`, saying how much).
- **The state moves once:** copies kept as `mirrors/<source>` go under
  `mirrors/<primary>/`, `node/` becomes `node-<primary>/`, and `sprs.json`
  gets a level per mesh. Opening again changes nothing.
- **A mesh in the list this machine is not on yet** is skipped with a warning;
  the daemon starts again when it joins (systemd's restart).

## Consequences

- scribe runs with `--mesh office,home`: the office machines keep their
  copies, k11 keeps its own over home.
- The installer records the firewall rules it opened (`config.json.firewall`)
  and closes them before opening the ones the new list needs, so a mesh left,
  or the order changed, leaves nothing open behind.
- A mesh dropped from the middle of the list moves the ports of the meshes
  after it: their nodes restart with new SPRs, which sources learn again on
  their next push.
- The apps show copies on other meshes as `name@mesh`. They choose owners on
  the primary mesh; an owner on another is set in the config for now. To
  offer one, an app has to read the machine's own peers, in its labels.
- A source with mirrors on two different meshes is not supported: its data
  would have to be staged on two nodes.
