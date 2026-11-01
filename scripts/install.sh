#!/usr/bin/env bash
# Install Shrooms Backup ON THIS MACHINE (ADR-001): a source
# that backs itself up to mirrors on the mesh, a mirror that keeps copies for
# them, or both.
#
#   curl -fsSLO https://raw.githubusercontent.com/vpavlin/shrooms-backup/main/scripts/install.sh
#   sudo bash install.sh --mirror                       # keep copies for others
#   sudo bash install.sh --source /etc/shrooms --source /home/me/notes --mirrors proteus,scribe
#   sudo bash install.sh --uninstall
#
# Shrooms must be installed and this machine in a mesh (scripts/install.sh).
#
# What it does:
#   - takes shrooms-backup out of its image, ghcr.io/vpavlin/shrooms-backup,
#     into /usr/local/bin (or --binary FILE);
#   - restic, a pinned release checked by its sha256, into /usr/local/bin;
#   - logos-storage, a pinned build checked by its sha256, into
#     /usr/local/lib/shrooms-backup (or --storage-bin FILE): upstream has no
#     release with the two fixes backups need yet (#1540, #1575), so it is
#     jimmy-crib's build of them, reachable on our office mesh, amd64 only;
#   - /etc/shrooms-backup/config.json: the mesh, the roles, the owners;
#   - opens TCP 7394 (the API) and the node's port (8590) to this machine's
#     address on that mesh only, when firewalld or ufw is running;
#   - a root service, shrooms-backup.service, started and enabled.
#
# A source also needs its key, /etc/shrooms-backup/key: from the owner's
# Keycard (the Shrooms Backup app), or `shrooms-backup key` meanwhile.
# Without it the backups cannot be read, by anyone, ever.
#
# Re-running is safe; it rewrites the config from what is given.
set -euo pipefail
IMAGE=${IMAGE:-ghcr.io/vpavlin/shrooms-backup:latest}
SOCK=/run/shrooms/shrooms.sock
RESTIC_VERSION=0.19.1
declare -A RESTIC_SHA=([amd64]=f415415624dcc452f2a02b8c33641791a8c6d6d3b65bbb3543fcf9a25151585c
                       [arm64]=a5f64aaab53d51e311fa3829124c5b703f2d14cf187d8640b6be3b2b49376465)
STORAGE_URL=${STORAGE_URL:-https://jimmy-crib.office.mesh:8444/storage-bin/logos-storage-be9807f-linux-amd64}
STORAGE_SHA=${STORAGE_SHA:-75f8f1d60369f14c6782f461316f4a7fa663fe752ca01f1e70c92defd21fe5ae}
PORTS="7394 8590"

usage() {
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
    cat <<EOF
Options:
  --mirror            keep copies for other machines
  --always-on         say this machine is always on (a server, not a laptop)
  --preset NAME       back up a preset: shrooms, agents, agent-history, foray, keys, services (repeat)
  --source PATH       back up PATH (repeat for more)
  --exclude PATTERN   leave out what matches (repeat)
  --mirrors A,B       the machines that keep this source's copies
  --times HH:MM,...   when to back up (default 03:00,15:00)
  --keep-local        keep this source's backup in its own node too
  --owners A,B        devices that may restore any backup here and start runs
  --mesh LABEL        the mesh to run on (default: this machine's first)
  --quota-gb N        the node's storage quota (default 50; 200 with --mirror)
  --binary FILE       use this shrooms-backup instead of the image's
  --storage-bin FILE  use this logos-storage instead of the pinned build
  --uninstall         stop and remove it (the backups on mirrors stay)
EOF
    exit "${1:-0}"
}

MIRROR=false ALWAYS=false KEEP_LOCAL=false UNINSTALL=0 MESH="" QUOTA="" TIMES="03:00,15:00"
BINARY="" STORAGE_BIN="" MIRRORS="" OWNERS=""
SOURCES=() EXCLUDES=() PRESETS=()
while [ $# -gt 0 ]; do
    case $1 in
        --mirror) MIRROR=true; shift ;;
        --always-on) ALWAYS=true; shift ;;
        --preset) PRESETS+=("$2"); shift 2 ;;
        --source) SOURCES+=("$2"); shift 2 ;;
        --exclude) EXCLUDES+=("$2"); shift 2 ;;
        --mirrors) MIRRORS=$2; shift 2 ;;
        --times) TIMES=$2; shift 2 ;;
        --keep-local) KEEP_LOCAL=true; shift ;;
        --owners) OWNERS=$2; shift 2 ;;
        --mesh) MESH=$2; shift 2 ;;
        --quota-gb) QUOTA=$2; shift 2 ;;
        --binary) BINARY=$2; shift 2 ;;
        --storage-bin) STORAGE_BIN=$2; shift 2 ;;
        --image) IMAGE=$2; shift 2 ;;
        --uninstall) UNINSTALL=1; shift ;;
        -h|--help) usage 0 ;;
        *) echo "unknown option $1"; usage 2 ;;
    esac
done
[ "$(id -u)" = 0 ] || { echo "run it as root (sudo)"; exit 1; }

status() { curl -s -m 5 --unix-socket "$SOCK" http://unix/status; }
mesh_addr() { # LABEL -> this machine's address on it
    status | python3 -c 'import json,sys
for m in json.load(sys.stdin).get("meshes") or []:
    if m.get("label")==sys.argv[1] and m.get("overlay"): print(m["overlay"])' "$1"
}

firewall() { # add | remove ADDRESS
    local verb=$1 a=$2 p
    if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
        for p in $PORTS; do
            local rule="rule family=ipv6 destination address=$a/128 port port=$p protocol=tcp accept"
            firewall-cmd -q --permanent --"$verb"-rich-rule="$rule" 2>/dev/null || true
        done
        firewall-cmd -q --reload
        echo "  firewalld: $PORTS $([ "$verb" = add ] && echo open to $a only || echo closed again)"
    elif command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        for p in $PORTS; do
            if [ "$verb" = add ]; then ufw allow to "$a" port "$p" proto tcp >/dev/null
            else ufw delete allow to "$a" port "$p" proto tcp >/dev/null 2>&1 || true; fi
        done
        echo "  ufw: $PORTS $([ "$verb" = add ] && echo open to $a only || echo closed again)"
    fi
}

if [ $UNINSTALL -eq 1 ]; then
    echo "==> removing Shrooms Backup"
    systemctl disable --now shrooms-backup 2>/dev/null || true
    rm -f /etc/systemd/system/shrooms-backup.service
    systemctl daemon-reload
    if [ -f /etc/shrooms-backup/config.json ]; then
        m=$(python3 -c 'import json;print(json.load(open("/etc/shrooms-backup/config.json"))["mesh"])' 2>/dev/null || true)
        a=$([ -n "$m" ] && mesh_addr "$m" || true)
        [ -n "$a" ] && firewall remove "$a"
    fi
    rm -f /usr/local/bin/shrooms-backup
    echo "  kept: /etc/shrooms-backup (the config and the key: the key is the only way to read this"
    echo "  machine's backups), /var/lib/shrooms-backup (copies kept for others), restic, logos-storage"
    exit 0
fi

echo "==> checking this machine"
[ -S "$SOCK" ] || { echo "  no shrooms daemon here: install shrooms first (scripts/install.sh)"; exit 1; }
command -v python3 >/dev/null || { echo "  needs python3"; exit 1; }
[ -n "$MESH" ] || MESH=$(status | python3 -c 'import json,sys
ms=[m["label"] for m in json.load(sys.stdin).get("meshes") or [] if m.get("overlay")]
print(ms[0] if ms else "")')
ADDR=$(mesh_addr "$MESH")
[ -n "$ADDR" ] || { echo "  not on mesh \"$MESH\""; exit 1; }
NSRC=$(( ${#SOURCES[@]} + ${#PRESETS[@]} ))
[ $NSRC -gt 0 ] || [ "$MIRROR" = true ] || { echo "  nothing to do: --mirror, or --preset/--source with --mirrors"; usage 2; }
[ $NSRC -eq 0 ] || [ -n "$MIRRORS" ] || { echo "  a source needs --mirrors"; exit 2; }
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
echo "  on mesh $MESH at $ADDR ($ARCH)"

echo "==> shrooms-backup"
if [ -n "$BINARY" ]; then
    install -m 0755 "$BINARY" /usr/local/bin/shrooms-backup
else
    RUNTIME=$(command -v podman || command -v docker || true)
    [ -n "$RUNTIME" ] || { echo "  neither docker nor podman, and no --binary"; exit 1; }
    "$RUNTIME" pull -q "$IMAGE" >/dev/null 2>&1 || "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 ||
        { echo "  could not get $IMAGE"; exit 1; }
    cid=$("$RUNTIME" create "$IMAGE")
    trap '"$RUNTIME" rm -f "$cid" >/dev/null 2>&1 || true' EXIT
    tmp=$(mktemp)
    "$RUNTIME" cp "$cid:/usr/bin/shrooms-backup" "$tmp"
    install -m 0755 "$tmp" /usr/local/bin/shrooms-backup
    rm -f "$tmp"
fi
command -v restorecon >/dev/null && restorecon /usr/local/bin/shrooms-backup 2>/dev/null || true

echo "==> restic $RESTIC_VERSION"
if /usr/local/bin/restic version 2>/dev/null | grep -q "restic $RESTIC_VERSION "; then
    echo "  already here"
else
    t=$(mktemp -d)
    curl -fsSL -o "$t/r.bz2" "https://github.com/restic/restic/releases/download/v$RESTIC_VERSION/restic_${RESTIC_VERSION}_linux_${ARCH}.bz2"
    echo "${RESTIC_SHA[$ARCH]}  $t/r.bz2" | sha256sum -c --quiet || { echo "  restic's checksum does not match"; exit 1; }
    # Python, not bunzip2: a minimal Fedora (the VPS) has no bzip2, and the
    # failure went by silently the first time (2026-11-01).
    python3 -c 'import bz2,sys; open(sys.argv[2],"wb").write(bz2.open(sys.argv[1]).read())' "$t/r.bz2" "$t/r"
    install -m 0755 "$t/r" /usr/local/bin/restic
    rm -rf "$t"
    /usr/local/bin/restic version >/dev/null || { echo "  restic does not run"; exit 1; }
fi

echo "==> logos-storage"
install -d -m 0755 /usr/local/lib/shrooms-backup
if [ -n "$STORAGE_BIN" ]; then
    install -m 0755 "$STORAGE_BIN" /usr/local/lib/shrooms-backup/logos-storage
elif [ -x /usr/local/lib/shrooms-backup/logos-storage ] &&
     echo "$STORAGE_SHA  /usr/local/lib/shrooms-backup/logos-storage" | sha256sum -c --quiet 2>/dev/null; then
    echo "  already here"
else
    [ "$ARCH" = amd64 ] || { echo "  the pinned build is amd64 only: give one with --storage-bin"; exit 1; }
    t=$(mktemp)
    curl -fsSLk -o "$t" "$STORAGE_URL" || { echo "  could not fetch $STORAGE_URL (on the office mesh): --storage-bin"; exit 1; }
    echo "$STORAGE_SHA  $t" | sha256sum -c --quiet || { echo "  its checksum does not match"; rm -f "$t"; exit 1; }
    install -m 0755 "$t" /usr/local/lib/shrooms-backup/logos-storage && rm -f "$t"
fi

echo "==> the config"
install -d -m 0700 /etc/shrooms-backup
[ -n "$QUOTA" ] || QUOTA=$([ "$MIRROR" = true ] && echo 200 || echo 50)
python3 - "$MESH" "$MIRROR" "$ALWAYS" "$OWNERS" "$QUOTA" "$MIRRORS" "$TIMES" "$KEEP_LOCAL" \
    "$(printf '%s\n' "${SOURCES[@]+"${SOURCES[@]}"}")" "$(printf '%s\n' "${EXCLUDES[@]+"${EXCLUDES[@]}"}")" \
    "$(printf '%s\n' "${PRESETS[@]+"${PRESETS[@]}"}")" <<'PY'
import json, sys
mesh, mirror, always, owners, quota, mirrors, times, keep, sources, excludes, presets = sys.argv[1:]
lst = lambda s, sep=",": [x.strip() for x in s.split(sep) if x.strip()]
c = {"mesh": mesh, "mirror": mirror == "true", "always_on": always == "true",
     "owners": lst(owners), "node": {"quota_gb": int(quota)}, "restic": "/usr/local/bin/restic"}
if lst(sources, "\n") or lst(presets, "\n"):
    c["source"] = {"presets": lst(presets, "\n"), "paths": lst(sources, "\n"), "exclude": lst(excludes, "\n"), "mirrors": lst(mirrors),
                   "times": lst(times), "keep_local": keep == "true"}
json.dump(c, open("/etc/shrooms-backup/config.json", "w"), indent=2)
print("  " + ", ".join(filter(None, ["a mirror" if c["mirror"] else "",
      ("a source of " + " ".join(c["source"]["presets"] + c["source"]["paths"]) + " to " + ", ".join(c["source"]["mirrors"])) if "source" in c else ""])))
PY
chmod 600 /etc/shrooms-backup/config.json

echo "==> the firewall"
firewall add "$ADDR"

echo "==> the service"
cat > /etc/systemd/system/shrooms-backup.service <<'EOF'
[Unit]
Description=Shrooms Backup: backups kept on the mesh's own Logos Storage nodes (ADR-001)
After=network-online.target shrooms.service
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/shrooms-backup daemon
Restart=always
RestartSec=10
# Root: a source may keep what only root reads (the mesh identity in /etc).
StateDirectory=shrooms-backup
Nice=10
IOSchedulingClass=idle

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable shrooms-backup >/dev/null 2>&1
systemctl restart shrooms-backup
for _ in $(seq 30); do shrooms-backup status >/dev/null 2>&1 && break; sleep 2; done
shrooms-backup status | sed 's/^/  /' || { echo "  not answering yet: journalctl -u shrooms-backup"; exit 1; }
if [ $NSRC -gt 0 ] && [ ! -s /etc/shrooms-backup/key ]; then
    echo
    echo "  This source has no key yet, so it cannot back up. Set it from the owner's Keycard"
    echo "  (the Shrooms Backup app), or for now: sudo shrooms-backup key"
fi
