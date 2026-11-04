// node --experimental-strip-types test/model.test.ts
import assert from "node:assert/strict";
import * as m from "../src/model.ts";

const now = Date.parse("2026-11-01T18:00:00Z");
const iso = (minAgo: number) => new Date(now - minAgo * 60000).toISOString();
let n = 0;
const t = (name: string, f: () => void) => { f(); n++; console.log("ok  ", name); };

t("addresses become the daemon's base", () => {
  assert.equal(m.baseUrl("proteus.office.mesh"), "http://proteus.office.mesh:7394");
  assert.equal(m.baseUrl("fdb0:9afc::1"), "http://[fdb0:9afc::1]:7394");
  assert.equal(m.baseUrl("[fdb0::1]:8000"), "http://[fdb0::1]:8000");
  assert.equal(m.baseUrl(" http://198.19.1.2/ "), "http://198.19.1.2:7394");
});

t("links add a machine, and nothing else", () => {
  assert.equal(m.parseLink("shroomsbackup://add?address=proteus.office.mesh"), "proteus.office.mesh");
  assert.equal(m.parseLink("shroomsbackup://add?address=fdb0%3A%3A1"), "fdb0::1");
  assert.equal(m.parseLink("shroomsbackup://add?address=a%20b"), null);
  assert.equal(m.parseLink("https://example.com"), null);
  assert.equal(m.parseLink(null), null);
});

const info = (name: string, x: Partial<m.Info> = {}): m.Info => ({ name, mirror: false, always_on: true, quota: 0, used: 0, ...x });
const mach = (name: string, x: Partial<m.Info> = {}, base = "b-" + name): m.Machine => ({ name, base, info: info(name, x) });

t("one entry a machine, sorted", () => {
  const d = m.dedupe([mach("vps"), mach("proteus", {}, "first"), mach("proteus", {}, "second")]);
  assert.deepEqual(d.map((x) => x.base), ["first", "b-vps"]);
});

t("a machine on two meshes: the answer that says we own it wins, whichever came first", () => {
  const d = m.dedupe([mach("scribe", { owner: false }, "home"), mach("scribe", { owner: true }, "office"), mach("scribe", { owner: false }, "late")]);
  assert.deepEqual(d.map((x) => x.base), ["office"]);
  assert.deepEqual(m.dedupe([mach("scribe", { owner: true }, "office"), mach("scribe", { owner: false }, "home")]).map((x) => x.base), ["office"]);
});

t("a machine that did not answer stays, marked stale", () => {
  const r = m.mergeKnown([mach("proteus")], [mach("proteus"), mach("scribe")]);
  assert.deepEqual(r.machines.map((x) => x.info.name), ["proteus", "scribe"]);
  assert.deepEqual(r.stale, ["scribe"]);
});

t("sizes and times read like the Basecamp view", () => {
  assert.equal(m.size(40569511), "41 MB");
  assert.equal(m.size(696), "696 B");
  assert.equal(m.size(undefined), "-");
  assert.equal(m.when(iso(60), now), "60 min ago");
  assert.equal(m.when(new Date(now + 3 * 3600000).toISOString(), now), "in 3 h");
  assert.equal(m.when("0001-01-01T00:00:00Z", now), "never");
});

t("a run before the summary has its words only", () => {
  assert.equal(m.runText({ started: iso(2), finished: iso(1), ok: true, added: "0 B   (0 B   stored)" }), "0 B   (0 B   stored) new");
  assert.equal(m.runText({ started: iso(2), finished: iso(1), ok: true, added: "1 KiB", files: 10, files_new: 2, files_changed: 1 }), "1 KiB new; 2 new and 1 changed of 10 files");
  assert.equal(m.runText({ started: iso(2), finished: iso(1), ok: false, error: "no key" }), "no key");
});

t("what a source keeps", () => {
  assert.equal(m.whatOf({ paths: ["/var/tmp/x"] }), "/var/tmp/x");
  assert.equal(m.whatOf({ presets: ["shrooms", "keys"], paths: ["/a", "/b"] }), "shrooms, keys, 2 folders");
});

t("where a source's copies are", () => {
  const src = mach("jimmy-crib", { source: { mirrors: ["proteus", "scribe", "vps"] }, last_run: { started: iso(5), finished: iso(4), ok: true, mirrors: { proteus: "ok", scribe: "ok", vps: "timeout" } } });
  const ms = [src,
    mach("proteus", { mirror: true, held: [{ source: "jimmy-crib", adopted: 9, pending: 9, missing: 0, bytes: 40e6 }] }),
    mach("vps", { mirror: true, always_on: false, held: [{ source: "jimmy-crib", adopted: 8, pending: 9, missing: 3 }] })];
  assert.deepEqual(m.mirrorLine(src, "proteus", ms), { text: "in step (40 MB)", tone: "ok" });
  assert.deepEqual(m.mirrorLine(src, "vps", ms), { text: "fetching, 3 objects to go, not always on", tone: "warn" });
  assert.deepEqual(m.mirrorLine(src, "scribe", ms), { text: "had the last run (not reachable from here now)", tone: "ok" });
  assert.equal(m.mirrorLine(mach("x", { last_run: null }), "y", ms).tone, "none");
  // Found, but holding nothing of this source: not "unreachable".
  assert.deepEqual(m.mirrorLine(mach("proteus", { source: { mirrors: ["vps"] }, last_run: { started: iso(1), finished: iso(1), ok: false, error: "no key" } }), "vps", ms),
    { text: "no copy yet", tone: "warn" });
  assert.equal(m.storageText(info("p", { used: 41e6, quota: 10e9, disk: { free: 80e9, total: 500e9 } })), "41 MB of 10 GB used · its disk: 80 GB free of 500 GB");
});

t("a source's state", () => {
  assert.equal(m.stateOf(info("a", { running: true })), "busy");
  assert.equal(m.stateOf(info("a", { source: {}, last_run: { started: "", finished: "", ok: false } })), "bad");
  assert.equal(m.stateOf(info("a", { source: {} })), "warn");
  assert.equal(m.stateOf(info("a", { mirror: true })), "ok");
  assert.match(m.sourceLine(info("a", { source: {}, last_run: { started: iso(3), finished: iso(1), ok: true, added: "1 KiB", stored: 2e6 } }), now), /^Last backed up 60 s ago: 1 KiB new, 2.0 MB in all, took 2 min\.$/);
});

t("settings go out as the daemon takes them, and come back the same", () => {
  const i = info("jimmy-crib", { owners: ["nothing", "laptop"], quota_gb: 10, source: { paths: ["/var/tmp/t"], times: ["03:30", "15:30"], mirrors: ["vps", "proteus"], keep_daily: 14, keep_weekly: 12 } });
  const f = m.formOf(i);
  const s = m.settingsOf(f);
  assert.deepEqual(s, { owners: ["laptop", "nothing"], always_on: true, mirror: false, quota_gb: 10,
    source: { presets: [], paths: ["/var/tmp/t"], exclude: [], times: ["03:30", "15:30"], mirrors: ["proteus", "vps"], keep_local: false, keep_daily: 14, keep_weekly: 12 } });
  assert.equal(m.settingsOf({ ...f, isSource: false }).source, null);
  assert.equal(m.formProblem(f), "");
  assert.equal(m.formProblem({ ...f, times: "3:30" }), "\"3:30\" is not a time (HH:MM).");
  assert.equal(m.formProblem({ ...f, mirrors: [] }), "Pick at least one machine to keep it.");
  assert.equal(m.formProblem({ ...f, paths: "relative/x" }), "\"relative/x\" is not an absolute path.");
  assert.equal(m.formProblem({ ...f, paths: "", presets: [] }), "Pick something to back up.");
});

t("any mesh device may be an owner, the machine itself not", () => {
  const peers: m.Peer[] = [{ name: "duet", mesh: "office", overlay: "fd::1", online: false }, { name: "proteus", mesh: "office", overlay: "fd::2", online: true }];
  assert.deepEqual(m.ownerChoices("proteus", peers, [mach("vps")], ["nothing"]), ["duet", "nothing", "vps"]);
});

t("mirrors to choose from", () => {
  const ms = [mach("proteus", { mirror: true }), mach("jimmy-crib", { source: {} }), mach("vps", { mirror: true, always_on: false })];
  assert.deepEqual(m.mirrorChoices("jimmy-crib", ms, ["scribe"]), [
    { name: "proteus", about: "always on" }, { name: "vps", about: "not always on" }, { name: "scribe", about: "not reachable from here" }]);
});

t("bars", () => {
  assert.deepEqual(m.bars([]), []);
  assert.deepEqual(m.bars([0, 5, 10]), [0.02, 0.5, 1]);
  const s = m.bars([100, 100, 110], false);
  assert.ok(s[0] < s[2] && s[2] === 1);
  const flat = m.bars([39669000, 39667000], false);
  assert.ok(flat[1] > 0.9, "a 2 KB change drew as " + flat);
  assert.deepEqual(m.bars([7, 7], false), [1, 1]);
});

t("a key's line says which and whether in use, never more", () => {
  assert.equal(m.keyLine({ id: "a1", label: "Keycard 6e021332", created: "2026-11-02T08:38:17+01:00", current: true }), "Keycard 6e021332 · in use · since 2 Nov 2026");
  assert.equal(m.keyLine({ id: "b2", label: "passphrase", created: "", current: false }), "passphrase");
});

console.log(n + " passed");
