# 002. Its own repository

**Status:** accepted 2026-11-01 (the owner created the repository: "should we consider moving the backup out of Shrooms Repo?"). Split out of ADR-001, where it was first recorded as a dated section.

Moved out of the Shrooms repository before it merged ("should we consider
moving the backup out of Shrooms Repo?" — the owner, who then created this
one). It uses nothing of Shrooms' code: only the daemon's status over its
control socket (this machine's name, its mesh addresses, who each peer is),
which is an interface for applications. Its own CI, releases and
contributors, without the delivery library, the Android build or the
mesh's tests.
