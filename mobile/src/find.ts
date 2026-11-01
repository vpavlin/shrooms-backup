// Finding the daemons: ask each machine the phone was told about for its info and its mesh peers,
// then every online peer at once. A machine that does not answer is skipped, not waited on.
import { api } from "./api";
import { baseUrl, dedupe, type Machine, type Peer } from "./model";

export async function findMachines(seeds: string[]): Promise<{ machines: Machine[]; peers: Peer[]; answered: number }> {
  const found: Machine[] = [];
  const peers = new Map<string, Peer>();
  let answered = 0;
  const probe = async (base: string) => {
    try { found.push({ name: "", base, info: await api.info(base) }); return true; } catch { return false; }
  };
  await Promise.all(seeds.map(async (s) => {
    const base = baseUrl(s);
    if (await probe(base)) answered++;
    try { for (const p of (await api.peers(base)).peers || []) if (p && p.name) peers.set(p.name + "|" + p.mesh, p); } catch { /* an older daemon, or off */ }
  }));
  const known = new Set(found.map((m) => m.info.name));
  // The IPv6 overlay first; the IPv4 alias where that fails (a network or an emulator without
  // IPv6 still reaches it: the mesh maps the alias to the same machine).
  await Promise.all([...peers.values()].filter((p) => p.online && p.overlay && !known.has(p.name))
    .map(async (p) => { if (!(await probe(baseUrl(p.overlay))) && p.overlay_v4) await probe(baseUrl(p.overlay_v4)); }));
  const ms = dedupe(found).map((m) => ({ ...m, name: m.info.name }));
  return { machines: ms, peers: [...peers.values()], answered };
}
