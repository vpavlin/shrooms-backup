# 015. A missed backup runs when the machine is back

**Status:** accepted 2026-11-09 by the owner ("if it misses, then trigger it
immediately when it comes online and connects to the mesh"); built.

## Context

A source runs at its `times` (03:00 and 15:00 by default). The daemon slept
until the next one. Two things went wrong with a laptop:

- **Off at the time:** a machine switched off at 03:30 never ran that backup.
  On start it looked only forward, so the night's run waited until 15:30.
- **Asleep at the time:** a timer does not count suspended time. A laptop
  asleep through 03:30 ran it hours after it woke, at whatever moment the
  timer finished counting.

## Decision

- **The schedule is read from the wall clock every minute,** not slept on.
- **A missed run** is the latest scheduled time having passed with no run
  started at or after it. Any run counts, a failed one too: a failure waits
  for the next time rather than being retried every minute.
- **A run, due or missed, starts only when one of the source's mirrors is
  online on its mesh.** A machine that wakes offline does not record a failed
  run, and goes the moment it is back. A new machine with no runs counts as
  missed, so its first backup is as soon as it is set up.

## Consequences

- A laptop that is off or asleep overnight backs up when it is next on the
  mesh, not the next afternoon.
- A run waits for a mirror rather than failing at its time on a machine
  that is offline. The run list shows nothing until it happens.
- One wake a minute in the daemon, which costs nothing next to restic.
