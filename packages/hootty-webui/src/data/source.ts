import type { AttachStream } from "@hayeah/hootty-termui";
import type { SessionSummary } from "./types";

// AttachStream is re-exported from @hayeah/hootty-termui — the same byte-
// pipe contract both the mock and live data sources produce,
// consumed by @hayeah/hootty-termui's TerminalPane.
export type { AttachStream };

// DataSource is the only API the views see. MockDataSource (for
// /preview) and LiveDataSource (for the real app) both implement
// it. Views never import from mock.ts or anything backend-specific.
export interface DataSource {
  sessions: SessionSummary[];
  attach(sessionKey: string): AttachStream;

  // createSession spawns a new session with the given shell command
  // (e.g. "bash -l"). Mock returns immediately with a fake summary;
  // Live POSTs to /api/sessions and waits for the hootty's
  // initial state to be published.
  createSession(cmd: string): Promise<SessionSummary>;

  // closeSession tears down a session. Mock removes it from the
  // list; Live sends SIGTERM via /api/sessions/<key>/close (or
  // DELETE) and removes it once the flock releases.
  closeSession(key: string): Promise<void>;
}
