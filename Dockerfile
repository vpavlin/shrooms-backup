# shrooms-backup alone, for scripts/install-backup.sh to copy out (ADR-001).
# Nothing runs this image: the daemon runs on the host, as root. Pure Go and
# static, so one binary per architecture and nothing else; restic and
# logos-storage the installer fetches, pinned by their checksums.
FROM scratch
ARG TARGETARCH
COPY shrooms-backup-${TARGETARCH} /usr/bin/shrooms-backup
ENTRYPOINT ["/usr/bin/shrooms-backup"]
