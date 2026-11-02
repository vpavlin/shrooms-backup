import QtQuick

// Loads the view offscreen against a fake core and prints what it
// understood, then a picture of each tab. Run by check.sh.
Item {
    id: h
    width: 1100; height: 800

    property var calls: []
    readonly property var now: new Date()
    function iso(minAgo) { return new Date(now.getTime() - minAgo * 60000).toISOString() }

    readonly property var infos: ({
        "local": { name: "jimmy-crib", mirror: false, always_on: false, quota: 10737418240, used: 0, quota_gb: 10, owners: [],
                   owner: true, key: true, running: false, next: new Date(now.getTime() + 3600000).toISOString(), holds: [], held: [],
                   source: { presets: ["shrooms", "keys"], paths: ["/var/tmp/shrooms-backup-test"], times: ["03:30", "15:30"],
                             mirrors: ["proteus", "scribe", "vps"], keep_daily: 14, keep_weekly: 12 },
                   last_run: { started: iso(62), finished: iso(60), ok: true, added: "696 B", data_added: 696, stored: 40569511, files: 812,
                               mirrors: { proteus: "ok", scribe: "ok", vps: "ok" } } },
        "fd00::2": { name: "proteus", mirror: true, always_on: true, quota: 214748364800, used: 40569511, quota_gb: 200, owners: ["jimmy-crib"],
                     owner: false, holds: ["jimmy-crib"], held: [{ source: "jimmy-crib", adopted: 9, pending: 9, missing: 0, bytes: 40569511 }] },
        "fd00::3": { name: "vps", mirror: true, always_on: true, quota: 42949672960, used: 40372402, quota_gb: 40, owners: [],
                     owner: true, holds: ["jimmy-crib"], held: [{ source: "jimmy-crib", adopted: 8, pending: 9, missing: 3, bytes: 40372402 }] },
        "fd00::4": { name: "atlas", mirror: false, always_on: true, quota: 0, used: 0, owner: true, holds: [], held: [], running: false,
                     source: { presets: ["agents"], times: ["04:00"], mirrors: ["scribe"], keep_daily: 14, keep_weekly: 12 },
                     last_run: { started: iso(300), finished: iso(299), ok: false, error: "no key (/etc/shrooms-backup/key)", mirrors: {} } }
    })
    function found() {
        var out = []
        for (var a in infos) out.push({ address: a, peer: a === "local" ? "" : infos[a].name, mesh: "office", info: infos[a] })
        out.push({ address: "fd00::9", peer: "jimmy-crib", mesh: "home", info: infos["local"] })   // the same machine on another mesh
        return out
    }
    function runs() {
        var out = []
        for (var i = 0; i < 30; i++)
            out.push({ started: iso(60 * (30 - i)), finished: iso(60 * (30 - i) - 1 - i % 3), ok: i !== 17,
                       error: i === 17 ? "restic backup: exit status 1" : undefined,
                       data_added: (i * 7919) % 50000, stored: 40000000 + i * 20000, files: 812, files_new: i % 4, files_changed: i % 3, added: "x" })
        return out
    }

    QtObject {
        id: fake
        function callModule(module, method, args) {
            h.calls.push(module + "." + method + " " + JSON.stringify(args))
            if (module === "shrooms_core" && method === "status")
                return JSON.stringify({ name: "duet", meshes: [{ label: "office", overlay: "fd00::1" }],
                                        peers: [{ name: "proteus", mesh: "office", overlay: "fd00::2", online: true },
                                                { name: "vps", mesh: "office", overlay: "fd00::3", online: true },
                                                { name: "atlas", mesh: "office", overlay: "fd00::4", online: true },
                                                { name: "gone", mesh: "office", overlay: "fd00::5", online: false }] })
            if (module !== "shrooms_backup_core") return ""
            if (method === "find" || method === "found") return JSON.stringify(JSON.stringify(h.found()))   // double-wrapped, as the bridge may
            if (method === "get" && args[1] === "/v1/presets")
                return JSON.stringify({ shrooms: { about: "the mesh identity" }, keys: { about: "SSH, GPG" }, agents: { about: "Shrooms Agents" } })
            if (method === "get" && args[1] === "/v1/runs") return JSON.stringify({ runs: h.runs() })
            if (method === "get" && args[1] === "/v1/keys") return JSON.stringify({ keys: [
                { id: "a1b2c3d4", label: "Keycard 6e021332", created: "2026-11-02T08:38:17+01:00", current: true },
                { id: "e5f6a7b8", label: "passphrase", created: "2026-11-01T21:30:00+01:00", current: false }] })
            if (method === "send") return JSON.stringify({ saved: true, restart: false })
            return JSON.stringify({ error: "unexpected " + method })
        }
    }

    Main { id: view; anchors.fill: parent; bridge: fake }

    property int step: 0
    Timer {
        interval: 1500; running: true; repeat: true
        onTriggered: {
            var out = Qt.application.arguments[Qt.application.arguments.length - 1]
            if (h.step === 0) {
                console.error("MACHINES=" + view.machines.map(function(m) { return m.name + (m.local ? "@local" : "@" + m.address) }).join(","))
                console.error("SELF=" + view.self + " SELECTED=" + view.selected + " PROBLEM=[" + view.problem + "]")
                console.error("PRESETS=" + Object.keys(view.presets).join(","))
                console.error("COPIES=" + view.copiesOf("jimmy-crib").map(function(c) { return c.mirror + ":" + view.copyText(c) }).join(" | "))
                view.grabToImage(function(r) { r.saveToFile(out + "/overview.png") })
            } else if (h.step === 1) {
                view.tab = 1
            } else if (h.step === 2) {
                console.error("RUNS=" + view.runs.length + " OF=" + view.runsOf)
                view.grabToImage(function(r) { r.saveToFile(out + "/metrics.png") })
            } else if (h.step === 3) {
                view.tab = 2
            } else if (h.step === 4) {
                view.grabToImage(function(r) { r.saveToFile(out + "/settings.png") })
                view.save(view.current, { owners: ["duet"] })
                console.error("SENT=" + h.calls.filter(function(c) { return c.indexOf(".send") > 0 }).join(" ; "))
                console.error("NOTICE=" + view.notice)
            } else {
                Qt.quit()
            }
            h.step++
        }
    }
}
