// What the app understands of the backup daemons' answers: pure functions, tested with node
// (test/model.test.ts), so the screens only draw.

export const PORT = 7394;

export interface Run {
  started: string; finished: string; ok: boolean; error?: string; added?: string;
  mirrors?: Record<string, string>;
  files?: number; files_new?: number; files_changed?: number; bytes?: number; data_added?: number; stored?: number;
}
export interface Source {
  presets?: string[]; paths?: string[]; exclude?: string[]; times?: string[]; mirrors?: string[];
  keep_local?: boolean; keep_daily?: number; keep_weekly?: number;
}
export interface Held { source: string; adopted: number; pending: number; missing: number; error?: string; bytes?: number; receiving?: boolean }
export interface Info {
  name: string; mirror: boolean; always_on: boolean; quota: number; used: number; quota_gb?: number;
  owners?: string[]; source?: Source | null; last_run?: Run | null; running?: boolean; next?: string;
  holds?: string[]; held?: Held[]; owner?: boolean; disk?: { free: number; total: number }; key?: boolean;
  meshes?: string[]; progress?: Progress | null;
}
/** A run under way: restic reading ("reading"), then each mirror fetching ("mirrors"). */
export interface Progress {
  phase: string; started?: string; files?: number; files_done?: number; bytes?: number; bytes_done?: number;
  seconds_remaining?: number;
  mirrors?: Record<string, { bytes: number; done: number; ok?: boolean; error?: string; rate?: number; seconds_left?: number }>;
}
export interface Machine { name: string; base: string; info: Info }
/** One of a source's keys, as its owners see it: never anything that opens it. */
export interface KeyInfo { id: string; label: string; created: string; current: boolean }
export interface Peer { name: string; mesh: string; overlay: string; overlay_v4?: string; online: boolean }
export interface Settings {
  owners: string[]; always_on: boolean; mirror: boolean; quota_gb: number; source: Source | null;
}

/** The daemon's API base for an address or name a person typed, or an overlay address. */
export function baseUrl(address: string, port = PORT): string {
  let a = address.trim().replace(/^https?:\/\//, "").replace(/\/+$/, "");
  const bracket = /^\[(.+)\](?::(\d+))?$/.exec(a);
  if (bracket) return `http://[${bracket[1]}]:${bracket[2] || port}`;
  if ((a.match(/:/g) || []).length >= 2) return `http://[${a}]:${port}`;   // bare IPv6
  const hp = /^(.+):(\d+)$/.exec(a);
  return hp ? `http://${hp[1]}:${hp[2]}` : `http://${a}:${port}`;
}

/** shroomsbackup://add?address=proteus.office.mesh: adding a machine from a link or a QR. */
export function parseLink(url: string | null): string | null {
  if (!url) return null;
  const m = /^shroomsbackup:\/\/add\?(.*)$/.exec(url.trim());
  if (!m) return null;
  for (const kv of m[1].split("&")) {
    const [k, v] = kv.split("=");
    if (k === "address" && v) {
      const a = decodeURIComponent(v);
      return /^[A-Za-z0-9.:\[\]-]{1,128}$/.test(a) ? a : null;
    }
  }
  return null;
}

/**
 * One entry a machine: a machine on two meshes, or added by hand and found as a peer, answers twice.
 * The answer that says we are an owner wins: a machine on several meshes (ADR-011) knows a bare-named
 * owner on its primary mesh only, so the same device asking over another mesh is read-only there, and
 * keeping whichever answered first made the settings flip between editable and not.
 */
export function dedupe(ms: Machine[]): Machine[] {
  const by = new Map<string, Machine>();
  for (const m of ms) {
    if (!m.info || !m.info.name) continue;
    const had = by.get(m.info.name);
    if (!had || (m.info.owner && !had.info.owner)) by.set(m.info.name, m);
  }
  return [...by.values()].sort((a, b) => a.info.name.localeCompare(b.info.name));
}

/** Keep machines that did not answer this time, marked, rather than letting them vanish. */
export function mergeKnown(fresh: Machine[], old: Machine[]): { machines: Machine[]; stale: string[] } {
  const names = new Set(fresh.map((m) => m.info.name));
  const kept = old.filter((m) => !names.has(m.info.name));
  return { machines: dedupe([...fresh, ...kept]), stale: kept.map((m) => m.info.name) };
}

export function size(n: number | undefined | null): string {
  if (n === undefined || n === null) return "-";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0, f = n;
  while (f >= 1000 && i < u.length - 1) { f /= 1000; i++; }
  return (i === 0 ? String(f) : f.toFixed(f < 10 ? 1 : 0)) + " " + u[i];
}

const zero = (iso?: string) => !iso || iso.startsWith("0001-");

export function when(iso: string | undefined, now = Date.now()): string {
  if (zero(iso)) return "never";
  let d = (now - new Date(iso!).getTime()) / 1000;
  const future = d < 0;
  d = Math.abs(d);
  const s = d < 90 ? Math.round(d) + " s" : d < 5400 ? Math.round(d / 60) + " min"
    : d < 129600 ? Math.round(d / 3600) + " h" : Math.round(d / 86400) + " days";
  return future ? "in " + s : s + " ago";
}

export function seconds(r: Run): number {
  if (zero(r.finished)) return 0;
  return Math.max(0, (new Date(r.finished).getTime() - new Date(r.started).getTime()) / 1000);
}
export const duration = (s: number) => s < 90 ? Math.round(s) + " s" : Math.round(s / 60) + " min";

export function whatOf(s: Source): string {
  const w = [...(s.presets || [])], n = (s.paths || []).length;
  if (n) w.push(n === 1 ? s.paths![0] : n + " folders");
  return w.join(", ");
}

/** A run from before the daemon kept restic's summary has its words only. */
export function runText(r: Run): string {
  if (!r.ok) return r.error || "failed";
  let t = (r.added || "nothing") + " new";
  if (r.files !== undefined) t += `; ${r.files_new || 0} new and ${r.files_changed || 0} changed of ${r.files} files`;
  return t;
}

export function roleOf(i: Info): string {
  const r = [];
  if (i.source) r.push("backs up");
  if (i.mirror) r.push("mirror");
  return r.length ? r.join(" + ") : "idle";
}

export type Tone = "ok" | "busy" | "warn" | "bad" | "none";
export function stateOf(i: Info): Tone {
  if (i.running) return "busy";
  if (i.source && i.last_run && !i.last_run.ok) return "bad";
  if (i.source && !i.last_run) return "warn";
  return "ok";
}

/** Where a source's copies are, as its mirrors report them. */
export function copiesOf(name: string, ms: Machine[]): { mirror: string; status: Held; alwaysOn: boolean }[] {
  const out = [];
  for (const m of ms) for (const h of m.info.held || []) if (h.source === name) out.push({ mirror: m.info.name, status: h, alwaysOn: m.info.always_on });
  return out;
}
export function copyText(h: Held): string {
  if (h.error) return h.error;
  if (h.pending > h.adopted) return `fetching, ${h.missing} objects to go`;
  if (h.receiving) return `receiving, ${size(h.bytes)} so far`;
  return `in step (${size(h.bytes)})`;
}
export function copyTone(h: Held): Tone {
  if (h.error) return "bad";
  if (h.pending > h.adopted || h.receiving) return "warn";
  return "ok";
}

/** A mirror line for a source: from the mirror's own report if it answered, else from the last run. */
export function mirrorLine(source: Machine, mirror: string, ms: Machine[]): { text: string; tone: Tone } {
  const c = copiesOf(source.info.name, ms).find((x) => x.mirror === mirror);
  if (c) return { text: copyText(c.status) + (c.alwaysOn ? "" : ", not always on"), tone: copyTone(c.status) };
  const said = source.info.last_run?.mirrors?.[mirror] || "";
  const answered = ms.some((m) => m.info.name === mirror);
  if (said === "ok") return { text: answered ? "had the last run" : "had the last run (not reachable from here now)", tone: "ok" };
  if (said) return { text: said, tone: "bad" };
  // A mirror that answered but holds nothing of this source has simply not had a run yet.
  if (answered) return { text: "no copy yet", tone: "warn" };
  return { text: "not reachable from here", tone: "none" };
}

/** A mirror's storage: its limit's use, and the disk it is on. */
export function storageText(i: Info): string {
  let t = `${size(i.used)} of ${size(i.quota)} used`;
  if (i.disk) t += ` · its disk: ${size(i.disk.free)} free of ${size(i.disk.total)}`;
  return t;
}

export function sourceLine(i: Info, now = Date.now()): string {
  const l = i.last_run;
  if (i.running) return "Backing up now.";
  if (!l) return "Not backed up yet.";
  if (l.ok) return `Last backed up ${when(l.finished, now)}: ${l.added || "nothing"} new, ${size(l.stored)} in all, took ${duration(seconds(l))}.`;
  return `The last run, ${when(l.started, now)}, failed: ${l.error}`;
}

// --- settings -------------------------------------------------------------------------------------

/** The form's state: text where a person types, sets where they tick. */
export interface Form {
  isSource: boolean; presets: string[]; paths: string; exclude: string; times: string;
  keepDaily: string; keepWeekly: string; mirrors: string[]; keepLocal: boolean;
  mirror: boolean; quotaGb: string; alwaysOn: boolean; owners: string[];
}

export function formOf(i: Info): Form {
  const s = i.source || null;
  return {
    isSource: !!s, presets: [...(s?.presets || [])], paths: (s?.paths || []).join("\n"), exclude: (s?.exclude || []).join("\n"),
    times: (s?.times || ["03:00", "15:00"]).join(", "), keepDaily: String(s?.keep_daily || 14), keepWeekly: String(s?.keep_weekly || 12),
    mirrors: [...(s?.mirrors || [])], keepLocal: !!s?.keep_local,
    mirror: !!i.mirror, quotaGb: String(i.quota_gb || 10), alwaysOn: !!i.always_on, owners: [...(i.owners || [])],
  };
}

const lines = (t: string) => t.split("\n").map((x) => x.trim()).filter((x) => x !== "");

/** What PUT /v1/config takes; the daemon checks it again and says what is wrong. */
export function settingsOf(f: Form): Settings {
  return {
    owners: [...f.owners].sort(), always_on: f.alwaysOn, mirror: f.mirror, quota_gb: parseInt(f.quotaGb) || 10,
    source: !f.isSource ? null : {
      presets: [...f.presets].sort(), paths: lines(f.paths), exclude: lines(f.exclude),
      times: f.times.split(/[ ,]+/).filter((x) => x !== ""), mirrors: [...f.mirrors].sort(), keep_local: f.keepLocal,
      keep_daily: parseInt(f.keepDaily) || 14, keep_weekly: parseInt(f.keepWeekly) || 12,
    },
  };
}

/** The form's own mistakes, before anything is sent. */
export function formProblem(f: Form): string {
  if (!f.isSource) return "";
  if (f.presets.length === 0 && lines(f.paths).length === 0) return "Pick something to back up.";
  if (f.mirrors.length === 0) return "Pick at least one machine to keep it.";
  for (const t of f.times.split(/[ ,]+/).filter((x) => x !== ""))
    if (!/^([01]\d|2[0-3]):[0-5]\d$/.test(t)) return `"${t}" is not a time (HH:MM).`;
  for (const p of lines(f.paths)) if (!p.startsWith("/")) return `"${p}" is not an absolute path.`;
  return "";
}

export function toggled(xs: string[], x: string, on: boolean): string[] {
  const s = new Set(xs);
  if (on) s.add(x); else s.delete(x);
  return [...s].sort();
}

/** Who may be an owner: every device on the meshes (a phone or tablet runs no daemon), and any named already. */
export function ownerChoices(machine: string, peers: Peer[], ms: Machine[], current: string[]): string[] {
  const s = new Set<string>([...peers.map((p) => p.name), ...ms.map((m) => m.info.name), ...current]);
  s.delete(machine);
  return [...s].filter((x) => x).sort();
}

/** The machines a source may send its copies to: those that mirror, and any it names already. */
export function mirrorChoices(machine: string, ms: Machine[], current: string[]): { name: string; about: string }[] {
  const out = ms.filter((m) => m.info.mirror && m.info.name !== machine).map((m) => ({ name: m.info.name, about: m.info.always_on ? "always on" : "not always on" }));
  for (const n of current) if (!out.some((o) => o.name === n)) out.push({ name: n, about: "not reachable from here" });
  return out;
}

/** Bars for a chart: heights 0..1, scaled from zero or, for slow-growing sizes, from just under the least. */
export function bars(values: number[], fromZero = true): number[] {
  if (values.length === 0) return [];
  const max = Math.max(...values) || 1;
  // From just under the least, but never closer than 5% of the most: a repository that grew by
  // 2 KB of 40 MB must not draw as one bar at zero and one at the top.
  const min = Math.min(...values);
  let base = fromZero ? 0 : min - Math.max((max - min) * 0.1, max * 0.05);
  if (max - base <= 0) base = max - 1;
  return values.map((v) => Math.max(0.02, (v - base) / (max - base)));
}

/** A key's line: "Keycard 6e021332 · in use · since 2 Nov", or a passphrase's. */
export function keyLine(k: KeyInfo): string {
  const d = new Date(k.created);
  const since = isNaN(d.getTime()) ? "" : ` · since ${d.getDate()} ${d.toLocaleString("en", { month: "short" })} ${d.getFullYear()}`;
  return `${k.label}${k.current ? " · in use" : ""}${since}`;
}

/** How long, roughly: "about 2 h", "about 25 min", "under a minute". */
export function left(seconds: number): string {
  if (seconds < 60) return "under a minute";
  if (seconds < 90 * 60) return `about ${Math.round(seconds / 60)} min`;
  return `about ${(seconds / 3600).toFixed(seconds < 10 * 3600 ? 1 : 0)} h`;
}

export interface Bar { label: string; fraction: number; tone: Tone }

/**
 * The bars for a run under way (one while restic reads, then one a mirror), or for mirrors still being
 * caught up between runs ("catching up": a source streams to its mirrors all the time, ADR-014). None when
 * every mirror holds everything.
 */
export function progressBars(i: Info): Bar[] {
  const p = i.progress;
  if (!p) return [];
  if (p.phase === "reading") {
    if (!p.bytes) return [{ label: "Reading files…", fraction: 0, tone: "busy" }];
    const rest = p.seconds_remaining ? `, ${left(p.seconds_remaining)} left` : "";
    return [{ label: `Reading files: ${size(p.bytes_done || 0)} of ${size(p.bytes)}${rest}`, fraction: Math.min((p.bytes_done || 0) / p.bytes, 1), tone: "busy" }];
  }
  return Object.entries(p.mirrors || {}).sort(([a], [b]) => a.localeCompare(b)).map(([name, mp]) => {
    if (mp.error) return { label: `${name}: ${mp.error}`, fraction: mp.bytes ? mp.done / mp.bytes : 0, tone: "bad" as Tone };
    if (mp.ok) return { label: `${name}: holds this run`, fraction: 1, tone: "ok" as Tone };
    const lead = p.phase === "catching up" ? "catching up, " : "";
    // The rate over the last minutes, and the time left at it (ADR-014).
    const pace = mp.rate ? ` · ${size(mp.rate)}/s${mp.seconds_left ? `, ${left(mp.seconds_left)} left` : ""}` : "";
    return { label: `${name}: ${lead}${size(mp.done)} of ${size(mp.bytes)}${pace}`, fraction: mp.bytes ? Math.min(mp.done / mp.bytes, 1) : 0, tone: "busy" as Tone };
  });
}
