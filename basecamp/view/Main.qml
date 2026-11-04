import QtQuick
import QtQuick.Controls
import QtQuick.Layouts

// Shrooms Backup in Basecamp: every backup daemon this device can reach, what
// each backs up and where its copies are (Overview), how its runs went
// (Metrics), and an owner's settings (Settings).
//
// shrooms_backup_core does the talking, since a view may not open a
// connection: to this machine's daemon on loopback and to the others on their
// mesh addresses, which shrooms_core's status names. The daemons decide who
// may change what; a device not among a machine's owners sees it read-only.
//
// Plain QtQuick in the Shrooms palette rather than Logos.Controls, for the
// Shrooms view's reason: the bundled design system differs between Basecamp
// versions, and a view that fails to load on one says nothing at all.
Item {
    id: root
    anchors.fill: parent

    readonly property color cVoid: "#07090B"
    readonly property color cPanel: "#0E1216"
    readonly property color cRaised: "#151a20"
    readonly property color cLine: "#1C2229"
    readonly property color cAsh: "#6B7680"
    readonly property color cBone: "#D6DDE3"
    readonly property color cPhosphor: "#35F0A0"
    readonly property color cAmber: "#F0B429"
    readonly property color cRust: "#E05252"
    readonly property color cViolet: "#9A7BFF"
    readonly property color cSky: "#5AA9FF"

    // The harness sets its own; in Basecamp it is the host's.
    property var bridge: typeof logos !== "undefined" ? logos : null
    readonly property bool haveCore: !!bridge && !!bridge.callModule

    property string self: ""          // this device's mesh name
    property var peerNames: []        // every device on its meshes, online or not
    property string problem: ""
    property var machines: []         // [{key, address, name, mesh, local, info}]
    property string selected: ""      // a machine's name
    property var runs: []             // the selected machine's
    property var presets: ({})
    property string runsOf: ""
    property int tab: 0
    property string notice: ""
    property color noticeColour: cAsh

    // --- the core ----------------------------------------------------------------

    function call(module, method, args) {
        if (!haveCore) return null
        var t = ""
        try { t = String(bridge.callModule(module, method, args || []) || "").trim() } catch (e) { return null }
        for (var i = 0; i < 2 && t.charAt(0) === '"'; i++) {
            try { t = String(JSON.parse(t)).trim() } catch (e2) { break }
        }
        if (t.charAt(0) !== "{" && t.charAt(0) !== "[") return null
        try { return JSON.parse(t) } catch (e3) { return null }
    }
    function core(method, args) { return call("shrooms_backup_core", method, args) }

    function peersArg() {
        var st = call("shrooms_core", "status", [])
        if (!st || st.error) {
            root.problem = st && st.error ? "Shrooms is not running here: " + st.error
                                          : "Shrooms Backup needs the Shrooms app in Basecamp to find your other machines."
            return ""
        }
        root.problem = ""
        root.self = st.name || ""
        var out = [], ps = st.peers || [], names = {}
        for (var k = 0; k < ps.length; k++) if (ps[k].name) names[ps[k].name] = true
        root.peerNames = Object.keys(names).sort()
        for (var j = 0; j < ps.length; j++)
            if (ps[j].online !== false && ps[j].overlay) out.push(ps[j].name + "|" + (ps[j].mesh || "") + "|" + ps[j].overlay)
        return out.join(";")
    }

    // One entry a machine: one on two meshes answers on both; this machine's
    // own daemon, on loopback, is the one that knows it as the owner. For any
    // other, the answer that says this device is an owner: a machine on
    // several meshes knows a bare-named owner on its primary only (ADR-011),
    // and the first answer made its settings read-only or not by chance.
    function readFound(found) {
        var byName = {}, order = []
        for (var i = 0; i < (found || []).length; i++) {
            var f = found[i], info = f.info || {}, name = info.name || f.peer
            if (!name) continue
            var m = { key: name, address: f.address, name: name, mesh: f.mesh, local: f.address === "local", info: info }
            if (!(name in byName)) { order.push(name); byName[name] = m }
            else if (m.local) byName[name] = m
            else if (!byName[name].local && info.owner && !byName[name].info.owner) byName[name] = m
        }
        order.sort(function(a, b) {
            var ma = byName[a], mb = byName[b]
            if (ma.local !== mb.local) return ma.local ? -1 : 1
            return a < b ? -1 : a > b ? 1 : 0
        })
        return order.map(function(n) { return byName[n] })
    }

    function find() {
        if (!haveCore) { root.problem = "Shrooms Backup's core module is missing: install shrooms_backup_core."; return }
        var f = core("find", [peersArg()])
        if (f === null) { root.problem = "Shrooms Backup's core module is missing: install shrooms_backup_core and restart Basecamp."; return }
        setMachines(readFound(f))
    }
    function poll() {
        var f = core("found", [])
        if (f !== null) setMachines(readFound(f))
    }
    function setMachines(ms) {
        // Keep the machines known while they are asked again: a poll between
        // two answers must not empty the screen.
        if (ms.length === 0 && machines.length > 0) return
        machines = ms
        if (selected === "" || !machineNamed(selected)) selected = ms.length > 0 ? ms[0].name : ""
        if (Object.keys(presets).length === 0 && ms.length > 0) {
            var p = core("get", [ms[0].address, "/v1/presets"])
            if (p && !p.error) presets = p
        }
    }
    function machineNamed(n) {
        for (var i = 0; i < machines.length; i++) if (machines[i].name === n) return machines[i]
        return null
    }
    readonly property var current: machineNamed(selected)

    function loadRuns() {
        var m = current
        if (!m) { runs = []; return }
        var r = core("get", [m.address, "/v1/runs"])
        if (r && !r.error) { runs = r.runs || []; runsOf = m.name }
    }

    function say(text, colour) { notice = text; noticeColour = colour || cAsh; noticeTimer.restart() }

    function runNow(m) {
        var r = core("send", [m.address, "POST", "/v1/run", "{}"])
        if (!r || r.error) say(m.name + ": " + (r ? r.error : "the core did not answer"), cRust)
        else { say(m.name + " is backing up now", cPhosphor); Qt.callLater(find) }
    }

    function save(m, settings) {
        var r = core("send", [m.address, "PUT", "/v1/config", JSON.stringify(settings)])
        if (!r || r.error) { say(m.name + ": " + (r ? r.error : "the core did not answer"), cRust); return false }
        say(r.restart ? m.name + " saved; its daemon restarts, back in about 20 s" : m.name + " saved", cPhosphor)
        Qt.callLater(find)
        return true
    }

    // --- words and numbers -----------------------------------------------------------

    // A run under way, as bars: restic reading, then each mirror fetching
    // (the daemon's progress; mobile/src/model.ts progressBars says the same).
    function left(s) {
        if (s < 60) return "under a minute"
        if (s < 90 * 60) return "about " + Math.round(s / 60) + " min"
        return "about " + (s / 3600).toFixed(s < 36000 ? 1 : 0) + " h"
    }
    function progressBars(info) {
        var p = info ? info.progress : null
        if (!info || !info.running || !p) return []
        if (p.phase === "reading") {
            if (!p.bytes) return [{ label: "Reading files...", fraction: 0, colour: cSky }]
            return [{ label: "Reading files: " + size(p.bytes_done || 0) + " of " + size(p.bytes)
                             + (p.seconds_remaining ? ", " + left(p.seconds_remaining) + " left" : ""),
                      fraction: Math.min((p.bytes_done || 0) / p.bytes, 1), colour: cSky }]
        }
        var ms = p.mirrors || {}, out = []
        Object.keys(ms).sort().forEach(function(n) {
            var m = ms[n], f = m.bytes ? Math.min(m.done / m.bytes, 1) : 0
            if (m.error) out.push({ label: n + ": " + m.error, fraction: f, colour: cRust })
            else if (m.ok) out.push({ label: n + ": holds this run", fraction: 1, colour: cPhosphor })
            else out.push({ label: n + ": " + size(m.done) + " of " + size(m.bytes), fraction: f, colour: cSky })
        })
        return out
    }

    function size(n) {
        if (n === undefined || n === null) return "-"
        var u = ["B", "KB", "MB", "GB", "TB"], i = 0, f = n
        while (f >= 1000 && i < u.length - 1) { f /= 1000; i++ }
        return (i === 0 ? f : f.toFixed(f < 10 ? 1 : 0)) + " " + u[i]
    }
    function when(iso) {
        if (!iso || iso.indexOf("0001-") === 0) return "never"
        var t = new Date(iso).getTime(), d = (Date.now() - t) / 1000, future = d < 0
        d = Math.abs(d)
        var s = d < 90 ? Math.round(d) + " s" : d < 5400 ? Math.round(d / 60) + " min"
              : d < 129600 ? Math.round(d / 3600) + " h" : Math.round(d / 86400) + " days"
        return future ? "in " + s : s + " ago"
    }
    function took(run) {
        if (!run || !run.finished || run.finished.indexOf("0001-") === 0) return "-"
        var s = (new Date(run.finished).getTime() - new Date(run.started).getTime()) / 1000
        return s < 90 ? Math.round(s) + " s" : Math.round(s / 60) + " min"
    }
    function seconds(run) {
        return Math.max(0, (new Date(run.finished).getTime() - new Date(run.started).getTime()) / 1000)
    }
    function whatOf(source) {
        var w = (source.presets || []).slice(), n = (source.paths || []).length
        if (n) w.push(n === 1 ? source.paths[0] : n + " folders")
        return w.join(", ")
    }
    // A run from before the daemon kept restic's summary has its words only.
    function runText(r) {
        if (!r.ok) return r.error || "failed"
        var t = (r.added || "nothing") + " new"
        if (r.files !== undefined)
            t += "; " + (r.files_new || 0) + " new and " + (r.files_changed || 0) + " changed of " + r.files + " files"
        return t
    }
    function roleOf(info) {
        var r = []
        if (info.source) r.push("backs up")
        if (info.mirror) r.push("mirror")
        return r.length ? r.join(" + ") : "idle"
    }
    function stateColour(info) {
        if (info.running) return cSky
        if (info.source && info.last_run && !info.last_run.ok) return cRust
        if (info.source && !info.last_run) return cAmber
        return cPhosphor
    }
    // Where a source's copies are, as its mirrors say: each mirror's daemon
    // reports how far it is with every source it holds.
    function copiesOf(name) {
        var out = []
        for (var i = 0; i < machines.length; i++) {
            var held = machines[i].info.held || []
            for (var j = 0; j < held.length; j++)
                if (held[j].source === name) out.push({ mirror: machines[i].name, status: held[j], alwaysOn: machines[i].info.always_on })
        }
        return out
    }
    function copyText(c) {
        var s = c.status
        if (s.error) return s.error
        if (s.pending > s.adopted) return "fetching, " + s.missing + " objects to go"
        return "in step (" + size(s.bytes) + ")"
    }
    function copyColour(c) {
        var s = c.status
        if (s.error) return cRust
        if (s.pending > s.adopted) return cAmber
        return cPhosphor
    }

    Timer { id: noticeTimer; interval: 8000; onTriggered: root.notice = "" }
    // Ask again for the machines now and then (peers come and go); read the
    // answers often, since they arrive one by one.
    Timer { interval: 15000; running: true; repeat: true; triggeredOnStart: true; onTriggered: Qt.callLater(root.find) }
    Timer { interval: 2500; running: true; repeat: true; onTriggered: root.poll() }
    Timer { interval: 20000; running: root.tab === 1; repeat: true; triggeredOnStart: true; onTriggered: Qt.callLater(root.loadRuns) }
    onSelectedChanged: if (tab === 1) Qt.callLater(loadRuns)
    onTabChanged: if (tab === 1) Qt.callLater(loadRuns)

    // --- pieces ----------------------------------------------------------------------

    component Label_: Text { color: root.cBone; font.pixelSize: 13; wrapMode: Text.Wrap }
    component Dim: Text { color: root.cAsh; font.pixelSize: 12; wrapMode: Text.Wrap }
    component Heading: Text { color: root.cBone; font.pixelSize: 15; font.bold: true }
    component Dot: Rectangle { width: 9; height: 9; radius: 4.5 }
    component Panel: Rectangle { color: root.cPanel; border.color: root.cLine; radius: 6 }
    component Btn: Rectangle {
        id: b
        property string text: ""
        property bool primary: false
        property bool enabled_: true
        signal clicked()
        implicitWidth: t.implicitWidth + 24; implicitHeight: 30; radius: 4
        color: !enabled_ ? root.cLine : primary ? (ma.containsMouse ? "#2bd18a" : root.cPhosphor) : (ma.containsMouse ? root.cLine : root.cRaised)
        border.color: primary ? "transparent" : root.cLine
        Text { id: t; anchors.centerIn: parent; text: b.text; font.pixelSize: 12; color: b.primary && b.enabled_ ? root.cVoid : b.enabled_ ? root.cBone : root.cAsh }
        MouseArea { id: ma; anchors.fill: parent; hoverEnabled: true; enabled: b.enabled_; cursorShape: Qt.PointingHandCursor; onClicked: b.clicked() }
    }
    component Field: TextField {
        color: root.cBone; font.pixelSize: 13; selectByMouse: true
        placeholderTextColor: root.cAsh
        background: Rectangle { color: root.cVoid; border.color: parent.activeFocus ? root.cSky : root.cLine; radius: 4; implicitHeight: 30 }
    }
    component Check: CheckBox {
        id: cb
        property string label: ""
        property string about: ""
        width: parent ? parent.width : implicitWidth
        spacing: 8
        indicator: Rectangle {
            x: cb.leftPadding; y: cb.topPadding + 2
            width: 16; height: 16; radius: 3; color: root.cVoid
            border.color: cb.checked ? root.cPhosphor : root.cLine
            Rectangle { anchors.centerIn: parent; width: 8; height: 8; radius: 2; color: root.cPhosphor; visible: cb.checked }
        }
        contentItem: Column {
            leftPadding: cb.indicator.width + cb.spacing
            Text { text: cb.label; color: cb.enabled ? root.cBone : root.cAsh; font.pixelSize: 13 }
            Text { text: cb.about; color: root.cAsh; font.pixelSize: 11; visible: cb.about !== ""; wrapMode: Text.Wrap; width: cb.availableWidth - cb.indicator.width - cb.spacing }
        }
    }

    // A bar chart of the runs, oldest at the left; a failed run is red.
    component Bars: Column {
        id: bars
        property string title: ""
        property var values: []        // numbers
        property var failed: []        // bools
        property var labelOf: function(v) { return String(v) }
        property color colour: root.cSky
        // Sizes grow slowly: scaled from zero, every bar is the same height.
        property bool fromZero: true
        spacing: 4
        Row {
            spacing: 8
            Text { text: bars.title; color: root.cBone; font.pixelSize: 13 }
            Text {
                color: root.cAsh; font.pixelSize: 12
                text: bars.values.length ? "last " + bars.labelOf(bars.values[bars.values.length - 1])
                      + ", most " + bars.labelOf(Math.max.apply(null, bars.values)) : "no runs yet"
            }
        }
        Canvas {
            id: cv
            width: bars.width; height: 90
            property var v: bars.values
            onVChanged: requestPaint()
            onWidthChanged: requestPaint()
            onPaint: {
                var ctx = getContext("2d")
                ctx.reset()
                ctx.fillStyle = root.cVoid
                ctx.fillRect(0, 0, width, height)
                var n = v.length
                if (n === 0) return
                var max = Math.max.apply(null, v) || 1
                var min = bars.fromZero ? 0 : Math.min.apply(null, v)
                // Never closer than 5% of the most: 2 KB on 40 MB is no step.
                var base = min - Math.max((max - min) * 0.1, max * 0.05)
                if (max - base <= 0) base = max - 1
                var w = width / Math.max(n, 20)
                for (var i = 0; i < n; i++) {
                    var h = Math.max(1, ((v[i] - base) / (max - base)) * (height - 6))
                    ctx.fillStyle = bars.failed[i] ? root.cRust : bars.colour
                    ctx.fillRect(i * w + 1, height - h, Math.max(1, w - 2), h)
                }
            }
        }
    }

    // --- layout ----------------------------------------------------------------------

    Rectangle { anchors.fill: parent; color: root.cVoid }

    ColumnLayout {
        anchors.fill: parent
        anchors.margins: 16
        spacing: 12

        RowLayout {
            Layout.fillWidth: true
            spacing: 16
            Text { text: "Shrooms Backup"; color: root.cBone; font.pixelSize: 20; font.bold: true }
            Repeater {
                model: ["Overview", "Metrics", "Settings"]
                delegate: Text {
                    required property int index
                    required property string modelData
                    text: modelData
                    color: root.tab === index ? root.cPhosphor : root.cAsh
                    font.pixelSize: 14; font.bold: root.tab === index
                    MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: root.tab = index }
                }
            }
            Item { Layout.fillWidth: true }
            Text { text: root.notice; color: root.noticeColour; font.pixelSize: 12; elide: Text.ElideRight; Layout.maximumWidth: 420 }
        }

        Text {
            visible: root.problem !== ""
            text: root.problem; color: root.cAmber; font.pixelSize: 13; wrapMode: Text.Wrap
            Layout.fillWidth: true
        }
        Text {
            visible: root.problem === "" && root.machines.length === 0
            text: "Looking for backup daemons on this machine and your meshes... A machine runs one once shrooms-backup is installed on it."
            color: root.cAsh; font.pixelSize: 13; wrapMode: Text.Wrap
            Layout.fillWidth: true
        }

        RowLayout {
            Layout.fillWidth: true
            Layout.fillHeight: true
            spacing: 12
            visible: root.machines.length > 0

            // The machines, in every tab: Overview reads them all, the other
            // two the one picked here.
            Panel {
                Layout.preferredWidth: 210
                Layout.fillHeight: true
                ListView {
                    anchors.fill: parent; anchors.margins: 6
                    clip: true; spacing: 2
                    model: root.machines
                    delegate: Rectangle {
                        required property var modelData
                        width: ListView.view.width; height: 48; radius: 4
                        color: root.selected === modelData.name ? root.cRaised : "transparent"
                        Row {
                            anchors.verticalCenter: parent.verticalCenter; x: 8; spacing: 8
                            Dot { anchors.verticalCenter: parent.verticalCenter; color: root.stateColour(modelData.info) }
                            Column {
                                Text { text: modelData.name + (modelData.local ? "  (here)" : ""); color: root.cBone; font.pixelSize: 13 }
                                Text { text: root.roleOf(modelData.info) + (modelData.info.owner ? "" : " - read only"); color: root.cAsh; font.pixelSize: 11 }
                            }
                        }
                        MouseArea { anchors.fill: parent; cursorShape: Qt.PointingHandCursor; onClicked: root.selected = modelData.name }
                    }
                }
            }

            StackLayout {
                Layout.fillWidth: true
                Layout.fillHeight: true
                currentIndex: root.tab

                // --- Overview --------------------------------------------------------
                ScrollView {
                    id: ov
                    clip: true
                    contentWidth: availableWidth
                    Column {
                        width: ov.availableWidth
                        spacing: 12
                        Heading { text: "Sources" }
                        Dim { text: "No machine backs anything up yet."; visible: root.machines.filter(function(m) { return !!m.info.source }).length === 0 }
                        Repeater {
                            model: root.machines.filter(function(m) { return !!m.info.source })
                            delegate: Panel {
                                id: src
                                required property var modelData
                                readonly property var info: modelData.info
                                readonly property var last: info.last_run || null
                                width: ov.availableWidth; height: sc.implicitHeight + 24
                                Column {
                                    id: sc
                                    x: 12; y: 12; width: parent.width - 24; spacing: 6
                                    RowLayout {
                                        width: parent.width
                                        Dot { color: root.stateColour(src.info) }
                                        Text { text: src.modelData.name; color: root.cBone; font.pixelSize: 15; font.bold: true }
                                        Dim {
                                            Layout.fillWidth: true
                                            text: root.whatOf(src.info.source)
                                        }
                                        Btn {
                                            text: src.info.running ? "Backing up..." : "Back up now"
                                            enabled_: src.info.owner && !src.info.running
                                            onClicked: root.runNow(src.modelData)
                                        }
                                    }
                                    Label_ {
                                        width: parent.width
                                        text: src.info.running ? "Backing up now."
                                              : !src.last ? "Not backed up yet."
                                              : src.last.ok ? "Last backed up " + root.when(src.last.finished) + ": " + (src.last.added || "nothing") + " new, " + root.size(src.last.stored) + " in all, took " + root.took(src.last) + "."
                                              : "The last run, " + root.when(src.last.started) + ", failed: " + src.last.error
                                        color: src.last && !src.last.ok && !src.info.running ? root.cRust : root.cBone
                                    }
                                    Repeater {
                                        model: root.progressBars(src.info)
                                        delegate: Column {
                                            required property var modelData
                                            width: sc.width; spacing: 3
                                            Dim { width: parent.width; text: modelData.label; color: modelData.colour === root.cRust ? root.cRust : root.cAsh }
                                            Rectangle {
                                                width: parent.width; height: 6; radius: 3; color: root.cLine
                                                Rectangle { width: parent.width * modelData.fraction; height: parent.height; radius: 3; color: modelData.colour }
                                            }
                                        }
                                    }
                                    Dim { text: "Next " + root.when(src.info.next) + " (at " + (src.info.source.times || []).join(", ") + "); keeps " + src.info.source.keep_daily + " days and " + src.info.source.keep_weekly + " weeks" }
                                    Repeater {
                                        model: src.info.source.mirrors || []
                                        delegate: Row {
                                            required property string modelData
                                            readonly property var copy: {
                                                var cs = root.copiesOf(src.modelData.name)
                                                for (var i = 0; i < cs.length; i++) if (cs[i].mirror === modelData) return cs[i]
                                                return null
                                            }
                                            readonly property string lastSays: src.last && src.last.mirrors ? (src.last.mirrors[modelData] || "") : ""
                                            spacing: 8
                                            Dot {
                                                anchors.verticalCenter: parent.verticalCenter
                                                color: copy ? root.copyColour(copy) : lastSays === "ok" ? root.cPhosphor : lastSays ? root.cRust : root.machineNamed(modelData) ? root.cAmber : root.cAsh
                                            }
                                            Text { text: modelData; color: root.cBone; font.pixelSize: 13; width: 110 }
                                            Dim {
                                                text: copy ? root.copyText(copy) + (copy.alwaysOn ? "" : ", not always on")
                                                      : lastSays === "ok" ? (root.machineNamed(modelData) ? "had the last run" : "had the last run (not reachable from here now)")
                                                      : lastSays ? lastSays
                                                      : root.machineNamed(modelData) ? "no copy yet" : "not reachable from here"
                                            }
                                        }
                                    }
                                }
                            }
                        }

                        Heading { text: "Mirrors" }
                        Dim { text: "No machine keeps copies yet."; visible: root.machines.filter(function(m) { return !!m.info.mirror }).length === 0 }
                        Repeater {
                            model: root.machines.filter(function(m) { return !!m.info.mirror })
                            delegate: Panel {
                                id: mir
                                required property var modelData
                                readonly property var info: modelData.info
                                width: ov.availableWidth; height: mc.implicitHeight + 24
                                Column {
                                    id: mc
                                    x: 12; y: 12; width: parent.width - 24; spacing: 6
                                    Row {
                                        spacing: 8
                                        Text { text: mir.modelData.name; color: root.cBone; font.pixelSize: 15; font.bold: true }
                                        Dim { text: mir.info.always_on ? "always on" : "not always on"; anchors.baseline: parent.children[0].baseline }
                                    }
                                    Rectangle {
                                        width: parent.width; height: 8; radius: 4; color: root.cVoid; border.color: root.cLine
                                        Rectangle {
                                            height: parent.height; radius: 4
                                            width: mir.info.quota > 0 ? Math.min(1, mir.info.used / mir.info.quota) * parent.width : 0
                                            color: mir.info.quota > 0 && mir.info.used / mir.info.quota > 0.85 ? root.cAmber : root.cViolet
                                        }
                                    }
                                    Dim { text: root.size(mir.info.used) + " of " + root.size(mir.info.quota) + " used" + (mir.info.disk ? "; its disk: " + root.size(mir.info.disk.free) + " free of " + root.size(mir.info.disk.total) : "") }
                                    Repeater {
                                        model: mir.info.held || []
                                        delegate: Row {
                                            required property var modelData
                                            spacing: 8
                                            Dot { anchors.verticalCenter: parent.verticalCenter; color: root.copyColour({ status: modelData }) }
                                            Text { text: modelData.source; color: root.cBone; font.pixelSize: 13; width: 110 }
                                            Dim { text: root.copyText({ status: modelData }) }
                                        }
                                    }
                                    Dim { text: "Holds nobody's backup yet."; visible: (mir.info.held || []).length === 0 }
                                }
                            }
                        }
                    }
                }

                // --- Metrics ---------------------------------------------------------
                ScrollView {
                    id: mt
                    clip: true
                    contentWidth: availableWidth
                    Column {
                        width: mt.availableWidth
                        spacing: 14
                        readonly property var rs: root.runsOf === root.selected ? root.runs : []
                        readonly property var info: root.current ? root.current.info : ({})
                        Heading { text: root.selected + ": " + parent.rs.length + " runs" }
                        Dim { visible: !parent.info.source; text: root.selected + " backs nothing up, so it has no runs. Its storage:" }
                        Row {
                            spacing: 24
                            Repeater {
                                model: {
                                    var i = parent.parent.info, rs = parent.parent.rs, ok = rs.filter(function(r) { return r.ok }).length
                                    var out = [{ k: "storage used", v: root.size(i.used) + " / " + root.size(i.quota) }]
                                    if (i.source) {
                                        var last = i.last_run || {}
                                        out.push({ k: "repository", v: root.size(last.stored) })
                                        out.push({ k: "runs that worked", v: rs.length ? Math.round(100 * ok / rs.length) + "%" : "-" })
                                        out.push({ k: "files", v: last.files !== undefined ? String(last.files) : "-" })
                                        out.push({ k: "read last run", v: root.size(last.bytes) })
                                    }
                                    if (i.mirror) out.push({ k: "holds", v: (i.held || []).length + " sources" })
                                    return out
                                }
                                delegate: Column {
                                    required property var modelData
                                    Text { text: modelData.v; color: root.cBone; font.pixelSize: 18 }
                                    Dim { text: modelData.k }
                                }
                            }
                        }
                        Bars {
                            width: parent.width; visible: !!parent.info.source
                            title: "Added to the repository"; colour: root.cPhosphor
                            values: parent.rs.map(function(r) { return r.data_added || 0 })
                            failed: parent.rs.map(function(r) { return !r.ok })
                            labelOf: root.size
                        }
                        Bars {
                            readonly property var sized: parent.rs.filter(function(r) { return r.stored !== undefined })
                            width: parent.width; visible: !!parent.info.source
                            title: "Repository size"; colour: root.cViolet; fromZero: false
                            values: sized.map(function(r) { return r.stored })
                            failed: sized.map(function(r) { return !r.ok })
                            labelOf: root.size
                        }
                        Bars {
                            width: parent.width; visible: !!parent.info.source
                            title: "How long a run took"; colour: root.cSky
                            values: parent.rs.map(function(r) { return root.seconds(r) })
                            failed: parent.rs.map(function(r) { return !r.ok })
                            labelOf: function(s) { return s < 90 ? Math.round(s) + " s" : Math.round(s / 60) + " min" }
                        }
                        Heading { text: "Runs"; visible: parent.rs.length > 0 }
                        Repeater {
                            model: parent.rs.slice().reverse()
                            delegate: Row {
                                required property var modelData
                                spacing: 10
                                Dot { anchors.verticalCenter: parent.verticalCenter; color: modelData.ok ? root.cPhosphor : root.cRust }
                                Text { text: new Date(modelData.started).toLocaleString(Qt.locale(), "ddd d MMM HH:mm"); color: root.cBone; font.pixelSize: 12; width: 130 }
                                Dim { width: 60; text: root.took(modelData) }
                                Dim {
                                    width: mt.availableWidth - 230
                                    text: root.runText(modelData)
                                    color: modelData.ok ? root.cAsh : root.cRust
                                    elide: Text.ElideRight; wrapMode: Text.NoWrap
                                }
                            }
                        }
                    }
                }

                // --- Settings --------------------------------------------------------
                ScrollView {
                    id: st
                    clip: true
                    contentWidth: availableWidth
                    Settings_ { width: st.availableWidth; machine: root.current }
                }
            }
        }
    }

    // The settings of one machine, edited as a copy and sent whole.
    component Settings_: Column {
        id: s
        property var machine: null
        readonly property var info: machine ? machine.info : ({})
        readonly property bool editable: !!info.owner
        property bool isSource: false
        property var chosen: ({})
        property var mirrorsChosen: ({})
        property var ownersChosen: ({})
        // Its keys, for owners: which card or passphrase opens its backups.
        property var keyList: null
        function loadKeys() {
            keyList = null
            if (!machine || !machine.info.owner || !machine.info.source || machine.info.key === false) return
            var r = root.core("get", [machine.address, "/v1/keys"])
            if (r && !r.error) keyList = r
        }
        function keyLine(k) {
            var d = new Date(k.created)
            return k.label + (k.current ? " - in use" : "") + (isNaN(d.getTime()) ? "" : " - since " + d.toLocaleDateString(Qt.locale(), "d MMM yyyy"))
        }
        spacing: 10

        // From machine, not info: in machine's change handler, info's
        // binding has not caught up yet, and the form showed the machine
        // before.
        function reset() {
            var info = machine ? machine.info : {}
            var src = info.source || null
            isSource = !!src
            var c = {}
            ;((src && src.presets) || []).forEach(function(p) { c[p] = true })
            chosen = c
            var m = {}
            ;((src && src.mirrors) || []).forEach(function(x) { m[x] = true })
            mirrorsChosen = m
            var o = {}
            ;(info.owners || []).forEach(function(x) { o[x] = true })
            ownersChosen = o
            paths.text = ((src && src.paths) || []).join("\n")
            exclude.text = ((src && src.exclude) || []).join("\n")
            times.text = ((src && src.times) || ["03:00", "15:00"]).join(", ")
            daily.text = String((src && src.keep_daily) || 14)
            weekly.text = String((src && src.keep_weekly) || 12)
            keepLocal.checked = !!(src && src.keep_local)
            mirror.checked = !!info.mirror
            alwaysOn.checked = !!info.always_on
            quota.text = String(info.quota_gb || 10)
            Qt.callLater(loadKeys)
        }
        // Reset when another machine is picked, not on every poll: a poll
        // must not undo what is being typed.
        property string loadedFor: ""
        onMachineChanged: if (machine && machine.name !== loadedFor) { loadedFor = machine.name; reset() }
        Component.onCompleted: if (machine) { loadedFor = machine.name; reset() }

        function keys(o) { return Object.keys(o).filter(function(k) { return o[k] }).sort() }
        function lines(t) { return t.split(/\n/).map(function(x) { return x.trim() }).filter(function(x) { return x !== "" }) }
        function toggle(o, k, on) { var c = Object.assign({}, o); c[k] = on; return c }

        function settings() {
            var out = { owners: keys(ownersChosen), always_on: alwaysOn.checked, mirror: mirror.checked,
                        quota_gb: parseInt(quota.text) || 10, source: null }
            if (isSource)
                out.source = { presets: keys(chosen), paths: lines(paths.text), exclude: lines(exclude.text),
                               times: times.text.split(/[ ,]+/).filter(function(x) { return x !== "" }),
                               mirrors: keys(mirrorsChosen), keep_local: keepLocal.checked,
                               keep_daily: parseInt(daily.text) || 14, keep_weekly: parseInt(weekly.text) || 12 }
            return out
        }
        // The machines it may mirror to: those that say they mirror, and any
        // it already names.
        readonly property var mirrorChoices: {
            var out = root.machines.filter(function(m) { return m.info.mirror && m.name !== (s.machine ? s.machine.name : "") })
                                   .map(function(m) { return { name: m.name, about: m.info.always_on ? "always on" : "not always on" } })
            keys(mirrorsChosen).forEach(function(n) { if (!out.some(function(o) { return o.name === n })) out.push({ name: n, about: "not reachable from here" }) })
            return out
        }
        // Owners: any device on the meshes, not only those with a daemon: a
        // tablet that only looks runs none.
        readonly property var ownerChoices: {
            var names = {}
            root.machines.forEach(function(m) { names[m.name] = true })
            root.peerNames.forEach(function(n) { names[n] = true })
            if (root.self) names[root.self] = true
            keys(ownersChosen).forEach(function(n) { names[n] = true })
            if (s.machine) delete names[s.machine.name]
            return Object.keys(names).sort()
        }

        Heading { text: s.machine ? s.machine.name : "" }
        Rectangle {
            visible: !s.editable && !!s.machine
            width: parent.width; height: ro.implicitHeight + 16; radius: 4; color: root.cRaised; border.color: root.cAmber
            Label_ {
                id: ro
                x: 8; y: 8; width: parent.width - 16
                text: "Read only: " + (root.self || "this device") + " is not among " + (s.machine ? s.machine.name : "") + "'s owners. Add it there, in Shrooms Backup on that machine or in /etc/shrooms-backup/config.json."
            }
        }

        Panel {
            visible: !!s.machine && s.editable && !!s.info.source
            width: parent.width; height: keyCol.implicitHeight + 24
            Column {
                id: keyCol
                x: 12; y: 12; width: parent.width - 24; spacing: 6
                Text { text: "Keys"; color: root.cBone; font.pixelSize: 13 }
                Label_ {
                    visible: s.info.key === false; width: parent.width; color: root.cAmber
                    text: "No key yet, so it cannot back up. With the Keycard in a reader: shrooms-backup key --keycard --machine " + (s.machine ? s.machine.name : "")
                }
                Dim { visible: s.info.key !== false && s.keyList === null; text: "Reading its keys..." }
                Dim { visible: !!s.keyList && !!s.keyList.pending; text: "It has a key; its first backup will make the repository." }
                Repeater {
                    model: s.keyList && s.keyList.keys ? s.keyList.keys : []
                    delegate: Row {
                        required property var modelData
                        spacing: 8
                        Dot { anchors.verticalCenter: parent.verticalCenter; color: modelData.current ? root.cPhosphor : root.cAsh }
                        Text { text: s.keyLine(modelData); color: root.cBone; font.pixelSize: 12 }
                    }
                }
                Dim {
                    width: parent.width
                    visible: !!s.keyList && !!s.keyList.keys && s.keyList.keys.length > 0 && !s.keyList.keys.some(function(k) { return k.label.indexOf("Keycard") === 0 })
                    text: "Only a passphrase opens these backups. Add the card's key: shrooms-backup key --keycard --machine " + (s.machine ? s.machine.name : "")
                }
            }
        }

        Panel {
            width: parent.width; height: srcCol.implicitHeight + 24
            Column {
                id: srcCol
                x: 12; y: 12; width: parent.width - 24; spacing: 8
                Check {
                    label: "Back this machine up"; enabled: s.editable
                    about: "It needs its key: from the Keycard (shrooms-backup key --keycard --machine NAME, where a reader is), or `sudo shrooms-backup key` on the machine."
                    checked: s.isSource; onToggled: s.isSource = checked
                }
                Column {
                    visible: s.isSource; width: parent.width; spacing: 8
                    Dim { text: "What it backs up" }
                    Repeater {
                        model: Object.keys(root.presets).sort()
                        delegate: Check {
                            required property string modelData
                            width: srcCol.width
                            label: modelData; about: root.presets[modelData].about || ""
                            enabled: s.editable; checked: !!s.chosen[modelData]
                            onToggled: s.chosen = s.toggle(s.chosen, modelData, checked)
                        }
                    }
                    Dim { text: "More paths, one a line (absolute)" }
                    TextArea {
                        id: paths; width: parent.width; enabled: s.editable
                        color: root.cBone; font.pixelSize: 12; font.family: "monospace"; placeholderText: "/home/me/notes"
                        placeholderTextColor: root.cAsh
                        background: Rectangle { color: root.cVoid; border.color: root.cLine; radius: 4; implicitHeight: 48 }
                    }
                    Dim { text: "Leave out, one pattern a line (restic's: \"*.tmp\", \"cache\", \"/abs/path\")" }
                    TextArea {
                        id: exclude; width: parent.width; enabled: s.editable
                        color: root.cBone; font.pixelSize: 12; font.family: "monospace"
                        background: Rectangle { color: root.cVoid; border.color: root.cLine; radius: 4; implicitHeight: 48 }
                    }
                    Row {
                        spacing: 16
                        Column { spacing: 4; Dim { text: "When (HH:MM, local)" } Field { id: times; width: 160; enabled: s.editable } }
                        Column { spacing: 4; Dim { text: "Keep days" } Field { id: daily; width: 70; enabled: s.editable; validator: IntValidator { bottom: 1; top: 3650 } } }
                        Column { spacing: 4; Dim { text: "Keep weeks" } Field { id: weekly; width: 70; enabled: s.editable; validator: IntValidator { bottom: 1; top: 520 } } }
                    }
                    Dim { text: "Kept by" }
                    Dim { text: "No machine you can reach is a mirror yet."; visible: s.mirrorChoices.length === 0 }
                    Repeater {
                        model: s.mirrorChoices
                        delegate: Check {
                            required property var modelData
                            label: modelData.name; about: modelData.about
                            enabled: s.editable; checked: !!s.mirrorsChosen[modelData.name]
                            onToggled: s.mirrorsChosen = s.toggle(s.mirrorsChosen, modelData.name, checked)
                        }
                    }
                    Check {
                        id: keepLocal; enabled: s.editable
                        label: "Keep a copy here too"
                        about: "Otherwise this machine's storage is only a staging area, emptied once every mirror has the run."
                    }
                }
            }
        }

        Panel {
            width: parent.width; height: mirCol.implicitHeight + 24
            Column {
                id: mirCol
                x: 12; y: 12; width: parent.width - 24; spacing: 8
                Check {
                    id: mirror; enabled: s.editable
                    label: "Keep copies for other machines"
                    about: "Changing this restarts its daemon."
                }
                Row {
                    spacing: 8; visible: mirror.checked
                    Dim { text: "The most backups may take here, GB"; anchors.verticalCenter: parent.verticalCenter }
                    Field { id: quota; width: 80; enabled: s.editable; validator: IntValidator { bottom: 1; top: 100000 } }
                    Dim { text: (s.info.disk ? "its disk: " + root.size(s.info.disk.free) + " free of " + root.size(s.info.disk.total) + "; " : "") + "changing it restarts the daemon"; anchors.verticalCenter: parent.verticalCenter }
                }
                Check {
                    id: alwaysOn; enabled: s.editable
                    label: "Always on"
                    about: "Says so to the others: a laptop is a weak mirror."
                }
            }
        }

        Panel {
            width: parent.width; height: ownCol.implicitHeight + 24
            Column {
                id: ownCol
                x: 12; y: 12; width: parent.width - 24; spacing: 8
                Text { text: "Owners"; color: root.cBone; font.pixelSize: 13 }
                Dim { width: parent.width; text: "Devices that may change these settings, start runs and restore any backup kept here. The machine itself always may." }
                Repeater {
                    model: s.ownerChoices
                    delegate: Check {
                        required property string modelData
                        label: modelData + (modelData === root.self ? "  (this device)" : "")
                        enabled: s.editable; checked: !!s.ownersChosen[modelData]
                        onToggled: s.ownersChosen = s.toggle(s.ownersChosen, modelData, checked)
                    }
                }
            }
        }

        Row {
            spacing: 8
            Btn { text: "Save"; primary: true; enabled_: s.editable && !!s.machine; onClicked: root.save(s.machine, s.settings()) }
            Btn { text: "Undo changes"; enabled_: !!s.machine; onClicked: s.reset() }
        }
    }
}
