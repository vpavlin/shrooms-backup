# 003. Presets name what a machine keeps; the first fleet

**Status:** accepted 2026-11-01; live on jimmy-crib, proteus, the laptop. Split out of ADR-001, where it was first recorded as a dated section.

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

**The laptop** (2026-11-02) backs itself up (shrooms, agents, agent-history,
keys, foray) and is no mirror: it is not always on. Its first full run read
10,493 files, 1.2 GB, into a 326 MB repository; proteus and scribe fetched it
over the office LAN in about two minutes, the VPS over the internet at about
2.6 MB/s (the office's upload), about three minutes. A run counts once every
mirror holds it, so the slowest mirror sets the pace of a first backup:
about 6–7 minutes a GB to the VPS.
