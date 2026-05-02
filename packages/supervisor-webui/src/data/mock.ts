import { makeAutoObservable, runInAction } from "mobx";
import type { AttachStream, DataSource } from "./source";
import type { SessionSummary } from "./types";

// Scripted output for each session. Chunks arrive over time so the
// preview feels like a real attach. Each string is bytes to feed
// into the terminal; ANSI escapes welcome.
const scripts: Record<string, string[]> = {
  bash: [
    "\x1b[32muser@ptydemo\x1b[0m:\x1b[34m~\x1b[0m$ ",
    "ls -la\r\n",
    "\x1b[36mtotal 40\x1b[0m\r\n",
    "drwxr-xr-x  8 user  staff  256 Apr 22 16:42 \x1b[34m.\x1b[0m\r\n",
    "drwxr-xr-x 113 user  staff 3616 Apr 22 13:14 \x1b[34m..\x1b[0m\r\n",
    "-rw-r--r--  1 user  staff 53531 Apr 22 16:42 spec.md\r\n",
    "-rw-r--r--  1 user  staff 19824 Apr 22 16:44 worklog.md\r\n",
    "\x1b[32muser@ptydemo\x1b[0m:\x1b[34m~\x1b[0m$ ",
  ],
  vim: [
    "\x1b[?1049h\x1b[?1h\x1b=\x1b[H\x1b[2J",
    "\x1b[1;1H  1 \x1b[38;5;176m# ptydemo\x1b[0m\r\n",
    "\x1b[2;1H  2 \r\n",
    "\x1b[3;1H  3 A bounded demo of the supervisor library.\r\n",
    "\x1b[4;1H  4 \r\n",
    "\x1b[5;1H  5 ## Usage\r\n",
    "\x1b[6;1H  6 \r\n",
    "\x1b[7;1H  7 ```\r\n",
    "\x1b[8;1H  8 ptydemo run -- bash\r\n",
    "\x1b[9;1H  9 ptydemo attach <key>\r\n",
    "\x1b[10;1H 10 ```\r\n",
    "\x1b[24;1H\x1b[7m-- INSERT --\x1b[0m",
  ],
  htop: [
    "\x1b[2J\x1b[H",
    "  \x1b[32m1\x1b[0m [\x1b[32m||||||||\x1b[0m\x1b[37m                      \x1b[0m 23.1%]\r\n",
    "  \x1b[32m2\x1b[0m [\x1b[32m|||||\x1b[0m\x1b[37m                         \x1b[0m 14.2%]\r\n",
    "  \x1b[32m3\x1b[0m [\x1b[32m|||\x1b[0m\x1b[37m                           \x1b[0m  8.7%]\r\n",
    "  \x1b[32m4\x1b[0m [\x1b[32m||||||\x1b[0m\x1b[37m                        \x1b[0m 18.5%]\r\n",
    " Mem[\x1b[32m|||||||||\x1b[0m\x1b[33m||||||\x1b[0m\x1b[37m        \x1b[0m 6.2G/16G]\r\n",
    " Swp[\x1b[37m                               \x1b[0m 0K/0K ]\r\n",
    "\r\n",
    "  \x1b[1mPID USER      PRI  NI  VIRT   RES   SHR S CPU% MEM%   TIME+  Command\x1b[0m\r\n",
    " 1337 user       20   0  4.2G  256M   32M S  3.1  1.6  1:23.45 \x1b[32mclaude\x1b[0m\r\n",
    " 1338 user       20   0  1.1G   64M   16M S  1.4  0.4  0:42.11 \x1b[32mcodex\x1b[0m\r\n",
    " 1339 user       20   0  512M   28M   12M S  0.2  0.2  0:05.33 ptydemo\r\n",
  ],
};

function makeSummary(
  key: string,
  state: SessionSummary["state"],
  cmd: string,
  minutesAgo: number,
  pid?: number,
): SessionSummary {
  return {
    key,
    alive: state !== "exited",
    state,
    cmd,
    startedAt: new Date(Date.now() - minutesAgo * 60_000).toISOString(),
    pid,
  };
}

class MockAttach implements AttachStream {
  private listeners = new Set<(bytes: Uint8Array) => void>();
  private timer: ReturnType<typeof setTimeout> | null = null;
  private closed = false;

  constructor(script: string[]) {
    // Replay the script with a small delay between chunks so the
    // screenshots look like a live terminal attach, not a dump.
    const replay = (i: number) => {
      if (this.closed || i >= script.length) return;
      const bytes = new TextEncoder().encode(script[i]);
      for (const fn of this.listeners) fn(bytes);
      this.timer = setTimeout(() => replay(i + 1), 40);
    };
    // Kick on next tick so the listener has time to subscribe.
    this.timer = setTimeout(() => replay(0), 20);
  }

  onBytes(fn: (bytes: Uint8Array) => void) {
    this.listeners.add(fn);
    return () => {
      this.listeners.delete(fn);
    };
  }

  send(_bytes: Uint8Array) {
    // Preview is passive — user keystrokes are dropped. Live mode
    // forwards to the backend.
  }

  resize(_cols: number, _rows: number) {
    // No-op in the mock.
  }

  close() {
    this.closed = true;
    if (this.timer) clearTimeout(this.timer);
    this.listeners.clear();
  }
}

export class MockDataSource implements DataSource {
  sessions: SessionSummary[] = [
    makeSummary("bash", "running", "bash -l", 12, 8401),
    makeSummary("vim", "running", "vim README.md", 4, 8420),
    makeSummary("htop", "running", "htop", 2, 8431),
    makeSummary("build-00", "exited", "go build ./...", 45),
  ];

  constructor() {
    makeAutoObservable(this);
  }

  attach(sessionKey: string): AttachStream {
    const scriptKey = sessionKey.split("-")[0] ?? "bash";
    const script = scripts[scriptKey] ?? scripts.bash;
    return new MockAttach(script);
  }

  async closeSession(key: string): Promise<void> {
    runInAction(() => {
      const idx = this.sessions.findIndex((s) => s.key === key);
      if (idx >= 0) this.sessions.splice(idx, 1);
    });
  }

  async createSession(cmd: string): Promise<SessionSummary> {
    const trimmed = cmd.trim() || "bash -l";
    const base = trimmed.split(/\s+/)[0] ?? "sess";
    // Unique key: <base>-NN, auto-incrementing per base name.
    const existing = this.sessions.filter((s) => s.key.startsWith(`${base}-`) || s.key === base);
    const suffix = existing.length === 0 ? "" : `-${existing.length + 1}`;
    const key = `${base}${suffix}`;
    const summary: SessionSummary = {
      key,
      alive: true,
      state: "starting",
      cmd: trimmed,
      startedAt: new Date().toISOString(),
      pid: 9000 + this.sessions.length,
    };
    runInAction(() => {
      this.sessions.push(summary);
    });
    // Flip from "starting" to "running" after a short beat so the
    // preview shows the state transition naturally.
    setTimeout(() => this.setState(key, "running"), 300);
    return summary;
  }

  // Mutators for __tap__ so the agent can drive preview states via
  // browser eval.
  setState(key: string, state: SessionSummary["state"]) {
    runInAction(() => {
      const s = this.sessions.find((s) => s.key === key);
      if (s) {
        s.state = state;
        s.alive = state !== "exited";
      }
    });
  }

  addSession(summary: SessionSummary) {
    runInAction(() => {
      this.sessions.push(summary);
    });
  }
}
