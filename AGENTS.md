# Working in this repository

Shrooms Backup: a daemon (`cmd/shrooms-backup`) and its package
(`internal/backup`). It uses nothing of Shrooms' code, only the Shrooms
daemon's status over its control socket (`internal/backup/mesh.go`).

- `make test` before a commit; the end-to-end tests run the real restic.
- A settled decision goes into `docs/adr/` with a row in its README, in the
  same change. Comments say why, not what.
- Stage files by name, never `git add -A`.
- Run a change where it runs (a source and a mirror on the mesh) before a
  release, and say where it ran: the first live run found a bug every test
  passed.
- Logos Storage: never `--block-ttl 0` (it expires at once); a re-upload keeps
  its CID only with the same filename, type and block size.
