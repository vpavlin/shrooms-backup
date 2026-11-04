# 010. logos-storage ships in the image, built by CI from the pinned commit

**Status:** accepted 2026-11-04, after k11 could not install.

## Context

Backups need two fixes upstream Logos Storage has not released (#1540, merged
but unreleased; #1575, open; ADR-001). The installer took a build of them that
jimmy-crib/vpavlin made, `be9807f`, from
`https://jimmy-crib.office.mesh:8444/storage-bin/`, checked by its sha256.
That address exists only on our office mesh. k11, on the home mesh, ran the
documented install and stopped at "Could not resolve host:
jimmy-crib.office.mesh" ("why would it depend on mesh resources", the
owner). The build was also amd64 only.

## Decision

- **CI builds logos-storage from the pinned upstream commit**
  (`logos-storage/logos-storage-nim@be9807ff`) as upstream's own Dockerfile
  does (`make update && make`, `-d:disableMarchNative`), on native Ubuntu
  24.04 runners for amd64 and arm64, cached by commit so only a new pin
  rebuilds. It checks the binary reports that revision.
- **The image carries it** at `/usr/lib/shrooms-backup/logos-storage`, beside
  `shrooms-backup`. The installer copies both out of the same image; the
  image is the pin, as it already was for the daemon.
- **Nothing an install needs comes from a mesh.** `--storage-bin` still takes
  another build; with no container runtime, `--binary` and `--storage-bin`
  together install without the image.

## Consequences

- Any machine that can pull from ghcr.io installs, on either architecture.
- logos-storage is dynamically linked: a glibc and libstdc++ as new as
  Ubuntu 24.04's are needed. The installer checks it runs and says so.
- A new pin is a one-line change in CI; the first build after it takes the
  better part of an hour, then it is cached.
- When upstream releases both fixes, the pin moves to the release; building
  it here stays the simplest way to ship it, unless upstream publishes
  binaries for both architectures.
