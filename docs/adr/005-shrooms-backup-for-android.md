# 005. Shrooms Backup for Android

**Status:** accepted 2026-11-01; 0.1.2 on Backpack and fdroid-loam. Split out of ADR-001, where it was first recorded as a dated section.

**Shrooms Backup for Android** (`mobile/`, `xyz.vpavlin.shrooms.backup`): the
Basecamp view's three screens, as an Expo app like Foray's. A phone runs no
daemon and cannot ask a shrooms daemon for the mesh, so it is told one
machine (its mesh name, or a `shroomsbackup://add?address=` link) and asks
that daemon's new `GET /v1/peers` for the rest, then every online peer's
`/v1/info`: the IPv6 overlay first, the IPv4 alias where that fails.
What it last saw is kept and shown at once on open, marked "as last seen"
for a machine that did not answer. The phone is an owner by its mesh name,
like any device. The release build is unsigned (`plugins/withUnsignedRelease.js`)
for the publisher to sign. Keys from Keycard come next, in this app: they
need an NFC module and the card.
