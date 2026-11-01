// What the phone remembers: the machines it was told about, and what it last saw of every machine,
// so the screen shows something at once on open while they are asked again.
import AsyncStorage from "@react-native-async-storage/async-storage";
import type { Machine, Peer } from "./model";

const K = { seeds: "sb.seeds", seen: "sb.seen", peers: "sb.peers" };

async function getJson<T>(k: string, fallback: T): Promise<T> {
  try { const v = await AsyncStorage.getItem(k); return v ? (JSON.parse(v) as T) : fallback; } catch { return fallback; }
}
const setJson = (k: string, v: unknown) => AsyncStorage.setItem(k, JSON.stringify(v)).catch(() => {});

export const store = {
  seeds: () => getJson<string[]>(K.seeds, []),
  setSeeds: (s: string[]) => setJson(K.seeds, s),
  seen: () => getJson<Machine[]>(K.seen, []),
  setSeen: (m: Machine[]) => setJson(K.seen, m),
  peers: () => getJson<Peer[]>(K.peers, []),
  setPeers: (p: Peer[]) => setJson(K.peers, p),
};
