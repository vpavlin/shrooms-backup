# 009. One installer; run again, it updates only what it is given

**Status:** accepted 2026-11-02, after the laptop's install.

## Context

`scripts/install.sh` wrote the whole config from its options each run. Moving
the laptop to another mesh meant repeating every preset and mirror, and would
have dropped what the apps had set ("it's just confusing that we have to run
the install with all the parameters", the owner). It also took a machine's
first mesh when not told one: on the laptop that was `default`, where no
mirror is, so its backups could not reach the mirrors and the apps could not
reach it. And `--preset a,b` became one preset named "a,b", which the daemon
refused to start with.

## Decision

- **Run again on a machine already set up, it changes only the settings it is
  given** and keeps the rest of the config, including what the apps set; the
  program is always updated. The firewall rule on a mesh it leaves is removed.
- **The mesh:** the one in the config when there is one; otherwise this
  machine's only mesh, or the one every mirror named is on; otherwise it asks
  for `--mesh` rather than guess.
- **`--preset` takes a list**, and names it does not know are refused before
  anything is written.
