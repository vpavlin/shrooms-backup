// Shrooms Backup on the phone: every backup daemon the phone can reach over the mesh, what each
// backs up and where its copies are (Overview), how its runs went (Metrics), and an owner's
// settings (Settings). The same three screens as the Basecamp view; the daemons decide who may
// change what, by the name the mesh gives this phone.
import { useCallback, useEffect, useRef, useState } from "react";
import {
  Alert, AppState, Linking, Modal, Pressable, RefreshControl, ScrollView, StyleSheet, Switch, Text, TextInput, View,
} from "react-native";
import { SafeAreaProvider, SafeAreaView } from "react-native-safe-area-context";
import { StatusBar } from "expo-status-bar";
import { api } from "./src/api";
import { findMachines } from "./src/find";
import { store } from "./src/store";
import {
  bars, duration, formOf, formProblem, mergeKnown, mirrorChoices, mirrorLine, ownerChoices, parseLink, roleOf,
  runText, seconds, settingsOf, size, sourceLine, storageText, stateOf, toggled, when, whatOf,
  type Form, type Machine, type Peer, type Run, type Tone,
} from "./src/model";

const C = {
  void: "#07090B", panel: "#0E1216", raised: "#151a20", line: "#1C2229", ash: "#6B7680", bone: "#D6DDE3",
  phosphor: "#35F0A0", amber: "#F0B429", rust: "#E05252", violet: "#9A7BFF", sky: "#5AA9FF",
};
const toneColour = (t: Tone) => ({ ok: C.phosphor, busy: C.sky, warn: C.amber, bad: C.rust, none: C.ash }[t]);
const TABS = ["Overview", "Metrics", "Settings"] as const;

export default function App() {
  return (
    <SafeAreaProvider>
      <SafeAreaView style={{ flex: 1, backgroundColor: C.void }} edges={["top", "bottom"]}>
        <StatusBar style="light" />
        <Main />
      </SafeAreaView>
    </SafeAreaProvider>
  );
}

function Main() {
  const [seeds, setSeeds] = useState<string[] | null>(null);
  const [machines, setMachines] = useState<Machine[]>([]);
  const [stale, setStale] = useState<string[]>([]);
  const [peers, setPeers] = useState<Peer[]>([]);
  const [tab, setTab] = useState(0);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(false);
  const [adding, setAdding] = useState(false);
  const [notice, setNotice] = useState<{ text: string; tone: Tone } | null>(null);
  const shown = useRef<Machine[]>([]);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const say = useCallback((text: string, tone: Tone = "none") => {
    setNotice({ text, tone });
    if (timer.current) clearTimeout(timer.current);
    timer.current = setTimeout(() => setNotice(null), 6000);
  }, []);

  const refresh = useCallback(async (ss?: string[]) => {
    const list = ss || (await store.seeds());
    if (list.length === 0) return;
    setLoading(true);
    try {
      const r = await findMachines(list);
      const merged = mergeKnown(r.machines, shown.current);
      shown.current = merged.machines;
      setMachines(merged.machines); setStale(merged.stale);
      if (r.peers.length) { setPeers(r.peers); store.setPeers(r.peers); }
      store.setSeen(merged.machines);
      if (r.answered === 0 && r.machines.length === 0) say("No machine answered. Is Shrooms connected on this phone?", "warn");
    } finally { setLoading(false); }
  }, [say]);

  const addSeed = useCallback(async (address: string) => {
    const cur = await store.seeds();
    const next = cur.includes(address) ? cur : [...cur, address];
    // Closed here too: a link that adds the first machine arrives while the dialog that asks for
    // one is open.
    await store.setSeeds(next); setSeeds(next); setAdding(false);
    say("Added. Looking for backup machines…");
    refresh(next);
  }, [refresh, say]);

  useEffect(() => {
    (async () => {
      const [ss, seen, ps] = await Promise.all([store.seeds(), store.seen(), store.peers()]);
      shown.current = seen; setMachines(seen); setPeers(ps); setSeeds(ss);
      if (ss.length === 0) setAdding(true); else refresh(ss);
    })();
    const onLink = (url: string | null) => { const a = parseLink(url); if (a) addSeed(a); };
    Linking.getInitialURL().then(onLink).catch(() => {});
    const sub = Linking.addEventListener("url", (e) => onLink(e.url));
    return () => sub.remove();
  }, [refresh, addSeed]);

  // While the app is open, ask again every 20 s: a run's progress shows without pulling.
  useEffect(() => {
    const t = setInterval(() => { if (AppState.currentState === "active") refresh(); }, 20000);
    return () => clearInterval(t);
  }, [refresh]);

  useEffect(() => {
    if (machines.length && !machines.some((m) => m.info.name === selected)) setSelected(machines[0].info.name);
  }, [machines, selected]);

  const current = machines.find((m) => m.info.name === selected) || null;

  if (seeds === null) return <View style={{ flex: 1 }} />;

  return (
    <View style={{ flex: 1 }}>
      <View style={s.header}>
        <Text style={s.title}>Shrooms Backup</Text>
        <Pressable onPress={() => setAdding(true)} hitSlop={10}><Text style={s.headerLink}>Machines</Text></Pressable>
      </View>
      <View style={s.tabs}>
        {TABS.map((t, i) => (
          <Pressable key={t} onPress={() => setTab(i)} style={[s.tab, tab === i && s.tabOn]}>
            <Text style={[s.tabText, tab === i && { color: C.phosphor }]}>{t}</Text>
          </Pressable>
        ))}
      </View>
      {notice && <Text style={[s.notice, { color: toneColour(notice.tone) === C.ash ? C.bone : toneColour(notice.tone) }]}>{notice.text}</Text>}
      {tab !== 0 && machines.length > 0 && (
        <ScrollView horizontal style={s.chips} contentContainerStyle={{ gap: 8, paddingHorizontal: 12, paddingVertical: 10, alignItems: "center" }} showsHorizontalScrollIndicator={false}>
          {machines.map((m) => (
            <Pressable key={m.info.name} onPress={() => setSelected(m.info.name)} style={[s.chip, selected === m.info.name && s.chipOn]}>
              <Dot tone={stateOf(m.info)} />
              <Text style={s.chipText}>{m.info.name}</Text>
            </Pressable>
          ))}
        </ScrollView>
      )}
      {machines.length === 0 ? (
        <ScrollView contentContainerStyle={s.pad} refreshControl={<RefreshControl refreshing={loading} onRefresh={() => refresh()} tintColor={C.phosphor} />}>
          <Text style={s.dim}>{seeds.length === 0
            ? "Add one machine that runs Shrooms Backup — proteus.office.mesh, say. The phone finds the others through it."
            : loading ? "Looking for backup machines…" : "No backup machine answered. Pull to try again, or check Shrooms is connected."}</Text>
        </ScrollView>
      ) : tab === 0 ? (
        <Overview machines={machines} stale={stale} loading={loading} onRefresh={() => refresh()}
          onOpen={(n) => { setSelected(n); setTab(1); }}
          onRun={async (m) => {
            try { await api.run(m.base); say(`${m.info.name} is backing up now`, "ok"); setTimeout(() => refresh(), 1500); }
            catch (e: any) { say(`${m.info.name}: ${e.message}`, "bad"); }
          }} />
      ) : tab === 1 ? (
        current && <Metrics key={current.info.name} machine={current} />
      ) : (
        current && <SettingsScreen key={current.info.name + "|" + JSON.stringify(current.info.source) + current.info.mirror}
          machine={current} machines={machines} peers={peers}
          onSaved={(restart) => { say(restart ? `${current.info.name} saved; its daemon restarts, back in about 20 s` : `${current.info.name} saved`, "ok"); setTimeout(() => refresh(), restart ? 20000 : 800); }}
          onError={(msg) => say(`${current.info.name}: ${msg}`, "bad")} />
      )}
      <AddMachines visible={adding} seeds={seeds} onClose={() => setAdding(false)}
        onAdd={(a) => { setAdding(false); addSeed(a); }}
        onForget={async (a) => { const next = seeds.filter((x) => x !== a); await store.setSeeds(next); setSeeds(next); }} />
    </View>
  );
}

function Dot({ tone }: { tone: Tone }) {
  return <View style={[s.dot, { backgroundColor: toneColour(tone) }]} />;
}

function Button({ text, onPress, primary, disabled }: { text: string; onPress: () => void; primary?: boolean; disabled?: boolean }) {
  return (
    <Pressable onPress={onPress} disabled={disabled}
      style={({ pressed }) => [s.btn, primary && s.btnPrimary, disabled && s.btnOff, pressed && !disabled && { opacity: 0.7 }]}>
      <Text style={[s.btnText, primary && !disabled && { color: C.void }, disabled && { color: C.ash }]}>{text}</Text>
    </Pressable>
  );
}

// --- Overview -------------------------------------------------------------------------------------

function Overview({ machines, stale, loading, onRefresh, onOpen, onRun }: {
  machines: Machine[]; stale: string[]; loading: boolean; onRefresh: () => void; onOpen: (name: string) => void; onRun: (m: Machine) => void;
}) {
  const sources = machines.filter((m) => m.info.source);
  const mirrors = machines.filter((m) => m.info.mirror);
  return (
    <ScrollView contentContainerStyle={s.pad} refreshControl={<RefreshControl refreshing={loading} onRefresh={onRefresh} tintColor={C.phosphor} />}>
      <Text style={s.h}>Sources</Text>
      {sources.length === 0 && <Text style={s.dim}>No machine backs anything up yet.</Text>}
      {sources.map((m) => {
        const i = m.info, l = i.last_run;
        return (
          <Pressable key={i.name} style={s.card} onPress={() => onOpen(i.name)}>
            <View style={s.row}>
              <Dot tone={stateOf(i)} />
              <Text style={s.name}>{i.name}</Text>
              {stale.includes(i.name) && <Text style={s.small}> (as last seen)</Text>}
            </View>
            <Text style={s.small}>{whatOf(i.source!)}</Text>
            <Text style={[s.body, l && !l.ok && !i.running && { color: C.rust }]}>{sourceLine(i)}</Text>
            <Text style={s.small}>Next {when(i.next)} (at {(i.source!.times || []).join(", ")}); keeps {i.source!.keep_daily} days and {i.source!.keep_weekly} weeks</Text>
            {(i.source!.mirrors || []).map((mi) => {
              const ln = mirrorLine(m, mi, machines);
              return (
                <View key={mi} style={[s.row, { marginTop: 4 }]}>
                  <Dot tone={ln.tone} />
                  <Text style={[s.body, { width: 92 }]}>{mi}</Text>
                  <Text style={[s.small, { flex: 1 }]}>{ln.text}</Text>
                </View>
              );
            })}
            {i.owner && (
              <View style={{ marginTop: 10, alignItems: "flex-start" }}>
                <Button text={i.running ? "Backing up…" : "Back up now"} disabled={!!i.running} onPress={() => onRun(m)} />
              </View>
            )}
          </Pressable>
        );
      })}
      <Text style={s.h}>Mirrors</Text>
      {mirrors.length === 0 && <Text style={s.dim}>No machine keeps copies yet.</Text>}
      {mirrors.map((m) => {
        const i = m.info, frac = i.quota > 0 ? Math.min(1, i.used / i.quota) : 0;
        return (
          <Pressable key={i.name} style={s.card} onPress={() => onOpen(i.name)}>
            <View style={s.row}>
              <Text style={s.name}>{i.name}</Text>
              <Text style={s.small}>  {i.always_on ? "always on" : "not always on"}{stale.includes(i.name) ? " · as last seen" : ""}</Text>
            </View>
            <View style={s.meter}><View style={[s.meterFill, { width: `${frac * 100}%`, backgroundColor: frac > 0.85 ? C.amber : C.violet }]} /></View>
            <Text style={s.small}>{storageText(i)}</Text>
            {(i.held || []).map((h) => (
              <View key={h.source} style={[s.row, { marginTop: 4 }]}>
                <Dot tone={h.error ? "bad" : h.pending > h.adopted ? "warn" : "ok"} />
                <Text style={[s.body, { width: 92 }]}>{h.source}</Text>
                <Text style={[s.small, { flex: 1 }]}>{h.error || (h.pending > h.adopted ? `fetching, ${h.missing} objects to go` : `in step (${size(h.bytes)})`)}</Text>
              </View>
            ))}
            {(i.held || []).length === 0 && <Text style={s.small}>Holds nobody's backup yet.</Text>}
          </Pressable>
        );
      })}
    </ScrollView>
  );
}

// --- Metrics --------------------------------------------------------------------------------------

function Chart({ title, values, failed, label, colour, fromZero = true }: {
  title: string; values: number[]; failed: boolean[]; label: (v: number) => string; colour: string; fromZero?: boolean;
}) {
  const hs = bars(values, fromZero);
  return (
    <View style={{ marginTop: 14 }}>
      <Text style={s.body}>{title}  <Text style={s.small}>{values.length ? `last ${label(values[values.length - 1])}, most ${label(Math.max(...values))}` : "no runs yet"}</Text></Text>
      <View style={s.chart}>
        {hs.map((h, k) => (
          <View key={k} style={{ flex: 1, maxWidth: 24, height: `${h * 100}%`, backgroundColor: failed[k] ? C.rust : colour, marginHorizontal: 1 }} />
        ))}
      </View>
    </View>
  );
}

function Metrics({ machine }: { machine: Machine }) {
  const [runs, setRuns] = useState<Run[] | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const load = useCallback(async () => {
    setLoading(true);
    try { setRuns((await api.runs(machine.base)).runs || []); setError(""); }
    catch (e: any) { setError(e.message); }
    finally { setLoading(false); }
  }, [machine.base]);
  useEffect(() => { load(); }, [load]);

  const i = machine.info, rs = runs || [], last = i.last_run;
  const ok = rs.filter((r) => r.ok).length;
  const sized = rs.filter((r) => r.stored !== undefined);
  const stats: [string, string][] = [["storage used", `${size(i.used)} / ${size(i.quota)}`]];
  if (i.source) {
    stats.push(["repository", size(last?.stored)], ["runs that worked", rs.length ? Math.round((100 * ok) / rs.length) + "%" : "-"],
      ["files", last?.files !== undefined ? String(last.files) : "-"], ["read last run", size(last?.bytes)]);
  }
  if (i.mirror) stats.push(["holds", `${(i.held || []).length} sources`]);
  return (
    <ScrollView contentContainerStyle={s.pad} refreshControl={<RefreshControl refreshing={loading} onRefresh={load} tintColor={C.phosphor} />}>
      <Text style={s.h}>{i.name}: {roleOf(i)}{i.source ? `, ${rs.length} runs` : ""}</Text>
      {error !== "" && <Text style={[s.small, { color: C.rust }]}>{error}</Text>}
      <View style={s.stats}>
        {stats.map(([k, v]) => (
          <View key={k} style={{ minWidth: "30%", marginBottom: 10 }}>
            <Text style={s.stat}>{v}</Text>
            <Text style={s.small}>{k}</Text>
          </View>
        ))}
      </View>
      {i.source ? (
        <>
          <Chart title="Added to the repository" colour={C.phosphor} label={size}
            values={rs.map((r) => r.data_added || 0)} failed={rs.map((r) => !r.ok)} />
          <Chart title="Repository size" colour={C.violet} label={size} fromZero={false}
            values={sized.map((r) => r.stored!)} failed={sized.map((r) => !r.ok)} />
          <Chart title="How long a run took" colour={C.sky} label={duration}
            values={rs.map(seconds)} failed={rs.map((r) => !r.ok)} />
          <Text style={[s.h, { marginTop: 18 }]}>Runs</Text>
          {[...rs].reverse().map((r) => (
            <View key={r.started} style={s.runRow}>
              <View style={s.row}>
                <Dot tone={r.ok ? "ok" : "bad"} />
                <Text style={s.body}>{new Date(r.started).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" })}</Text>
                <Text style={s.small}>   {duration(seconds(r))}</Text>
              </View>
              <Text style={[s.small, { marginLeft: 17 }, !r.ok && { color: C.rust }]}>{runText(r)}</Text>
            </View>
          ))}
        </>
      ) : <Text style={s.dim}>{i.name} backs nothing up, so it has no runs.</Text>}
    </ScrollView>
  );
}

// --- Settings -------------------------------------------------------------------------------------

function Check({ label, about, on, onChange, disabled }: { label: string; about?: string; on: boolean; onChange: (v: boolean) => void; disabled?: boolean }) {
  return (
    <Pressable onPress={() => !disabled && onChange(!on)} style={s.check}>
      <View style={[s.box, on && { borderColor: C.phosphor }]}>{on && <View style={s.boxOn} />}</View>
      <View style={{ flex: 1 }}>
        <Text style={[s.body, disabled && { color: C.ash }]}>{label}</Text>
        {!!about && <Text style={s.small}>{about}</Text>}
      </View>
    </Pressable>
  );
}

function Field({ label, value, onChange, disabled, multiline, keyboard, width }: {
  label: string; value: string; onChange: (v: string) => void; disabled?: boolean; multiline?: boolean; keyboard?: "numeric"; width?: number;
}) {
  return (
    <View style={{ marginTop: 10, width }}>
      <Text style={s.small}>{label}</Text>
      <TextInput value={value} onChangeText={onChange} editable={!disabled} multiline={multiline} keyboardType={keyboard}
        autoCapitalize="none" autoCorrect={false} placeholderTextColor={C.ash}
        style={[s.input, multiline && { minHeight: 64, textAlignVertical: "top", fontFamily: "monospace" }, disabled && { color: C.ash }]} />
    </View>
  );
}

function SettingsScreen({ machine, machines, peers, onSaved, onError }: {
  machine: Machine; machines: Machine[]; peers: Peer[]; onSaved: (restart: boolean) => void; onError: (msg: string) => void;
}) {
  const i = machine.info, ro = !i.owner;
  const [f, setF] = useState<Form>(() => formOf(i));
  const [presets, setPresets] = useState<Record<string, { about: string }>>({});
  const [saving, setSaving] = useState(false);
  const set = (p: Partial<Form>) => setF((x) => ({ ...x, ...p }));
  useEffect(() => { api.presets(machine.base).then(setPresets).catch(() => {}); }, [machine.base]);

  const problem = formProblem(f);
  const restarts = f.mirror !== i.mirror || f.isSource !== !!i.source || (f.mirror && parseInt(f.quotaGb) !== (i.quota_gb || 10));
  const save = () => {
    const go = async () => {
      setSaving(true);
      try { const r = await api.save(machine.base, settingsOf(f)); onSaved(r.restart); }
      catch (e: any) { onError(e.message); }
      finally { setSaving(false); }
    };
    if (restarts) Alert.alert("Restart its daemon?", `${i.name}'s backup daemon restarts to apply this; it is back in about 20 s.`,
      [{ text: "Cancel", style: "cancel" }, { text: "Save", onPress: go }]);
    else go();
  };

  return (
    <ScrollView contentContainerStyle={s.pad} keyboardShouldPersistTaps="handled">
      <Text style={s.h}>{i.name}</Text>
      {ro && (
        <View style={[s.card, { borderColor: C.amber }]}>
          <Text style={s.body}>Read only: this phone is not among {i.name}'s owners. Add it there, from a device that is (Shrooms Backup in Basecamp), or in /etc/shrooms-backup/config.json.</Text>
        </View>
      )}
      <View style={s.card}>
        <Check label="Back this machine up" on={f.isSource} disabled={ro} onChange={(v) => set({ isSource: v })}
          about="It needs its key: until Keycard sets it, `sudo shrooms-backup key` on the machine." />
        {f.isSource && (
          <>
            <Text style={[s.small, { marginTop: 8 }]}>What it backs up</Text>
            {Object.keys(presets).sort().map((p) => (
              <Check key={p} label={p} about={presets[p].about} on={f.presets.includes(p)} disabled={ro}
                onChange={(v) => set({ presets: toggled(f.presets, p, v) })} />
            ))}
            <Field label="More folders, one a line (absolute)" value={f.paths} multiline disabled={ro} onChange={(v) => set({ paths: v })} />
            <Field label={'Leave out, one pattern a line ("*.tmp", "cache", "/abs/path")'} value={f.exclude} multiline disabled={ro} onChange={(v) => set({ exclude: v })} />
            <Field label="When (HH:MM, its local time)" value={f.times} disabled={ro} onChange={(v) => set({ times: v })} />
            <View style={[s.row, { gap: 16 }]}>
              <Field label="Keep days" value={f.keepDaily} keyboard="numeric" width={100} disabled={ro} onChange={(v) => set({ keepDaily: v })} />
              <Field label="Keep weeks" value={f.keepWeekly} keyboard="numeric" width={100} disabled={ro} onChange={(v) => set({ keepWeekly: v })} />
            </View>
            <Text style={[s.small, { marginTop: 12 }]}>Kept by</Text>
            {mirrorChoices(i.name, machines, f.mirrors).map((c) => (
              <Check key={c.name} label={c.name} about={c.about} on={f.mirrors.includes(c.name)} disabled={ro}
                onChange={(v) => set({ mirrors: toggled(f.mirrors, c.name, v) })} />
            ))}
            <Check label="Keep a copy here too" on={f.keepLocal} disabled={ro} onChange={(v) => set({ keepLocal: v })}
              about="Otherwise its storage is only a staging area, emptied once every mirror has the run." />
          </>
        )}
      </View>
      <View style={s.card}>
        <View style={s.switchRow}>
          <View style={{ flex: 1 }}>
            <Text style={s.body}>Keep copies for other machines</Text>
            <Text style={s.small}>Changing this restarts its daemon.</Text>
          </View>
          <Switch value={f.mirror} disabled={ro} onValueChange={(v) => set({ mirror: v })} trackColor={{ true: C.phosphor, false: C.line }} thumbColor={C.bone} />
        </View>
        {f.mirror && <Field label={"The most backups may take here, GB" + (i.disk ? ` (its disk: ${size(i.disk.free)} free of ${size(i.disk.total)})` : "") + "; changing it restarts the daemon"}
          value={f.quotaGb} keyboard="numeric" disabled={ro} onChange={(v) => set({ quotaGb: v })} />}
        <View style={[s.switchRow, { marginTop: 10 }]}>
          <View style={{ flex: 1 }}>
            <Text style={s.body}>Always on</Text>
            <Text style={s.small}>Says so to the others: a laptop is a weak mirror.</Text>
          </View>
          <Switch value={f.alwaysOn} disabled={ro} onValueChange={(v) => set({ alwaysOn: v })} trackColor={{ true: C.phosphor, false: C.line }} thumbColor={C.bone} />
        </View>
      </View>
      <View style={s.card}>
        <Text style={s.body}>Owners</Text>
        <Text style={s.small}>Devices that may change these settings, start runs and restore any backup kept here. The machine itself always may.</Text>
        {ownerChoices(i.name, peers, machines, f.owners).map((o) => (
          <Check key={o} label={o} on={f.owners.includes(o)} disabled={ro} onChange={(v) => set({ owners: toggled(f.owners, o, v) })} />
        ))}
      </View>
      {problem !== "" && <Text style={[s.small, { color: C.amber, marginBottom: 8 }]}>{problem}</Text>}
      <View style={[s.row, { gap: 10 }]}>
        <Button text={saving ? "Saving…" : "Save"} primary disabled={ro || saving || problem !== ""} onPress={save} />
        <Button text="Undo changes" onPress={() => setF(formOf(i))} />
      </View>
    </ScrollView>
  );
}

// --- machines -------------------------------------------------------------------------------------

function AddMachines({ visible, seeds, onClose, onAdd, onForget }: {
  visible: boolean; seeds: string[]; onClose: () => void; onAdd: (a: string) => void; onForget: (a: string) => void;
}) {
  const [text, setText] = useState("");
  const a = text.trim();
  return (
    <Modal visible={visible} transparent animationType="fade" onRequestClose={onClose}>
      <View style={s.modalBack}>
        <View style={s.modal}>
          <Text style={s.h}>Machines</Text>
          <Text style={s.small}>One machine that runs Shrooms Backup is enough: the phone finds the others on its meshes through it. Its mesh name (proteus.office.mesh) or address.</Text>
          <TextInput value={text} onChangeText={setText} autoCapitalize="none" autoCorrect={false} placeholder="proteus.office.mesh"
            placeholderTextColor={C.ash} style={[s.input, { marginTop: 10 }]} onSubmitEditing={() => { if (a) { onAdd(a); setText(""); } }} />
          <View style={[s.row, { gap: 10, marginTop: 10 }]}>
            <Button text="Add" primary disabled={!/^[A-Za-z0-9.:\[\]-]{1,128}$/.test(a)} onPress={() => { onAdd(a); setText(""); }} />
            {seeds.length > 0 && <Button text="Close" onPress={onClose} />}
          </View>
          {seeds.length > 0 && <Text style={[s.small, { marginTop: 14 }]}>Asked first</Text>}
          {seeds.map((x) => (
            <View key={x} style={[s.row, { justifyContent: "space-between", marginTop: 6 }]}>
              <Text style={s.body}>{x}</Text>
              <Pressable onPress={() => onForget(x)} hitSlop={8}><Text style={[s.small, { color: C.rust }]}>Forget</Text></Pressable>
            </View>
          ))}
        </View>
      </View>
    </Modal>
  );
}

const s = StyleSheet.create({
  header: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: 16, paddingTop: 10, paddingBottom: 6 },
  title: { color: C.bone, fontSize: 20, fontWeight: "700" },
  headerLink: { color: C.sky, fontSize: 14 },
  tabs: { flexDirection: "row", paddingHorizontal: 12, borderBottomWidth: 1, borderBottomColor: C.line },
  tab: { paddingVertical: 10, paddingHorizontal: 10 },
  tabOn: { borderBottomWidth: 2, borderBottomColor: C.phosphor },
  tabText: { color: C.ash, fontSize: 14, fontWeight: "600" },
  notice: { paddingHorizontal: 16, paddingVertical: 6, fontSize: 13, backgroundColor: C.panel },
  chips: { flexGrow: 0, flexShrink: 0 },
  chip: { flexDirection: "row", alignItems: "center", gap: 6, paddingHorizontal: 12, paddingVertical: 7, borderRadius: 16, borderWidth: 1, borderColor: C.line, backgroundColor: C.panel },
  chipOn: { borderColor: C.phosphor },
  chipText: { color: C.bone, fontSize: 13 },
  pad: { padding: 12, paddingBottom: 40 },
  h: { color: C.bone, fontSize: 16, fontWeight: "700", marginTop: 6, marginBottom: 8 },
  dim: { color: C.ash, fontSize: 14, lineHeight: 20 },
  card: { backgroundColor: C.panel, borderColor: C.line, borderWidth: 1, borderRadius: 8, padding: 12, marginBottom: 10 },
  row: { flexDirection: "row", alignItems: "center" },
  name: { color: C.bone, fontSize: 16, fontWeight: "700" },
  body: { color: C.bone, fontSize: 14, lineHeight: 20 },
  small: { color: C.ash, fontSize: 12, lineHeight: 17 },
  dot: { width: 9, height: 9, borderRadius: 5, marginRight: 8 },
  btn: { paddingHorizontal: 14, paddingVertical: 8, borderRadius: 6, borderWidth: 1, borderColor: C.line, backgroundColor: C.raised },
  btnPrimary: { backgroundColor: C.phosphor, borderColor: C.phosphor },
  btnOff: { backgroundColor: C.line, borderColor: C.line },
  btnText: { color: C.bone, fontSize: 14, fontWeight: "600" },
  meter: { height: 8, borderRadius: 4, backgroundColor: C.void, borderWidth: 1, borderColor: C.line, marginTop: 8, marginBottom: 4, overflow: "hidden" },
  meterFill: { height: "100%", borderRadius: 4, minWidth: 2 },
  stats: { flexDirection: "row", flexWrap: "wrap", gap: 12 },
  stat: { color: C.bone, fontSize: 18 },
  chart: { height: 80, flexDirection: "row", alignItems: "flex-end", backgroundColor: C.void, borderRadius: 4, marginTop: 6, paddingTop: 4 },
  runRow: { paddingVertical: 6, borderBottomWidth: 1, borderBottomColor: C.line },
  check: { flexDirection: "row", alignItems: "flex-start", gap: 10, paddingVertical: 7 },
  box: { width: 20, height: 20, borderRadius: 4, borderWidth: 1.5, borderColor: C.ash, alignItems: "center", justifyContent: "center", marginTop: 1 },
  boxOn: { width: 10, height: 10, borderRadius: 2, backgroundColor: C.phosphor },
  input: { color: C.bone, backgroundColor: C.void, borderWidth: 1, borderColor: C.line, borderRadius: 6, paddingHorizontal: 10, paddingVertical: 8, fontSize: 14, marginTop: 4 },
  switchRow: { flexDirection: "row", alignItems: "center", gap: 12 },
  modalBack: { flex: 1, backgroundColor: "rgba(0,0,0,0.7)", justifyContent: "center", padding: 20 },
  modal: { backgroundColor: C.panel, borderRadius: 10, borderWidth: 1, borderColor: C.line, padding: 16 },
});
