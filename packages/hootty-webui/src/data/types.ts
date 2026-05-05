// Shared domain types. No dependency on any concrete DataSource.

export interface SessionSummary {
  key: string;
  alive: boolean;
  state: "starting" | "running" | "exited" | "unknown";
  cmd: string; // e.g. "bash -l", "vim README.md"
  startedAt: string; // ISO 8601
  pid?: number;
}
