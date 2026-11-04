# shrooms-backup and the logos-storage it runs, for scripts/install.sh to
# copy out (ADR-001, ADR-010). Nothing runs this image: the daemon runs on the
# host, as root. shrooms-backup is pure Go and static; logos-storage is CI's
# build of the pinned upstream commit (ADR-010), dynamically linked against
# glibc and libstdc++. restic the installer fetches, pinned by its checksum.
FROM scratch
ARG TARGETARCH
COPY shrooms-backup-${TARGETARCH} /usr/bin/shrooms-backup
COPY logos-storage-${TARGETARCH} /usr/lib/shrooms-backup/logos-storage
ENTRYPOINT ["/usr/bin/shrooms-backup"]
