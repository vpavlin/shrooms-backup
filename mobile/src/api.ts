// The backup daemon's HTTP API (port 7394 on each mesh address). Every call has a timeout: a machine
// that is off or off-mesh must never leave the app spinning.
import type { Info, KeyInfo, Peer, Run, Settings } from "./model";

export class ApiError extends Error {
  status: number;
  constructor(status: number, message: string) { super(message); this.status = status; }
}

async function req<T>(base: string, path: string, init: { method?: string; body?: unknown } = {}, timeoutMs = 6000): Promise<T> {
  const ctl = new AbortController();
  const timer = setTimeout(() => ctl.abort(), timeoutMs);
  let res: Response;
  try {
    res = await fetch(base + path, {
      method: init.method || "GET",
      headers: init.body !== undefined ? { "Content-Type": "application/json" } : {},
      body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
      signal: ctl.signal,
    });
  } catch {
    throw new ApiError(0, "Can't reach it right now.");
  } finally { clearTimeout(timer); }
  const text = await res.text();
  let data: any = null;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!res.ok) {
    const msg = res.status === 403 && path === "/v1/config" ? "This phone is not one of its owners."
      : data && typeof data.error === "string" ? data.error : `Something went wrong (${res.status}).`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export const api = {
  info: (b: string) => req<Info>(b, "/v1/info", {}, 5000),
  peers: (b: string) => req<{ self: string; peers: Peer[] }>(b, "/v1/peers", {}, 5000),
  runs: (b: string) => req<{ runs: Run[] }>(b, "/v1/runs"),
  presets: (b: string) => req<Record<string, { about: string }>>(b, "/v1/presets"),
  keys: (b: string) => req<{ keys: KeyInfo[]; pending?: boolean }>(b, "/v1/keys", {}, 30000),
  run: (b: string) => req<{ started: boolean }>(b, "/v1/run", { method: "POST", body: {} }),
  save: (b: string, s: Settings) => req<{ saved: boolean; restart: boolean }>(b, "/v1/config", { method: "PUT", body: s }, 10000),
};
