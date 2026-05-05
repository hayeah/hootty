import { WebSocketAttach, type AttachStream } from "@hayeah/hootty-termui";
import { makeAutoObservable, runInAction } from "mobx";
import type { DataSource } from "./source";
import type { SessionSummary } from "./types";

// Raw shape returned by GET /api/sessions. Mirrors StateFile +
// `alive` (added server-side).
interface ApiEntry {
  session: { key: string; pid?: number; created_at: string };
  state?: {
    state?: "starting" | "running" | "exited" | string;
    cmd?: string;
    pid?: number;
    exit_code?: number;
    started_at?: string;
    exited_at?: string;
  };
  alive: boolean;
}

function toSummary(e: ApiEntry): SessionSummary {
  const s = e.state ?? {};
  const state = (s.state ?? (e.alive ? "running" : "exited")) as SessionSummary["state"];
  return {
    key: e.session.key,
    alive: e.alive,
    state: ["starting", "running", "exited"].includes(state) ? state : "unknown",
    cmd: s.cmd ?? "",
    startedAt: s.started_at ?? e.session.created_at,
    pid: s.pid,
  };
}

// LiveDataSource talks to ptydemo's HTTP API (/api/*) and WS
// attach endpoint. Sessions are refreshed via polling (1s
// interval); if we wanted to be fancier we'd subscribe to a
// server-sent stream of session events, but polling a static
// JSON list is fine for the demo and keeps the implementation
// obvious.
export class LiveDataSource implements DataSource {
  sessions: SessionSummary[] = [];

  private apiBase: string;
  private wsBase: string;
  private pollTimer: ReturnType<typeof setInterval> | null = null;

  constructor(opts: { apiBase?: string } = {}) {
    this.apiBase = opts.apiBase ?? "/api";
    // ws base is http(s) → ws(s) on the current origin. We don't
    // care about the path prefix; attach builds full URLs below.
    const origin = window.location.origin.replace(/^http/, "ws");
    this.wsBase = origin + this.apiBase;
    makeAutoObservable(this, {}, { autoBind: true });
    void this.refresh();
    this.pollTimer = setInterval(() => {
      void this.refresh();
    }, 1000);
  }

  async refresh(): Promise<void> {
    try {
      const resp = await fetch(`${this.apiBase}/sessions`, {
        headers: { Accept: "application/json" },
      });
      if (!resp.ok) return;
      const body = (await resp.json()) as { sessions: ApiEntry[] };
      const next = (body.sessions ?? []).map(toSummary);
      runInAction(() => {
        this.sessions = next;
      });
    } catch {
      // Transient fetch errors are fine — next tick will retry.
    }
  }

  attach(sessionKey: string): AttachStream {
    const url = `${this.wsBase}/sessions/${encodeURIComponent(sessionKey)}/attach`;
    return new WebSocketAttach({ url });
  }

  async createSession(cmd: string): Promise<SessionSummary> {
    const trimmed = cmd.trim();
    if (!trimmed) throw new Error("cmd is required");
    const resp = await fetch(`${this.apiBase}/sessions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ cmd: trimmed }),
    });
    if (!resp.ok) {
      const text = await resp.text().catch(() => resp.statusText);
      throw new Error(`create: ${resp.status} ${text}`);
    }
    const entry = (await resp.json()) as ApiEntry;
    const summary = toSummary(entry);
    runInAction(() => {
      const idx = this.sessions.findIndex((s) => s.key === summary.key);
      if (idx >= 0) this.sessions[idx] = summary;
      else this.sessions.push(summary);
    });
    return summary;
  }

  async closeSession(key: string): Promise<void> {
    const resp = await fetch(`${this.apiBase}/sessions/${encodeURIComponent(key)}`, {
      method: "DELETE",
    });
    if (!resp.ok && resp.status !== 404) {
      throw new Error(`close: ${resp.status} ${resp.statusText}`);
    }
    // The optimistic update + next poll will remove it. For
    // snappiness we also flip alive/state locally right away.
    runInAction(() => {
      const s = this.sessions.find((s) => s.key === key);
      if (s) {
        s.alive = false;
        s.state = "exited";
      }
    });
  }

  dispose(): void {
    if (this.pollTimer) clearInterval(this.pollTimer);
    this.pollTimer = null;
  }
}
