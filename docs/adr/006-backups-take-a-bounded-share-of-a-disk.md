# 006. Backups take a bounded share of a disk

**Status:** accepted 2026-11-01 (the owner: "we should not assume we can take all the space"). Split out of ADR-001, where it was first recorded as a dated section.

A machine gives backups **10 GB unless told otherwise** (`quota_gb`, now in
decimal GB as the apps show sizes; it was GiB, and 10 read "11 GB"). The
installer gave mirrors 200 GB and others 50, which assumed the disk was
ours; Václav: "we should not assume we can take all the space". The fleet
is at 10 GB each (they hold 41 MB).

**Never more than the disk can give:** a quota may be at most what is free
plus what the node holds already, less a reserve kept for everything else:
a tenth of the disk, but no more than 10 GB. The settings API refuses more,
saying how much the disk has; a daemon whose configured quota no longer
fits (the disk filled with other things) starts with what fits and logs it,
but never below 1 GB, which a source needs to stage a run. A plain tenth
left jimmy-crib (20 GB free of 233) nothing, and its runs would have failed.
`/v1/info` carries the disk's free and total space, so the apps show both.
