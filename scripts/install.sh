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
# Run again, it updates: the program, and only the settings it is given
# (sudo bash install.sh --mesh office,home); the rest of the config stays.
#
# A machine may run backups on several meshes (ADR-011): a mirror for each,
# a storage node on each. A source backs up over the one its mirrors are on.
#
# Shrooms must be installed and this machine in a mesh (scripts/install.sh).
#
# What it does:
#   - takes shrooms-backup out of its image, ghcr.io/vpavlin/shrooms-backup,
#     into /usr/local/bin (or --binary FILE);
#   - restic, a pinned release checked by its sha256, into /usr/local/bin;
#   - logos-storage, from the same image, into /usr/local/lib/shrooms-backup
#     (or --storage-bin FILE): upstream has no release with the two fixes
#     backups need yet (#1540, #1575), so the image carries CI's build of the
#     pinned commit, for amd64 and arm64 (ADR-010);
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
API_PORT=7394

usage() {
    sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
    cat <<EOF
Options:
  --mirror            keep copies for other machines
  --always-on         say this machine is always on (a server, not a laptop)
  --preset NAME[,…]   back up presets: shrooms, agents, agent-history, foray, keys, services (a list, or repeat)
  --source PATH       back up PATH (repeat for more)
  --exclude PATTERN   leave out what matches (repeat)
  --mirrors A,B       the machines that keep this source's copies
  --times HH:MM,...   when to back up (default 03:00,15:00)
  --keep-local        keep this source's backup in its own node too
  --owners A,B        devices that may restore any backup here and start runs
  --mesh A,B          the meshes to run on, the primary first (default: this
                      machine's only mesh, or the one its mirrors are on)
  --quota-gb N        the most backups may take on this disk, in GB (default 10)
  --binary FILE       use this shrooms-backup instead of the image's
  --storage-bin FILE  use this logos-storage instead of the image's
  --uninstall         stop and remove it (the backups on mirrors stay)
EOF
    exit "${1:-0}"
}

MIRROR=false ALWAYS=false KEEP_LOCAL=false UNINSTALL=0 MESHES="" QUOTA="" TIMES="03:00,15:00"
BINARY="" STORAGE_BIN="" MIRRORS="" OWNERS=""
SOURCES=() EXCLUDES=() PRESETS=() GIVEN=""
while [ $# -gt 0 ]; do
    case $1 in
        --mirror) MIRROR=true; GIVEN+=" mirror"; shift ;;
        --always-on) ALWAYS=true; GIVEN+=" always"; shift ;;
        # A list as well as one at a time: "--preset shrooms,agents" was
        # written into the config as one preset of that name, and the daemon
        # refused to start (2026-11-02).
        --preset) IFS=, read -ra _p <<< "$2"
            for _x in "${_p[@]}"; do
                case "$_x" in
                    shrooms|agents|agent-history|foray|keys|services) PRESETS+=("$_x"); GIVEN+=" what" ;;
                    *) echo "  no preset \"$_x\": shrooms, agents, agent-history, foray, keys, services"; exit 2 ;;
                esac
            done
            shift 2 ;;
        --source) SOURCES+=("$2"); GIVEN+=" what"; shift 2 ;;
        --exclude) EXCLUDES+=("$2"); GIVEN+=" exclude"; shift 2 ;;
        --mirrors) MIRRORS=$2; GIVEN+=" mirrors"; shift 2 ;;
        --times) TIMES=$2; GIVEN+=" times"; shift 2 ;;
        --keep-local) KEEP_LOCAL=true; GIVEN+=" keep"; shift ;;
        --owners) OWNERS=$2; GIVEN+=" owners"; shift 2 ;;
        --mesh|--meshes) MESHES=$2; shift 2 ;;
        --quota-gb) QUOTA=$2; GIVEN+=" quota"; shift 2 ;;
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

# node_port INDEX: a mesh's storage node port, by its place in the list,
# as the daemon gives it (Config.Ports): 8590 for the primary, then +2.
node_port() { echo $(( 8590 + 2 * $1 )); }

firewall() { # add | remove ADDRESS "PORTS"
    local verb=$1 a=$2 ports=$3 p
    if command -v firewall-cmd >/dev/null && firewall-cmd --state >/dev/null 2>&1; then
        for p in $ports; do
            local rule="rule family=ipv6 destination address=$a/128 port port=$p protocol=tcp accept"
            firewall-cmd -q --permanent --"$verb"-rich-rule="$rule" 2>/dev/null || true
        done
        firewall-cmd -q --reload
        [ "$verb" = add ] && echo "  firewalld: $ports open to $a only"
    elif command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
        for p in $ports; do
            if [ "$verb" = add ]; then ufw allow to "$a" port "$p" proto tcp >/dev/null
            else ufw delete allow to "$a" port "$p" proto tcp >/dev/null 2>&1 || true; fi
        done
        [ "$verb" = add ] && echo "  ufw: $ports open to $a only"
    fi
    return 0
}

# config_meshes: the meshes an existing config runs on, one a line.
config_meshes() {
    python3 -c 'import json,sys
c=json.load(open(sys.argv[1]))
print("\n".join(c.get("meshes") or ([c["mesh"]] if c.get("mesh") else [])))' "$1" 2>/dev/null || true
}

# close_all CONFIG: the firewall rules for every mesh it ran on.
close_all() {
    local i=0 m a
    while read -r m; do
        [ -n "$m" ] || continue
        a=$(mesh_addr "$m" || true)
        [ -n "$a" ] && firewall remove "$a" "$API_PORT $(node_port $i)"
        i=$((i + 1))
    done < <(config_meshes "$1")
}

if [ $UNINSTALL -eq 1 ]; then
    echo "==> removing Shrooms Backup"
    systemctl disable --now shrooms-backup 2>/dev/null || true
    rm -f /etc/systemd/system/shrooms-backup.service
    systemctl daemon-reload
    for f in /etc/shrooms-backup/config.json.firewall /etc/shrooms-backup/config.json; do
        [ -f "$f" ] && { close_all "$f"; rm -f /etc/shrooms-backup/config.json.firewall; echo "  firewall rules closed"; break; }
    done
    rm -f /usr/local/bin/shrooms-backup
    echo "  kept: /etc/shrooms-backup (the config and the key: the key is the only way to read this"
    echo "  machine's backups), /var/lib/shrooms-backup (copies kept for others), restic, logos-storage"
    exit 0
fi

echo "==> checking this machine"
[ -S "$SOCK" ] || { echo "  no shrooms daemon here: install shrooms first (scripts/install.sh)"; exit 1; }
command -v python3 >/dev/null || { echo "  needs python3"; exit 1; }
CONFIG=/etc/shrooms-backup/config.json
OLD_MESHES=""
[ -f "$CONFIG" ] && OLD_MESHES=$(config_meshes "$CONFIG" | paste -sd, -)
# Installed before the rules were recorded: they were opened for this config.
[ -f "$CONFIG" ] && [ ! -f "$CONFIG.firewall" ] && cp "$CONFIG" "$CONFIG.firewall"
# Run again without --mesh, the meshes it ran on.
[ -n "$MESHES" ] || MESHES=$OLD_MESHES
# Without --mesh on a first install: the one mesh this machine is on, or the
# one its mirrors are all on. Never simply the first: on the laptop that was
# "default", where no mirror is, and its backups and the apps could not reach
# it (2026-11-02).
if [ -z "$MESHES" ]; then
    MESHES=$(status | python3 -c 'import json,sys
st=json.load(sys.stdin); want=[m for m in sys.argv[1].split(",") if m]
ms=[m["label"] for m in st.get("meshes") or [] if m.get("overlay") and not m.get("disabled")]
if len(ms)==1: print(ms[0]); sys.exit()
on=lambda l: {p["name"] for p in st.get("peers") or [] if p.get("mesh")==l}
fit=[l for l in ms if want and set(want) <= on(l)]
print(fit[0] if len(fit)==1 else "")' "$MIRRORS")
    [ -n "$MESHES" ] || { echo "  which meshes do backups run on? This machine is on several; give --mesh A[,B] (the one its mirrors are on first)"; exit 2; }
fi
IFS=, read -ra MESH_LIST <<< "$MESHES"
on_some=0
for m in "${MESH_LIST[@]}"; do
    a=$(mesh_addr "$m")
    if [ -n "$a" ]; then echo "  on mesh $m at $a"; on_some=1
    else echo "  not on mesh \"$m\" (yet): the daemon serves the others and starts again when it joins"; fi
done
[ $on_some -eq 1 ] || { echo "  not on any of: $MESHES"; exit 1; }
NSRC=$(( ${#SOURCES[@]} + ${#PRESETS[@]} ))
# Run again on a machine already set up, it changes only what it is given
# and keeps the rest of the config (and so whatever the apps set): having to
# repeat every option to move the laptop to another mesh was confusing.
if [ ! -f "$CONFIG" ]; then
    [ $NSRC -gt 0 ] || [ "$MIRROR" = true ] || { echo "  nothing to do: --mirror, or --preset/--source with --mirrors"; usage 2; }
    [ $NSRC -eq 0 ] || [ -n "$MIRRORS" ] || { echo "  a source needs --mirrors"; exit 2; }
fi
# The mesh a source backs up over: the first of them its mirrors are all on
# (its copies are fetched from its node there).
SRC_MIRRORS=$MIRRORS
[ -n "$SRC_MIRRORS" ] || [ ! -f "$CONFIG" ] || SRC_MIRRORS=$(python3 -c 'import json,sys
print(",".join((json.load(open(sys.argv[1])).get("source") or {}).get("mirrors") or []))' "$CONFIG" 2>/dev/null || true)
SOURCE_MESH=""
if [ -n "$SRC_MIRRORS" ]; then
    SOURCE_MESH=$(status | python3 -c 'import json,sys
st=json.load(sys.stdin); want=[m for m in sys.argv[1].split(",") if m]
on=lambda l: {p["name"] for p in st.get("peers") or [] if p.get("mesh")==l}
fit=[l for l in sys.argv[2].split(",") if set(want) <= on(l)]
print(fit[0] if fit else "")' "$SRC_MIRRORS" "$MESHES")
    [ -n "$SOURCE_MESH" ] || { echo "  no one of $MESHES has all of $SRC_MIRRORS on it: a source's mirrors must share a mesh with it"; exit 2; }
    echo "  backs up over $SOURCE_MESH, the mesh its mirrors ($SRC_MIRRORS) are on"
fi
ARCH=$(uname -m); case $ARCH in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac

# Both programs come out of one image, unless both are given: logos-storage
# came from a server on our office mesh once, and a machine on any other mesh
# could not install (k11, 2026-11-04).
cid=
if [ -z "$BINARY" ] || [ -z "$STORAGE_BIN" ]; then
    RUNTIME=$(command -v podman || command -v docker || true)
    [ -n "$RUNTIME" ] || { echo "  neither docker nor podman: give both --binary and --storage-bin"; exit 1; }
    "$RUNTIME" pull -q "$IMAGE" >/dev/null 2>&1 || "$RUNTIME" image inspect "$IMAGE" >/dev/null 2>&1 ||
        { echo "  could not get $IMAGE"; exit 1; }
    cid=$("$RUNTIME" create "$IMAGE")
    trap '"$RUNTIME" rm -f "$cid" >/dev/null 2>&1 || true' EXIT
fi
from_image() { # PATH-IN-IMAGE DEST
    local tmp; tmp=$(mktemp)
    "$RUNTIME" cp "$cid:$1" "$tmp" || { echo "  $IMAGE has no $1: an image from before ADR-010?"; rm -f "$tmp"; exit 1; }
    install -m 0755 "$tmp" "$2"
    rm -f "$tmp"
}

echo "==> shrooms-backup"
if [ -n "$BINARY" ]; then
    install -m 0755 "$BINARY" /usr/local/bin/shrooms-backup
else
    from_image /usr/bin/shrooms-backup /usr/local/bin/shrooms-backup
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
else
    from_image /usr/lib/shrooms-backup/logos-storage /usr/local/lib/shrooms-backup/logos-storage
fi
/usr/local/lib/shrooms-backup/logos-storage --version >/dev/null 2>&1 ||
    { echo "  logos-storage does not run here: it is built on Ubuntu 24.04 and needs a glibc and libstdc++ as new"; exit 1; }
command -v restorecon >/dev/null && restorecon /usr/local/lib/shrooms-backup/logos-storage 2>/dev/null || true

echo "==> the config"
install -d -m 0700 /etc/shrooms-backup
[ -n "$QUOTA" ] || QUOTA=10
python3 - "$MESHES" "$MIRROR" "$ALWAYS" "$OWNERS" "$QUOTA" "$MIRRORS" "$TIMES" "$KEEP_LOCAL" \
    "$(printf '%s\n' "${SOURCES[@]+"${SOURCES[@]}"}")" "$(printf '%s\n' "${EXCLUDES[@]+"${EXCLUDES[@]}"}")" \
    "$(printf '%s\n' "${PRESETS[@]+"${PRESETS[@]}"}")" "$GIVEN" "$SOURCE_MESH" <<'PY'
import json, os, sys
meshes, mirror, always, owners, quota, mirrors, times, keep, sources, excludes, presets, given, source_mesh = sys.argv[1:]
given = set(given.split())
lst = lambda s, sep=",": [x.strip() for x in s.split(sep) if x.strip()]
path = "/etc/shrooms-backup/config.json"
old = json.load(open(path)) if os.path.exists(path) else None
c = old or {"mirror": False, "always_on": False, "owners": [], "node": {}, "restic": "/usr/local/bin/restic"}
c["meshes"] = lst(meshes)
c["mesh"] = c["meshes"][0]
if old is None or "mirror" in given: c["mirror"] = mirror == "true"
if old is None or "always" in given: c["always_on"] = always == "true"
if old is None or "owners" in given: c["owners"] = lst(owners)
if old is None or "quota" in given: c.setdefault("node", {})["quota_gb"] = int(quota)
s = c.get("source")
if "what" in given:
    s = s or {"times": lst(times), "keep_local": False}
    s["presets"], s["paths"] = lst(presets, "\n"), lst(sources, "\n")
if s is not None:
    if "exclude" in given or "exclude" not in s: s["exclude"] = lst(excludes, "\n")
    if "mirrors" in given or not s.get("mirrors"): s["mirrors"] = lst(mirrors)
    if "times" in given or not s.get("times"): s["times"] = lst(times)
    if "keep" in given: s["keep_local"] = True
    if not s["mirrors"]: sys.exit("  a source needs --mirrors")
    # Over its mirrors' mesh; the primary is the default and written as none.
    if source_mesh and source_mesh != c["meshes"][0]: s["mesh"] = source_mesh
    else: s.pop("mesh", None)
    c["source"] = s
json.dump(c, open(path, "w"), indent=2)
print("  " + ", ".join(filter(None, ["a mirror" if c["mirror"] else "",
      ("a source of " + " ".join(c["source"]["presets"] + c["source"]["paths"]) + " to " + ", ".join(c["source"]["mirrors"])) if "source" in c else ""])))
PY
chmod 600 /etc/shrooms-backup/config.json

echo "==> the firewall"
# What it opened before, then what it needs now: a mesh left, or the order
# changed (a node's port follows its place), leaves nothing open behind.
[ -n "$OLD_MESHES" ] && [ -f "$CONFIG.firewall" ] && close_all "$CONFIG.firewall"
i=0
for m in "${MESH_LIST[@]}"; do
    a=$(mesh_addr "$m")
    [ -n "$a" ] && firewall add "$a" "$API_PORT $(node_port $i)"
    i=$((i + 1))
done
# What the rules were made for, so the next run closes exactly those.
cp "$CONFIG" "$CONFIG.firewall"

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
